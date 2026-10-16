package agysettings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// absolutePathLabel is what an absolute path collapses to in an error message.
const absolutePathLabel = "<absolute path>"

// Absolute-path scrubbers, used only for error shapes this package does not
// control (the last-resort fallback in sanitizeOSError).
//
// The Windows alternative requires a token boundary before the drive letter: a
// bare [a-z]:[\\/] also matches the "i:/" inside a gemini-api:// URL (and the
// "e:" of blade:), which would eat the offending model key out of a collision
// diagnostic. A drive letter never follows a letter, a digit, or a hyphen, so
// excluding those three is enough. The boundary character is captured and
// restored through ${1} so the sentence around the path survives. The trailing
// class stops at whitespace, quotes, the colon and sentence punctuation, so a
// path does not swallow the prose that follows it.
var (
	absoluteWindowsPathPattern = regexp.MustCompile(`(?i)(?:^|([^a-zA-Z0-9-]))[a-z]:[\\/][^\s"':,;)]*`)
	absolutePosixPathPattern   = regexp.MustCompile(`(^|[\s"'(=\[])(/[^\s"':,;)]*)`)
)

// maskedPath renders a resolved path for an error message.
//
// The directory layout is always dropped, so no user path can reach stderr or an
// artifact. The base name survives only when it is one of the names this package
// itself produces (settings.json and its .lock / .bak siblings), because that is
// the diagnostic the operator needs and the name identifies nothing about the
// user. Anything else collapses to classLabel, since a caller-supplied base name
// can be identifying on its own ("alice-secrets.json").
//
// providers.MaskOperand is deliberately NOT used here: it exempts locations by
// design (keyaudit.go: "MaskOperand alone exempts locations so a spawn error can
// name the binary"), so it would leave C:\Users\...\settings.json.lock raw.
func maskedPath(p, classLabel string) string {
	base := filepath.Base(filepath.Clean(p))
	switch {
	case base == settingsFileName, base == settingsFileName+".lock":
		return base
	case strings.HasPrefix(base, settingsFileName+".bak."):
		return base
	}
	return classLabel
}

// maskOperand renders a caller-supplied value for an error message.
//
// A value that is not an absolute path is echoed verbatim: it is the caller's own
// input and the operator needs it to fix the call. An absolute path collapses to
// absolutePathLabel, because it can carry the user's directory layout. Model keys
// and URLs are never absolute paths (no volume name, no leading separator), so
// they stay readable.
func maskOperand(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return `""`
	}
	if isAbsolutePath(trimmed) {
		return absolutePathLabel
	}
	return s
}

// isAbsolutePath reports whether s spells an absolute path. It accepts every
// spelling on every platform, not just the host's: a Windows volume path
// ("C:\x", "C:x", "\\srv\share") and a rooted path ("/etc"), because the value
// may have been produced elsewhere and still names a local location.
func isAbsolutePath(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	if filepath.IsAbs(trimmed) || filepath.VolumeName(trimmed) != "" {
		return true
	}
	return strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`)
}

// osError is an OS error rendered without its path. The original stays in the
// chain, so errors.Is/As still see the syscall errno.
type osError struct {
	msg string
	err error
}

func (e *osError) Error() string { return e.msg }
func (e *osError) Unwrap() error { return e.err }

// wrapOSError renders err for an error message with every path replaced by
// classLabel, keeping the chain unwrappable.
//
// Masking only this package's own format strings is not enough: the os package's
// own error types embed the raw path in their message ("open C:\Users\...\
// settings.json.lock: Access is denied"), so a %w on the raw error would leak the
// path even though the surrounding text is masked.
func wrapOSError(err error, classLabel string) error {
	if err == nil {
		return nil
	}
	return &osError{msg: sanitizeOSError(err, classLabel), err: err}
}

// sanitizeOSError renders err without any path.
func sanitizeOSError(err error, classLabel string) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return fmt.Sprintf("%s %s: %v", pathErr.Op, classLabel, pathErr.Err)
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return fmt.Sprintf("%s %s: %v", linkErr.Op, classLabel, linkErr.Err)
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		return fmt.Sprintf("%s: %v", syscallErr.Syscall, syscallErr.Err)
	}
	return scrubAbsolutePaths(err.Error())
}

// scrubAbsolutePaths is the last resort for an error shape this package does not
// control: any absolute path still present in the text is replaced. The leading
// boundary character of each match is preserved through ${1}.
func scrubAbsolutePaths(s string) string {
	s = absoluteWindowsPathPattern.ReplaceAllString(s, "${1}"+absolutePathLabel)
	return absolutePosixPathPattern.ReplaceAllString(s, "${1}"+absolutePathLabel)
}

// scrubbedError is an error whose message was rewritten by scrubErrorPaths. The
// original stays in the chain.
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// scrubErrorPaths is the last line of defense behind the rule that no error
// leaving this package may carry a raw absolute path.
//
// Every individual site is masked, and every OS error is unwrapped into a
// path-free message, but a wrapped error shape nobody anticipated could still
// smuggle a path through. This runs at the public entry points, so the rule
// holds by construction rather than by site-by-site review. It returns the
// original error untouched when there is nothing to scrub, which is the common
// case.
func scrubErrorPaths(err error) error {
	if err == nil {
		return nil
	}
	msg := scrubAbsolutePaths(err.Error())
	if msg == err.Error() {
		return err
	}
	return &scrubbedError{msg: msg, err: err}
}
