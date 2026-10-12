//go:build windows

package providers

// Persistent environment access through advapi32 only (stdlib syscall, no
// third-party packages, no shell). Replacing the old powershell.exe path is a
// security fix (S-2): the previous Set/Delete built a script with the key
// name and value base64-encoded on the argv, where any process-list, ETW or
// command-line-auditing observer could decode them. The registry API moves
// names and values through pointers, never through a command line, and keeps
// names as exact UTF-16 so a leading U+FEFF survives (the reason the old code
// avoided reg.exe in the first place).

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	advapi32             = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyExW    = advapi32.NewProc("RegOpenKeyExW")
	procRegCloseKey      = advapi32.NewProc("RegCloseKey")
	procRegEnumValueW    = advapi32.NewProc("RegEnumValueW")
	procRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")
	procRegSetValueExW   = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW  = advapi32.NewProc("RegDeleteValueW")
)

const (
	regHKEYCurrentUser   = uintptr(0x80000001)
	regHKEYLocalMachine  = uintptr(0x80000002)
	regKeyRead           = uintptr(0x20019) // KEY_READ
	regKeySetValue       = uintptr(0x0002)  // KEY_SET_VALUE
	regTypeSZ            = 1
	regTypeExpandSZ      = 2
	regErrorFileNotFound = 2
	regErrorNoMoreItems  = 259
	regErrorMoreData     = 234
)

// win32Registry is the production registryBackend: direct HKCU\Environment
// and HKLM Session Manager\Environment access through the advapi32 API.
type win32Registry struct{}

// regRootForKey maps an EnvRoot* name to the HKEY and subkey that back it.
func regRootForKey(root string) (uintptr, string, error) {
	switch root {
	case EnvRootUser:
		return regHKEYCurrentUser, "Environment", nil
	case EnvRootMachine:
		return regHKEYLocalMachine, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, nil
	default:
		return 0, "", fmt.Errorf("unsupported environment root %q (want %q or %q)", root, EnvRootUser, EnvRootMachine)
	}
}

func openRegKey(root string, access uintptr) (syscall.Handle, error) {
	hkey, subkey, err := regRootForKey(root)
	if err != nil {
		return 0, err
	}
	sub, err := syscall.UTF16PtrFromString(subkey)
	if err != nil {
		return 0, err
	}
	var h syscall.Handle
	r, _, callErr := procRegOpenKeyExW.Call(hkey, uintptr(unsafe.Pointer(sub)), 0, access, uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		if r == regErrorFileNotFound {
			return 0, fmt.Errorf("registry key for %q not found: %w", root, callErr)
		}
		return 0, fmt.Errorf("RegOpenKeyExW(%s) failed: %v (code %d)", subkey, callErr, r)
	}
	return h, nil
}

func closeRegKey(h syscall.Handle) {
	_, _, _ = procRegCloseKey.Call(uintptr(h))
}

// queryRegValue reads one REG_SZ/REG_EXPAND_SZ value as a raw string.
// Non-string types are reported as absent: the environment store only holds
// strings, and guessing at binary blobs would corrupt lookups.
func queryRegValue(h syscall.Handle, name string) (string, bool, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return "", false, err
	}
	query := func(data *byte, size *uint32) (uint32, uintptr) {
		var regType uint32
		r, _, _ := procRegQueryValueExW.Call(
			uintptr(h),
			uintptr(unsafe.Pointer(namePtr)),
			0,
			uintptr(unsafe.Pointer(&regType)),
			uintptr(unsafe.Pointer(data)),
			uintptr(unsafe.Pointer(size)),
		)
		return regType, r
	}
	return queryRegValueWith(query, name)
}

// regQueryFn is one RegQueryValueExW-shaped call: with data == nil it reports
// the value type and size, otherwise it fills the caller's buffer. It is the
// seam that keeps the zero-length path unit-testable without a live hive.
type regQueryFn func(data *byte, size *uint32) (regType uint32, r uintptr)

