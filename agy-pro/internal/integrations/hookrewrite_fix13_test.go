package integrations

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The O-1 fix (SESSION9 FIX-13): the SHIPPED portable source carries the hook flag
// inline in the command VALUE, because the Antigravity host documents a hook handler
// as type/command/timeout and runs the command through a shell (`cmd /c` / `sh -c`).
// A separate "args" array is not a handler field, so a value of "agy-tokens" plus
// args ["--hook"] ran bin/agy-tokens.js WITHOUT --hook on every install: report mode
// instead of badge mode, a silently missing PostInvocation badge.
//
// These two pins read the repo's own hooks/hooks.json, not a fixture, so the file
// that ships is the file under test. A standalone agy-pro checkout carries no sibling
// hooks/ tree, so both pins skip there (the same shape the sync gate uses).

// shippedHooksSourcePath is the portable source, located from THIS test file's own
// directory (agy-pro/internal/integrations -> repo root hooks/hooks.json) rather than
// from the process working directory, so the pin reads the same bytes wherever the
// package is run from.
func shippedHooksSourcePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not name this test file, so the shipped source cannot be located")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "hooks", hookSourceFile)
}

// readShippedHooksSource returns the shipped source bytes, skipping when the tree is
// absent instead of failing the package on a standalone checkout.
func readShippedHooksSource(t *testing.T) []byte {
	t.Helper()
	path := shippedHooksSourcePath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("the repo hooks source is not in this checkout (%v): a standalone agy-pro tree ships no sibling hooks/ tree", err)
	}
	return data
}

// TestShippedHooksSourceCarriesTheInlineHookFlag is the shipped-file half of O-1:
// the PostInvocation command value spells "agy-tokens --hook" itself and the handler
// carries no "args" key, so the host's shell execution passes the flag whether or not
// it honours a field it never documented.
func TestShippedHooksSourceCarriesTheInlineHookFlag(t *testing.T) {
	data := readShippedHooksSource(t)

	var doc struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode %s: %v", shippedHooksSourcePath(t), err)
	}
	if len(doc.Hooks) == 0 {
		t.Fatalf("%s registers no hooks", shippedHooksSourcePath(t))
	}
	handler, ok := doc.Hooks["PostInvocation"]
	if !ok {
		t.Fatalf("%s has no PostInvocation handler (handlers: %v)", shippedHooksSourcePath(t), hookKeys(doc.Hooks))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(handler, &fields); err != nil {
		t.Fatalf("decode the PostInvocation handler: %v", err)
	}

	want := hookPortableCommand + " --hook"
	var command string
	if err := json.Unmarshal(fields[hookCommandKey], &command); err != nil {
		t.Fatalf("the handler's %q field is not a string: %v", hookCommandKey, err)
	}
	if command != want {
		t.Fatalf("shipped command = %q, want %q (the flag must be in the value the host runs)", command, want)
	}
	if _, carried := fields["args"]; carried {
		t.Fatalf("the shipped handler still carries an \"args\" key; the host documents only type, command and timeout, so the flag belongs in the command value")
	}
	if bytes.Contains(data, []byte(`"args"`)) {
		t.Fatalf("%s still mentions an \"args\" key somewhere:\n%s", shippedHooksSourcePath(t), string(data))
	}
	// The portable name must be a whole word: hookPortableTail accepts the value only
	// when the flag is separated by a space, which is what makes the rewrite below see
	// the tail it keeps.
	if tail, portable := hookPortableTail(command); !portable || tail != " --hook" {
		t.Fatalf("hookPortableTail(%q) = (%q, %v), want (%q, true)", command, tail, portable, " --hook")
	}
}

// hookKeys lists a hook map's event names for a failure message.
func hookKeys(hooks map[string]json.RawMessage) []string {
	names := make([]string, 0, len(hooks))
	for name := range hooks {
		names = append(names, name)
	}
	return names
}

