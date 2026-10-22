package dashboard

// Browser open (P5-2 S5).
//
// The Open row is the ONLY path in the Go client that can start a browser, and
// it is deliberately narrower than "open this URL": the target must be the
// loopback dashboard URL, matched against an explicit whitelist. Anything else
// (a file:// path, an external host, a lookalike loopback spelling, a scheme
// with a path traversal, a URL with credentials, query or fragment) is refused
// before a process is even built, so no argv this package produces can carry
// untrusted text into an OS command line.
//
// The opener itself is a seam: launchCommand is a pure builder (it constructs
// the per-OS command without running it) and runLauncher is the exec edge, so
// tests assert the exact argv and never start a browser.

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// RefusedURLError is the typed whitelist refusal. It deliberately does NOT echo
// the rejected URL: the whole point is that the value is untrusted.
type RefusedURLError struct {
	Reason string
}

func (e *RefusedURLError) Error() string {
	return "refused browser target: " + e.Reason
}

// LoopbackURL builds the only URL shape this package hands to an OS opener
// (http://127.0.0.1:<port>/). It returns "" for a port ValidPort rejects, so no
// caller can construct an openable target from a bad record.
func LoopbackURL(port int) string {
	if !ValidPort(port) {
		return ""
	}
	return "http://" + loopbackHost + ":" + strconv.Itoa(port) + "/"
}

// urlPathChars is the complete set of characters a path may carry past the
// authority. It is a whitelist, not a blacklist: no cmd.exe metacharacter, no
// quote, no percent-escape, no backslash, no '@', '?' or '#', so the target can
// never be re-parsed as a second command or a different host.
const urlPathChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-/"

// validateLoopbackURL enforces the whitelist:
//
//	http://127.0.0.1:<port>      exact scheme + host, no userinfo, no port range leak
//	http://127.0.0.1:<port>/<p>  optional path over urlPathChars only
//
// The scheme and host are matched case-sensitively on purpose: LoopbackURL is
// the only producer, it emits lowercase, and accepting other spellings widens
// the surface for no gain.
func validateLoopbackURL(raw string) (string, error) {
	const prefix = "http://" + loopbackHost + ":"
	if !strings.HasPrefix(raw, prefix) {
		return "", &RefusedURLError{Reason: "only http://127.0.0.1:<port> targets are allowed"}
	}
	rest := raw[len(prefix):]
	digits := rest
	tail := ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		digits, tail = rest[:i], rest[i:]
	}
	if digits == "" || len(digits) > 5 {
		return "", &RefusedURLError{Reason: "the target carries no usable port"}
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", &RefusedURLError{Reason: "the target port is not a plain integer"}
		}
	}
	port, err := strconv.Atoi(digits)
	if err != nil || !ValidPort(port) {
		return "", &PortError{Port: port}
	}
	for i := 0; i < len(tail); i++ {
		if !strings.ContainsRune(urlPathChars, rune(tail[i])) {
			return "", &RefusedURLError{Reason: "the target path carries a character this opener will not pass through"}
		}
	}
	if strings.Contains(tail, "..") {
		return "", &RefusedURLError{Reason: "the target path carries a parent traversal"}
	}
	return raw, nil
}

// launchCommand builds the OS opener command for target. It is pure: nothing is
// executed here, which is what lets the tests assert the per-OS argv without a
// browser ever starting.
func launchCommand(goos, target string) (*exec.Cmd, error) {
	switch goos {
	case "windows":
		// `start` is a cmd built-in. The empty argument is its window-title
		// slot, so the URL is always consumed as the target and never as a
		// candidate title (the classic `start "url"` footgun).
		return exec.Command("cmd.exe", "/c", "start", "", target), nil
	case "darwin":
		return exec.Command("open", target), nil
	case "linux":
		return exec.Command("xdg-open", target), nil
	default:
		return nil, fmt.Errorf("no browser opener is known on %s", goos)
	}
}

// runLauncher is the exec edge. Tests replace it to capture the command and
// return without launching anything.
var runLauncher = func(cmd *exec.Cmd) error { return cmd.Run() }

// OpenInBrowser hands the loopback dashboard URL to the OS default browser.
// The URL is validated against the whitelist first; a refused target never
// reaches a process. The launcher is waited for because every supported opener
// (`cmd /c start`, `open`, `xdg-open`) returns once the browser is told to open.
func OpenInBrowser(url string) error {
	target, err := validateLoopbackURL(url)
	if err != nil {
		return err
	}
	cmd, err := launchCommand(runtime.GOOS, target)
	if err != nil {
		return err
	}
	if err := runLauncher(cmd); err != nil {
		return fmt.Errorf("browser launch failed: %w", err)
	}
	return nil
}