// queryRegValueWith reads one REG_SZ/REG_EXPAND_SZ value as a raw string.
// Non-string types are reported as absent: the environment store only holds
// strings, and guessing at binary blobs would corrupt lookups. A zero-length
// string value is present but empty (R2-1): it must return "" instead of
// taking the address of a zero-length buffer, which panics and used to crash
// every registry-dependent path on one empty REG_SZ anywhere in the hive.
func queryRegValueWith(query regQueryFn, name string) (string, bool, error) {
	var regType uint32
	var size uint32
	regType, r := query(nil, &size)
	if r != 0 {
		return "", false, nil
	}
	if regType != regTypeSZ && regType != regTypeExpandSZ {
		return "", false, nil
	}
	if size == 0 {
		// Empty REG_SZ/REG_EXPAND_SZ (e.g. `setx NAME ""`, or a regedit
		// "New String Value" before it is edited): present, value "".
		return "", true, nil
	}
	buf := make([]byte, size)
	if _, r := query(&buf[0], &size); r != 0 {
		return "", false, nil
	}
	return utf16BytesToString(buf[:size]), true, nil
}

// enumRegValueNames lists every value name under h, preserving exact UTF-16
// (a leading U+FEFF in a name must survive; that is the whole reason this
// layer exists instead of parsing reg.exe output).
func enumRegValueNames(h syscall.Handle) ([]string, error) {
	var names []string
	buf := make([]uint16, 256)
	for index := uint32(0); ; index++ {
		nameLen := uint32(len(buf))
		var regType uint32
		r, _, _ := procRegEnumValueW.Call(
			uintptr(h),
			uintptr(index),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&nameLen)),
			0,
			uintptr(unsafe.Pointer(&regType)),
			0,
			0,
		)
		if r == regErrorNoMoreItems {
			return names, nil
		}
		if r == regErrorMoreData {
			buf = make([]uint16, nameLen+1)
			index-- // retry the same index with the larger buffer
			continue
		}
		if r != 0 {
			return nil, fmt.Errorf("RegEnumValueW(index %d) failed: code %d", index, r)
		}
		names = append(names, syscall.UTF16ToString(buf[:nameLen]))
	}
}

func (win32Registry) enumerate(root string) (map[string]string, error) {
	h, err := openRegKey(root, regKeyRead)
	if err != nil {
		return nil, err
	}
	defer closeRegKey(h)
	names, err := enumRegValueNames(h)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		if v, ok, err := queryRegValue(h, n); err == nil && ok {
			out[n] = v
		}
	}
	return out, nil
}

func (win32Registry) query(root, name string) (string, bool, error) {
	h, err := openRegKey(root, regKeyRead)
	if err != nil {
		return "", false, err
	}
	defer closeRegKey(h)
	return queryRegValue(h, name)
}

func (win32Registry) set(root, name, value string) error {
	h, err := openRegKey(root, regKeyRead|regKeySetValue)
	if err != nil {
		return err
	}
	defer closeRegKey(h)
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	data := utf16BytesFromString(value)
	r, _, callErr := procRegSetValueExW.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(namePtr)),
		0,
		regTypeSZ,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
	)
	if r != 0 {
		return fmt.Errorf("RegSetValueExW(%q) failed: %v (code %d)", name, callErr, r)
	}
	return nil
}

func (win32Registry) delete(root, name string) error {
	h, err := openRegKey(root, regKeyRead|regKeySetValue)
	if err != nil {
		return err
	}
	defer closeRegKey(h)
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	r, _, callErr := procRegDeleteValueW.Call(uintptr(h), uintptr(unsafe.Pointer(namePtr)))
	if r != 0 {
		if r == regErrorFileNotFound {
			return nil // deleting a missing value is not an error (EnvStore contract)
		}
		return fmt.Errorf("RegDeleteValueW(%q) failed: %v (code %d)", name, callErr, r)
	}
	return nil
}

// utf16BytesFromString encodes s as little-endian UTF-16 including the
// terminating NUL, matching what RegSetValueExW expects for REG_SZ.
func utf16BytesFromString(s string) []byte {
	u := syscall.StringToUTF16(s) // includes the terminating NUL
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[i*2] = byte(c)
		b[i*2+1] = byte(c >> 8)
	}
	return b
}

// utf16BytesToString decodes little-endian UTF-16 registry data, dropping any
// terminating NUL but keeping every other code unit (U+FEFF included).
func utf16BytesToString(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	n := len(b) / 2
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = uint16(b[i*2]) | uint16(b[i*2+1])<<8
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return syscall.UTF16ToString(u)
}