// TestShippedHooksSourceSplicesTheFlagOutsideTheQuotes is the quote-boundary proof for
// the shipped source: the deployed bytes are the source bytes with ONE JSON string
// literal replaced, the value is quoteLauncher(path) followed by the source's own
// argument text, and the tail lands OUTSIDE the quotes.
//
// The wrong shape this pins against is `"C:\x\agy-tokens.bat --hook"`: under `cmd /c`
// that is one giant argv[0], so the host would look for a file whose name contains the
// flag. The right shape is `"C:\x\agy-tokens.bat" --hook` (or the unquoted path when it
// needs no quoting).
func TestShippedHooksSourceSplicesTheFlagOutsideTheQuotes(t *testing.T) {
	source := readShippedHooksSource(t)

	cases := []struct {
		name     string
		goos     string
		launcher string
		quoted   bool
	}{
		{
			name:     "windows plain path",
			goos:     "windows",
			launcher: `C:\Users\k1yt\.gemini\antigravity-cli\bin\agy-tokens.bat`,
			quoted:   false,
		},
		{
			name:     "windows spaced path",
			goos:     "windows",
			launcher: `C:\Users\a b\.gemini\antigravity-cli\bin\agy-tokens.bat`,
			quoted:   true,
		},
		{
			name:     "posix plain path",
			goos:     "linux",
			launcher: "/home/a/.local/bin/agy-tokens",
			quoted:   false,
		},
		{
			name:     "posix spaced path",
			goos:     "darwin",
			launcher: "/home/a b/.local/bin/agy-tokens",
			quoted:   true,
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			pinRuntimeGOOS(t, testCase.goos)

			spans, err := hookCommandSpans(source)
			if err != nil {
				t.Fatalf("hookCommandSpans: %v", err)
			}
			if len(spans) == 0 {
				t.Fatal("the shipped source has no command value; this pin would be vacuous")
			}
			rewritten, posture := hookCommandRewriteTo(source, spans, testCase.launcher)
			if posture.Posture != HookCommandAbsolute || posture.Sites != len(spans) {
				t.Fatalf("posture = %+v, want %d absolute site(s)", posture, len(spans))
			}

			quotedPath := quoteLauncher(testCase.launcher)
			wantValue := quotedPath + " --hook"
			if posture.Value != wantValue {
				t.Fatalf("deployed value = %q, want %q", posture.Value, wantValue)
			}
			if got := (quotedPath != testCase.launcher); got != testCase.quoted {
				t.Fatalf("quoteLauncher(%q) = %q; the case expected quoted=%v", testCase.launcher, quotedPath, testCase.quoted)
			}

			// The quote closes before the tail: the value starts with the executable
			// token and the argument text follows a separator, never inside the quotes.
			if !strings.HasSuffix(wantValue, " --hook") {
				t.Fatalf("deployed value %q does not end with the source tail", wantValue)
			}
			head := strings.TrimSuffix(wantValue, " --hook")
			if head != quotedPath {
				t.Fatalf("the executable token = %q, want exactly the quoted launcher %q (the tail must sit outside the quotes)", head, quotedPath)
			}
			if unquoted := shellUnquote(head); unquoted != testCase.launcher {
				t.Fatalf("unquoting the executable token gave %q, want the launcher %q", unquoted, testCase.launcher)
			}
			// The WRONG shape, spelled out so a future edit cannot drift into it.
			if wrong := quoteLauncher(testCase.launcher + " --hook"); wantValue == wrong && quotedPath != testCase.launcher {
				t.Fatalf("the deployed value is the whole-line quoted form %q; the host would run it as one argv[0]", wrong)
			}

			// Byte proof: the deployed file is the source with only that one literal
			// replaced, so indentation, key order, line endings and the trailing
			// newline are untouched.
			sourceLiteral := hookJSONLiteral(t, spans[0].Value)
			deployedLiteral := hookJSONLiteral(t, wantValue)
			want := strings.Replace(string(source), sourceLiteral, deployedLiteral, 1)
			if want == string(source) {
				t.Fatalf("the substitution did not fire; the shipped source no longer spells %s", sourceLiteral)
			}
			if string(rewritten) != want {
				t.Fatalf("deployed bytes =\n%q\nwant the source with only the command literal replaced:\n%q", string(rewritten), want)
			}
			if got, wantCount := strings.Count(string(rewritten), "\r\n"), strings.Count(string(source), "\r\n"); got != wantCount {
				t.Fatalf("deployed file has %d CRLF line breaks, want the source's %d", got, wantCount)
			}
			if !bytes.HasSuffix(rewritten, []byte("}\r\n")) {
				t.Fatalf("deployed file = %q, want its trailing CRLF preserved", string(rewritten))
			}
			if !bytes.Contains(rewritten, []byte(deployedLiteral)) {
				t.Fatalf("the deployed bytes do not carry %s", deployedLiteral)
			}
			// The byte proof a reader can check: the executable token ends with the
			// closing quote (or the path's last byte) and is followed by 0x20 0x2d 0x2d.
			index := strings.Index(wantValue, " --hook")
			if index < 0 || wantValue[index] != ' ' || wantValue[index+1] != '-' || wantValue[index+2] != '-' {
				t.Fatalf("the separator before the flag in %q is not SPACE '-' '-'", wantValue)
			}
			t.Logf("%s: command line bytes = % x", testCase.name, []byte(wantValue))
			t.Logf("%s: deployed command literal = %s", testCase.name, deployedLiteral)
		})
	}
}
