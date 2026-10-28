//go:build windows

package agenttools

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// These functions are the FIX-A1 (S2) and FIX-D1a (R3-2 / R3-3) Win32 arms: they
// are the only place in this package that asks the OS what a name really
// resolves to, plus the one switch that decides whether the host-side name SHAPE
// rule applies. The non-Windows file (guard_win32_other.go) stubs all three, so
// guard.go carries no build tag.

var (
	guardKernel32             = syscall.NewLazyDLL("kernel32.dll")
	guardGetLongPathNameWProc = guardKernel32.NewProc("GetLongPathNameW")
)

// win32StreamSyntaxRule turns on the R3-2 refusal in guard.go: on this host a
// component carrying ':' addresses an NTFS alternate data stream, never a file
// of that name. The wording and the call sites stay platform-neutral; only this
// const decides whether the host refuses it, because a POSIX filename may legally
// contain ':'.
const win32StreamSyntaxRule = true

// getLongPathName is kernel32!GetLongPathNameW: it returns the long form of p
// with each 8.3 short component expanded and the on-disk casing applied, or
// ok=false when p (or any component of it) does not exist. It is an EXISTENCE
// lookup, which is why the guard cannot use it for a name that does not exist
// yet (the shape arm covers that case). Measured (FIX-D1a probe B6): it also
// answers 0 for a path carrying a stream separator, which is why the expansion
// arm cannot be the only answer for those paths.
func getLongPathName(p string) (string, bool) {
	if strings.TrimSpace(p) == "" {
		return "", false
	}
	p16, err := syscall.UTF16FromString(p)
	if err != nil || len(p16) == 0 {
		return "", false
	}
	n, _, _ := guardGetLongPathNameWProc.Call(uintptr(unsafe.Pointer(&p16[0])), 0, 0)
	if n == 0 || n > 0x7fff {
		return "", false
	}
	buf := make([]uint16, n+1)
	n, _, _ = guardGetLongPathNameWProc.Call(
		uintptr(unsafe.Pointer(&p16[0])),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if n == 0 || int(n) > len(buf) {
		return "", false
	}
	return syscall.UTF16ToString(buf[:n]), true
}

// win32ExpandWriteTarget returns the long form of a write target's LONGEST
// EXISTING ANCESTOR with the not-yet-existing tail rejoined verbatim: the target
// itself when it already exists (the replace_in_file case), otherwise the nearest
// ancestor directory GetLongPathNameW can resolve, plus every component below it
// exactly as the seat wrote them. ok=false means not even the volume root
// resolves, so no expansion is available at all.
//
// FIX-D1a (R3-3) widened this from "the immediate parent only" to the whole
// ancestor chain, because the A1 form went inert exactly where it mattered: a
// write through an alias into directories that do not exist yet
// ("PROVID~1/newdir/sub/x.md") has no resolvable parent (newdir does not exist),
// so the arm answered ok=false and the caller's early return allowed the write -
// while MkdirAll proceeded to create newdir and sub THROUGH the alias, landing
// everything inside ProvidersRegistryBackup. Walking up costs one
// GetLongPathNameW call per path component and stops at the first ancestor that
// exists.
//
// The rejoined tail is deliberately not lowercased or canonicalized: the guard
// compares it against what the seat wrote, and a difference there is the signal
// the governance/VCS comparison needs.
//
// FIX-E1 (N7): the tail is assembled by ONE append per level and reversed at the
// join. The D1a form prepended a fresh copy of the tail at every level, so a path
// of n components copied O(n^2) strings (~2GB on a 32KB path).
func win32ExpandWriteTarget(absTarget string) (string, bool) {
	if long, ok := getLongPathName(absTarget); ok {
		return long, true
	}
	cur := absTarget
	// tail holds the not-yet-existing components from the DEEPEST one up: the
	// walk climbs, so appending and reversing costs one cheap append per level
	// (N7). The D1a form prepended a copy of the whole tail per level, which is
	// quadratic in path depth: a ~32KB path is ~2GB of copying.
	tail := make([]string, 0, 8)
	for {
		parent := filepath.Dir(cur)
		if parent == cur || parent == "" {
			return "", false
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
		if long, ok := getLongPathName(cur); ok {
			parts := make([]string, 0, len(tail)+1)
			parts = append(parts, long)
			for i := len(tail) - 1; i >= 0; i-- {
				parts = append(parts, tail[i])
			}
			return filepath.Join(parts...), true
		}
	}
}

// win32ShortAliasShape reports whether one path component has the classic
// generated 8.3 alias shape: a "~" followed by one to five digits directly
// before a dot (or at the end of the name), in any case, so "PROVID~1.JSON",
// "provid~12", "PROVID~1" and "file~1.tar.gz" all have it.
//
// It is a SHAPE test, not a resolution: the guard cannot prove an alias exists
// (that needs an existing target and a volume with 8.3 generation enabled), so it
// refuses the shape itself while the write is gated. A hand-made "notes~1.md" is
// refused by this rule too; that over-refusal is the documented cost of closing a
// vector that is silently open on every 8.3-enabled volume. The five-digit cap
// keeps human date-like names ("report~20261024") writable while covering every
// tail Windows' own 8.3 generator produces.
//
// FIX-D1a (R3-3): the caller applies this to EVERY component, not just the leaf,
// because an alias in an intermediate position is created through by MkdirAll.
func win32ShortAliasShape(comp string) bool {
	base := canonicalWriteComponent(comp)
	end := len(base)
	for {
		if aliasTailDigits(base[:end]) {
			return true
		}
		i := strings.LastIndexByte(base[:end], '.')
		if i <= 0 {
			return false
		}
		end = i
	}
}

// aliasTailDigits reports whether s ends with "~" followed by one to five
// digits, which is the generated-alias tail.
func aliasTailDigits(s string) bool {
	i := strings.LastIndexByte(s, '~')
	if i <= 0 || i == len(s)-1 {
		return false
	}
	digits := s[i+1:]
	if len(digits) > 5 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
