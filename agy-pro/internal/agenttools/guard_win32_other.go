//go:build !windows

package agenttools

// Non-Windows stubs for the FIX-A1 (S2) and FIX-D1a (R3-2 / R3-3) Win32 arms. A
// POSIX host has no alternate data streams, no 8.3 short names and never strips a
// trailing dot or space from a component, so there is nothing to expand, no alias
// shape to refuse and no stream syntax to forbid. The functions must still exist
// because guard.go (which is not build-tagged) calls them unconditionally; the
// canonical trailing-trim and reserved-device-name rules in guard.go stay active
// on every host, and the FOLD (win32NameFold) stays active too, so the guard's
// verdict never forks by platform except for the two host-specific shape arms.

// win32StreamSyntaxRule is off here: a POSIX filename may legally contain ':',
// so refusing it would over-refuse ordinary names ("notes:2026.md").
const win32StreamSyntaxRule = false

// win32ExpandWriteTarget is the non-Windows stub: no expansion is available.
func win32ExpandWriteTarget(string) (string, bool) { return "", false }

// win32ShortAliasShape is the non-Windows stub: "NAME~1.EXT" is an ordinary
// filename on POSIX and must stay writable.
func win32ShortAliasShape(string) bool { return false }
