//go:build windows

package providers

// Backup value encryption through crypt32 DPAPI only (stdlib syscall).
// Security finding S-5: registry-keys backups used to hold provider keys in
// plaintext with a 0600 mode that does not map to an ACL on Windows. DPAPI
// (user scope, CryptProtectData) binds the ciphertext to the current user
// account, so the file is useless to another account, to backup/roaming
// tooling that copies it elsewhere, and to offline forensic recovery of the
// file alone. A fixed entropy string is used as defense in depth; it is not a
// secret and must never be treated as one.

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	kernel32ForDPAPI       = syscall.NewLazyDLL("kernel32.dll")
	procLocalFreeDPAPI     = kernel32ForDPAPI.NewProc("LocalFree")
)

const cryptProtectUIForbidden = 0x1

// backupEntropy is the optional entropy blob mixed into every protected
// backup value (defense in depth; identical for all users of this tool).
var backupEntropy = []byte("agypro-keybackup-v2")

// dataBlob mirrors the Win32 DATA_BLOB.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func blobFrom(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (d dataBlob) bytes() []byte {
	if d.pbData == nil || d.cbData == 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(d.pbData, d.cbData)...)
}

func (d dataBlob) free() {
	if d.pbData != nil {
		_, _, _ = procLocalFreeDPAPI.Call(uintptr(unsafe.Pointer(d.pbData)))
	}
}

// dpapiProtector is the production SecretProtector: CryptProtectData and
// CryptUnprotectData with CRYPTPROTECT_UI_FORBIDDEN in user scope.
type dpapiProtector struct{}

func (dpapiProtector) Protect(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	in := blobFrom(plaintext)
	entropy := blobFrom(backupEntropy)
	var out dataBlob
	r, _, callErr := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		uintptr(unsafe.Pointer(&entropy)),
		0,
		0,
		uintptr(cryptProtectUIForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData: %v", callErr)
	}
	defer out.free()
	return out.bytes(), nil
}

func (dpapiProtector) Unprotect(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	in := blobFrom(ciphertext)
	entropy := blobFrom(backupEntropy)
	var out dataBlob
	r, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		uintptr(unsafe.Pointer(&entropy)),
		0,
		0,
		uintptr(cryptProtectUIForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %v", callErr)
	}
	defer out.free()
	return out.bytes(), nil
}
