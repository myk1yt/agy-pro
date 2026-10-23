package integrations

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"agypro/profiles"
)

// hookPortableFixtureJSON is the PRE-FIX repo hooks/hooks.json shape - command and
// args as separate fields, so the command value is the bare launcher name alone - kept
// as the no-argument arm of the rewrite. The shipped file no longer looks like this:
// its command value spells "agy-tokens --hook" inline, because the host documents no
// "args" handler field (O-1, FIX-13), and that shape is pinned against the real file by
// hookrewrite_fix13_test.go. The fixture also carries the two decoys the rewrite must
// never touch:
//
//   - a description that spells the launcher and its arguments (a string that
//     LOOKS like a command line but is not a command value);
//   - a second hook whose command is somebody else's tool.
//
// The line endings are CRLF and the file ends with a newline on purpose: the
// rewrite splices one JSON string literal, so the file's byte shape has to
// survive it untouched.
var hookPortableFixtureJSON = []byte("{\r\n" +
	"  \"$schema\": \"https://example.invalid/hooks.schema.json\",\r\n" +
	"  \"name\": \"antigravity-lifecycle-hooks\",\r\n" +
	"  \"description\": \"runs agy-tokens after every turn\",\r\n" +
	"  \"hooks\": {\r\n" +
	"    \"PostInvocation\": {\r\n" +
	"      \"enabled\": true,\r\n" +
	"      \"command\": \"agy-tokens\",\r\n" +
	"      \"args\": [\"--hook\"],\r\n" +
	"      \"description\": \"agy-tokens --hook\"\r\n" +
	"    },\r\n" +
	"    \"SessionStart\": {\r\n" +
	"      \"command\": \"other-tool --flag\",\r\n" +
	"      \"args\": []\r\n" +
	"    }\r\n" +
	"  }\r\n" +
	"}\r\n")

// hookPortableFixtureContent is hookPortableFixtureJSON as a deploy source.
func hookPortableFixtureContent() fs.FS {
	return fstest.MapFS{"hooks/" + hookSourceFile: {Data: hookPortableFixtureJSON}}
}

// hookLauncherSandbox points the home at a fresh temp directory, pins the launcher
// ladder to goos, and creates the launcher the ladder looks for. It returns the
// sandbox home and the absolute launcher path. Nothing here names a real machine
// path: both come from the sandbox.
func hookLauncherSandbox(t *testing.T, goos string) (home, launcher string) {
	t.Helper()
	home = sandboxHome(t)
	pinRuntimeGOOS(t, goos)
	launcher = filepath.Join(launcherDirsFor(goos, home)[0], statusLineLauncherNames(goos)[0])
	writeFileRaw(t, launcher, []byte("#!/bin/sh\n"))
	return home, launcher
}

// writeSandboxLauncher creates the launcher of goos inside a deploy sandbox's own
// home, so a plan built by that sandbox resolves it. It returns the launcher path.
func writeSandboxLauncher(t *testing.T, sandbox *deploySandbox, goos string) string {
	t.Helper()
	pinRuntimeGOOS(t, goos)
	launcher := filepath.Join(launcherDirsFor(goos, sandbox.home)[0], statusLineLauncherNames(goos)[0])
	writeFileRaw(t, launcher, []byte("#!/bin/sh\n"))
	return launcher
}

// pinRuntimeGOOS sets the package's OS seam for one test and restores it after.
func pinRuntimeGOOS(t *testing.T, goos string) {
	t.Helper()
	previous := runtimeGOOS
	runtimeGOOS = goos
	t.Cleanup(func() { runtimeGOOS = previous })
}

// hookJSONLiteral renders a command value as the JSON string literal the deployed
// file carries, so an expectation can be built the same way the engine builds it.
func hookJSONLiteral(t *testing.T, value string) string {
	t.Helper()
	literal, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %q as a JSON literal: %v", value, err)
	}
	return string(literal)
}

// hookDoc decodes a deployed document for the semantic half of the assertions.
func hookDoc(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode the hook document: %v\n%s", err, string(data))
	}
	return doc
}

// hookCommandAt reads hooks.<outer>.command out of a decoded document. The hook
// schema accepts both an object and an array of objects there, and the fixtures
// use both, so the helper walks whichever shape the document carries.
func hookCommandAt(t *testing.T, doc map[string]any, outer string) string {
	t.Helper()
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("the document carries no hooks object: %v", doc)
	}
	switch entry := hooks[outer].(type) {
	case map[string]any:
		command, _ := entry["command"].(string)
		return command
	case []any:
		if len(entry) == 0 {
			t.Fatalf("hooks.%s is an empty array", outer)
		}
		first, ok := entry[0].(map[string]any)
		if !ok {
			t.Fatalf("hooks.%s[0] is not an object: %v", outer, entry[0])
		}
		command, _ := first["command"].(string)
		return command
	default:
		t.Fatalf("hooks.%s is neither an object nor an array: %v", outer, hooks[outer])
		return ""
	}
}

// shellUnquote strips one layer of shell quoting from a command value, so a test
// can compare the path the host will execute against the path the ladder resolved
// without re-implementing either OS's quoting rules.
func shellUnquote(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return strings.ReplaceAll(value[1:len(value)-1], `'\''`, "'")
		}
	}
	return value
}

// TestHookCommandRewriteTouchesOnlyTheCommandLiteral is requirement 1: the
// deployed file is the source file with one JSON string value replaced, byte for
// byte everywhere else. The expectation is built by substituting the same literal
// in the source text, so an indentation, key-order, line-ending or trailing-newline
// change would fail here.
func TestHookCommandRewriteTouchesOnlyTheCommandLiteral(t *testing.T) {
	_, launcher := hookLauncherSandbox(t, "windows")

	data, posture := hookCommandRewriteFor(hookPortableFixtureJSON)

	if posture == nil || posture.Posture != HookCommandAbsolute {
		t.Fatalf("posture = %+v, want an absolute rewrite", posture)
	}
	if posture.Launcher != launcher {
		t.Fatalf("posture launcher = %q, want %q", posture.Launcher, launcher)
	}
	want := strings.Replace(string(hookPortableFixtureJSON), `"command": "agy-tokens"`,
		`"command": `+hookJSONLiteral(t, quoteLauncher(launcher)), 1)
	if want == string(hookPortableFixtureJSON) {
		t.Fatal("the fixture no longer carries the portable command shape this test substitutes")
	}
	if string(data) != want {
		t.Fatalf("deployed bytes =\n%q\nwant the source with only the command literal replaced:\n%q", string(data), want)
	}

	// The splice is the byte proof; these are the semantic ones.
	if got, want := strings.Count(string(data), "\r\n"), strings.Count(string(hookPortableFixtureJSON), "\r\n"); got != want {
		t.Fatalf("deployed file has %d CRLF line breaks, want %d (line endings changed)", got, want)
	}
	if !strings.HasSuffix(string(data), "}\r\n") {
		t.Fatalf("deployed file = %q, want its trailing newline preserved", string(data))
	}
	doc := hookDoc(t, data)
	if got := hookCommandAt(t, doc, "PostInvocation"); got != quoteLauncher(launcher) {
		t.Fatalf("deployed PostInvocation command = %q, want %q", got, quoteLauncher(launcher))
	}
	if got := hookCommandAt(t, doc, "SessionStart"); got != "other-tool --flag" {
		t.Fatalf("the second command value changed to %q; the rewrite must touch only the portable launcher", got)
	}
	if got := doc["description"]; got != "runs agy-tokens after every turn" {
		t.Fatalf("a command-shaped string outside a command key changed: %v", got)
	}
	if got := doc["hooks"].(map[string]any)["PostInvocation"].(map[string]any)["description"]; got != "agy-tokens --hook" {
		t.Fatalf("a command-shaped description changed: %v", got)
	}
	if got := doc["hooks"].(map[string]any)["PostInvocation"].(map[string]any)["args"]; !reflect.DeepEqual(got, []any{"--hook"}) {
		t.Fatalf("the args array changed: %v", got)
	}
}

// TestHookCommandRewriteKeepsTheSourceArguments pins the second portable shape: a
// file whose command carries its arguments inline keeps them.
func TestHookCommandRewriteKeepsTheSourceArguments(t *testing.T) {
	_, launcher := hookLauncherSandbox(t, "linux")
	source := []byte(`{"hooks":{"PostInvocation":[{"command":"agy-tokens --hook --raw"}]}}`)

	data, posture := hookCommandRewriteFor(source)
	if posture.Posture != HookCommandAbsolute {
		t.Fatalf("posture = %+v, want an absolute rewrite", posture)
	}
	want := strings.Replace(string(source), `"agy-tokens --hook --raw"`,
		hookJSONLiteral(t, quoteLauncher(launcher)+" --hook --raw"), 1)
	if string(data) != want {
		t.Fatalf("deployed bytes = %q, want %q", string(data), want)
	}
	if got := hookCommandAt(t, hookDoc(t, data), "PostInvocation"); got != quoteLauncher(launcher)+" --hook --raw" {
		t.Fatalf("deployed command = %q, want the launcher followed by the source arguments", got)
	}
}

// TestHookCommandRewriteIsIdempotent pins that feeding a rewritten file back into
// the rewrite changes nothing: the command is no longer the portable name, so a
// second pass is a no-op rather than a second prefix.
func TestHookCommandRewriteIsIdempotent(t *testing.T) {
	hookLauncherSandbox(t, "windows")

	first, posture := hookCommandRewriteFor(hookPortableFixtureJSON)
	if posture.Posture != HookCommandAbsolute {
		t.Fatalf("first posture = %+v, want an absolute rewrite", posture)
	}
	second, again := hookCommandRewriteFor(first)
	if string(second) != string(first) {
		t.Fatalf("the second pass changed the bytes:\n%q\n->\n%q", string(first), string(second))
	}
	if again.Posture != HookCommandUnchanged || again.Rewritten() {
		t.Fatalf("second posture = %+v, want unchanged", again)
	}
}

// TestHookCommandRewriteDeclinesWhatItCannotPin covers the shapes that must deploy
// byte-identically with the reason recorded: an unreadable document, a document
// with no command, a command that belongs to another tool, and a name that only
// starts like the portable one.
func TestHookCommandRewriteDeclinesWhatItCannotPin(t *testing.T) {
	hookLauncherSandbox(t, "windows")

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "invalid json", input: "not json at all", want: "could not be read as JSON"},
		{name: "no command", input: `{"hooks":{"PostInvocation":{"enabled":true}}}`, want: "not the portable"},
		{name: "foreign tool", input: `{"hooks":{"PostInvocation":{"command":"other-tool"}}}`, want: "not the portable"},
		{name: "prefix only", input: `{"hooks":{"PostInvocation":{"command":"agy-tokens-extra"}}}`, want: "not the portable"},
		{name: "already absolute", input: `{"hooks":{"PostInvocation":{"command":"/usr/local/bin/agy-tokens"}}}`, want: "not the portable"},
		{name: "command as a value", input: `{"type":"command","hooks":{}}`, want: "not the portable"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			data, posture := hookCommandRewriteFor([]byte(testCase.input))
			if string(data) != testCase.input {
				t.Fatalf("deployed bytes = %q, want the source bytes unchanged", string(data))
			}
			if posture.Posture != HookCommandUnchanged || posture.Rewritten() {
				t.Fatalf("posture = %+v, want unchanged", posture)
			}
			if !strings.Contains(posture.Detail, testCase.want) {
				t.Fatalf("posture detail = %q, want it to contain %q", posture.Detail, testCase.want)
			}
			if !strings.Contains(posture.PostureText(), testCase.want) {
				t.Fatalf("posture text = %q, want it to name the reason", posture.PostureText())
			}
		})
	}

	// A nil posture renders nothing, so a renderer never prints an empty row.
	var none *HookCommandRewrite
	if none.PostureText() != "" || none.Rewritten() {
		t.Fatal("a nil posture rendered a row or claimed a rewrite")
	}
}

// TestHookRewriteVerificationRejectsATamperedSplice proves the rewrite is checked,
// not trusted: the verifier refuses bytes that changed outside the command value,
// and refuses a change to a command value that was not the portable launcher.
func TestHookRewriteVerificationRejectsATamperedSplice(t *testing.T) {
	_, launcher := hookLauncherSandbox(t, "windows")

	source := hookPortableFixtureJSON
	spans, err := hookCommandSpans(source)
	if err != nil {
		t.Fatalf("hookCommandSpans: %v", err)
	}
	edits := make([]hookCommandEdit, 0, 1)
	for _, span := range spans {
		value, ok := hookCommandValue(span.Value, launcher)
		if !ok {
			continue
		}
		literal, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("encode %q: %v", value, marshalErr)
		}
		edits = append(edits, hookCommandEdit{span: span, value: value, literal: literal})
	}
	rewritten := spliceHookCommands(source, edits)
	if err := verifyHookCommandRewrite(source, rewritten, spans, edits, launcher); err != nil {
		t.Fatalf("the honest splice did not verify: %v", err)
	}

	tampered := []byte(strings.Replace(string(rewritten), `"enabled": true`, `"enabled":  true`, 1))
	if err := verifyHookCommandRewrite(source, tampered, spans, edits, launcher); err == nil {
		t.Fatal("the verifier accepted a file whose whitespace outside the command value changed")
	}

	tampered = []byte(strings.Replace(string(rewritten), `"other-tool --flag"`, `"other-tool --flaggy"`, 1))
	if err := verifyHookCommandRewrite(source, tampered, spans, edits, launcher); err == nil {
		t.Fatal("the verifier accepted a change to a command value it did not rewrite")
	}

	tampered = []byte(strings.Replace(string(rewritten), `"agy-tokens --hook"`, `"agy-tokens --hooked"`, 1))
	if err := verifyHookCommandRewrite(source, tampered, spans, edits, launcher); err == nil {
		t.Fatal("the verifier accepted a change to a value that only looks like a command line")
	}
}

// TestHookCommandSpansStructural pins the scanner's structural reading: only the
// VALUE of a "command" key is a span, so a string that spells the word, a key
// spelled inside an escaped string, and a nested array of hook objects all
// classify the way the schema says they should.
func TestHookCommandSpansStructural(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "top level",
			input: `{"command":"a"}`,
			want:  []string{"a"},
		},
		{
			name:  "array of hook objects",
			input: `{"hooks":{"PostInvocation":[{"command":"a"},{"command":"b"}]}}`,
			want:  []string{"a", "b"},
		},
		{
			name:  "command as a value is not a key",
			input: `{"type":"command","other":{"command":"a"}}`,
			want:  []string{"a"},
		},
		{
			name:  "escaped quotes never form a key",
			input: `{"description":"a \"command\": \"b\"","hooks":{"PostInvocation":{"command":"c"}}}`,
			want:  []string{"c"},
		},
		{
			name:  "escaped value round-trips",
			input: `{"command":"d\\e\"f"}`,
			want:  []string{`d\e"f`},
		},
		{
			name:  "non-string command is not a span",
			input: `{"command":["--hook"]}`,
			want:  nil,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			spans, err := hookCommandSpans([]byte(testCase.input))
			if err != nil {
				t.Fatalf("hookCommandSpans: %v", err)
			}
			got := make([]string, 0, len(spans))
			for _, span := range spans {
				got = append(got, span.Value)
				if span.Start >= span.End || span.End > len(testCase.input) {
					t.Fatalf("span %+v is outside the document", span)
				}
			}
			if len(got) != len(testCase.want) || (len(got) > 0 && !reflect.DeepEqual(got, testCase.want)) {
				t.Fatalf("spans = %v, want %v", got, testCase.want)
			}
		})
	}

	if _, err := hookCommandSpans([]byte(`{"command":`)); err == nil {
		t.Fatal("an invalid document produced spans")
	}
	deep := strings.Repeat("[", hookJSONMaxDepth+2) + strings.Repeat("]", hookJSONMaxDepth+2)
	if _, err := hookCommandSpans([]byte(deep)); err == nil {
		t.Fatal("a document nested past the depth bound produced spans")
	}
}

// TestHookCommandRewriteCrossOSPathShapes pins requirement 6: the rewrite value is
// computed per runtime GOOS by the door's own resolver and quoting, on both path
// shapes, with no machine path baked into the test.
//
// The ladder half is what this test can prove about a real deploy (which directory
// and which file name each OS uses). The quoting half is proved directly on
// hookCommandValue below, because a Windows host cannot produce a POSIX-shaped
// absolute path for the ladder to resolve.
func TestHookCommandRewriteCrossOSPathShapes(t *testing.T) {
	cases := []struct {
		name     string
		goos     string
		wantPath func(home string) string
	}{
		{
			name: "windows bat ladder",
			goos: "windows",
			wantPath: func(home string) string {
				return filepath.Join(home, ".gemini", "antigravity-cli", "bin", "agy-tokens.bat")
			},
		},
		{
			name: "posix local-bin ladder",
			goos: "linux",
			wantPath: func(home string) string {
				return filepath.Join(home, ".local", "bin", "agy-tokens")
			},
		},
		{
			name: "posix antigravity-bin fallback",
			goos: "darwin",
			wantPath: func(home string) string {
				return filepath.Join(home, ".gemini", "antigravity-cli", "bin", "agy-tokens")
			},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			home := sandboxHome(t)
			pinRuntimeGOOS(t, testCase.goos)
			launcher := testCase.wantPath(home)
			writeFileRaw(t, launcher, []byte("#!/bin/sh\n"))

			data, posture := hookCommandRewriteFor(hookPortableFixtureJSON)
			if posture.Posture != HookCommandAbsolute {
				t.Fatalf("posture = %+v, want an absolute rewrite", posture)
			}
			if posture.Launcher != launcher {
				t.Fatalf("resolved launcher = %q, want %q", posture.Launcher, launcher)
			}
			wantValue := quoteLauncher(launcher)
			if posture.Value != wantValue {
				t.Fatalf("posture value = %q, want the door's own quoting %q", posture.Value, wantValue)
			}
			if got := hookCommandAt(t, hookDoc(t, data), "PostInvocation"); got != wantValue {
				t.Fatalf("deployed command = %q, want %q", got, wantValue)
			}
			// The executable path survives the shell spelling the value carries.
			if got := shellUnquote(wantValue); got != launcher {
				t.Fatalf("unquoting the deployed value gave %q, want the resolved launcher %q", got, launcher)
			}
			if !strings.Contains(string(data), hookJSONLiteral(t, wantValue)) {
				t.Fatalf("the deployed bytes do not carry %q", hookJSONLiteral(t, wantValue))
			}
			if !strings.Contains(posture.PostureText(), "pinned to the absolute launcher") {
				t.Fatalf("posture text = %q, want it to name the pin", posture.PostureText())
			}
		})
	}
}

// TestHookCommandValueCrossOSShapes is the quoting half of requirement 6, on both
// path shapes. It drives hookCommandValue directly with a path of each OS's shape,
// because the point is the per-GOOS quoting the value carries, not the ladder.
func TestHookCommandValueCrossOSShapes(t *testing.T) {
	cases := []struct {
		name     string
		goos     string
		launcher string
		want     string
	}{
		{
			name:     "windows plain",
			goos:     "windows",
			launcher: `C:\Users\a\.gemini\antigravity-cli\bin\agy-tokens.bat`,
			want:     `C:\Users\a\.gemini\antigravity-cli\bin\agy-tokens.bat`,
		},
		{
			name:     "windows with a space",
			goos:     "windows",
			launcher: `C:\Users\a b\.gemini\antigravity-cli\bin\agy-tokens.bat`,
			want:     `"C:\Users\a b\.gemini\antigravity-cli\bin\agy-tokens.bat"`,
		},
		{
			name:     "posix plain",
			goos:     "linux",
			launcher: "/home/a/.local/bin/agy-tokens",
			want:     "/home/a/.local/bin/agy-tokens",
		},
		{
			name:     "posix with a space",
			goos:     "darwin",
			launcher: "/home/a b/.local/bin/agy-tokens",
			want:     "'/home/a b/.local/bin/agy-tokens'",
		},
		{
			name:     "posix with a quote",
			goos:     "linux",
			launcher: "/home/o'brien/.local/bin/agy-tokens",
			want:     `'/home/o'\''brien/.local/bin/agy-tokens'`,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			pinRuntimeGOOS(t, testCase.goos)
			got, ok := hookCommandValue("agy-tokens", testCase.launcher)
			if !ok {
				t.Fatalf("hookCommandValue(%q) refused a portable value", testCase.launcher)
			}
			if got != testCase.want {
				t.Fatalf("hookCommandValue = %q, want %q", got, testCase.want)
			}
			if unquoted := shellUnquote(got); unquoted != testCase.launcher {
				t.Fatalf("the value %q unquotes to %q, want the launcher %q", got, unquoted, testCase.launcher)
			}
			// The arguments the source carried are appended, never re-quoted.
			withArgs, ok := hookCommandValue("agy-tokens --hook --raw", testCase.launcher)
			if !ok || withArgs != testCase.want+" --hook --raw" {
				t.Fatalf("hookCommandValue with arguments = %q, %v; want %q", withArgs, ok, testCase.want+" --hook --raw")
			}
		})
	}
}

// TestHookPlanBindsTheRewrittenDigest is requirement 2: the plan, the manifest and
// Status all speak about the REWRITTEN bytes, and a second apply writes nothing.
func TestHookPlanBindsTheRewrittenDigest(t *testing.T) {
	sandbox := newDeploySandbox(t)
	launcher := writeSandboxLauncher(t, sandbox, "windows")
	source := hookPortableFixtureContent()

	rewritten, posture := hookCommandRewriteFor(hookPortableFixtureJSON)
	if posture.Posture != HookCommandAbsolute {
		t.Fatalf("posture = %+v, want an absolute rewrite", posture)
	}

	plan := sandbox.plan(FeatureHook, source)
	if plan.HookCommand == nil || plan.HookCommand.Posture != HookCommandAbsolute {
		t.Fatalf("plan posture = %+v, want the absolute rewrite", plan.HookCommand)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("plan has %d action(s), want 1", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.NewSHA != SHA256Hex(rewritten) {
		t.Fatalf("plan digest = %q, want the digest of the rewritten bytes", action.NewSHA)
	}
	if action.NewSHA == SHA256Hex(hookPortableFixtureJSON) {
		t.Fatal("the plan digest is the SOURCE digest; the binding must be to the rewritten bytes")
	}

	counter := countingDeployWrites(t)
	summary := sandbox.apply(plan)
	if summary.Created != 1 || *counter != 1 {
		t.Fatalf("apply = %+v with %d write(s), want 1 create and 1 write", summary, *counter)
	}
	destination := sandbox.dest(FeatureHook, hookSourceFile)
	assertFileBytes(t, destination, rewritten)
	if strings.Contains(string(readFileRaw(t, destination)), `"command": "agy-tokens"`) {
		t.Fatalf("the deployed file still names the bare launcher:\n%s", string(readFileRaw(t, destination)))
	}
	if !strings.Contains(string(readFileRaw(t, destination)), hookJSONLiteral(t, quoteLauncher(launcher))) {
		t.Fatal("the deployed file does not name the absolute launcher")
	}

	entries := sandbox.entriesFor(FeatureHook)
	if len(entries) != 1 || entries[0].SHA256 != SHA256Hex(rewritten) {
		t.Fatalf("manifest entries = %+v, want the rewritten digest recorded", entries)
	}
	if entries[0].Provenance != ProvenanceGoApplied {
		t.Fatalf("provenance = %q, want go-applied", entries[0].Provenance)
	}

	status := sandbox.status(FeatureHook)
	if !status.OK() || status.Clean != 1 || status.Drifted != 0 {
		t.Fatalf("status = %+v, want a clean deployment of the rewritten bytes", status.Destinations)
	}

	// Idempotent re-apply: nothing to write, no manifest rewrite.
	again := sandbox.plan(FeatureHook, source)
	if got := again.Writes(); got != 0 {
		t.Fatalf("second plan writes %d destination(s), want 0 (%v)", got, again.Lines())
	}
	before := modTime(t, sandbox.manifestPath)
	second := sandbox.apply(again)
	if second.Written() != 0 || second.ManifestUpdated {
		t.Fatalf("second apply = %+v, want nothing written and no manifest update", second)
	}
	if *counter != 1 {
		t.Fatalf("second apply wrote %d destination(s) in total, want the first apply's 1", *counter)
	}
	if after := modTime(t, sandbox.manifestPath); !after.Equal(before) {
		t.Fatalf("the manifest was rewritten by an all-identical apply (%v -> %v)", before, after)
	}
}

// TestHookFallbackDeploysBareAndDriftsWhenTheLauncherAppears is requirement 3: a
// launcherless machine gets the portable bytes and reads clean; the same machine
// reads drifted the moment the launcher lands, and repair pins the command.
func TestHookFallbackDeploysBareAndDriftsWhenTheLauncherAppears(t *testing.T) {
	sandbox := newDeploySandbox(t)
	pinRuntimeGOOS(t, "windows")
	source := hookPortableFixtureContent()

	plan := sandbox.plan(FeatureHook, source)
	if plan.HookCommand == nil || plan.HookCommand.Posture != HookCommandBare {
		t.Fatalf("plan posture = %+v, want the bare fallback", plan.HookCommand)
	}
	if text := plan.HookCommand.PostureText(); !strings.Contains(text, "keeps the bare PATH name") {
		t.Fatalf("posture text = %q, want it to name the fallback", text)
	}
	if text := plan.HookCommand.PostureText(); !strings.Contains(text, launcherShapeWindows) {
		t.Fatalf("posture text = %q, want it to name the documented launcher shape", text)
	}
	lines := DeployLines(FeatureHook, plan)
	if !strings.Contains(strings.Join(lines, "\n"), "command") {
		t.Fatalf("the deploy diff carries no command row: %v", lines)
	}

	sandbox.apply(plan)
	destination := sandbox.dest(FeatureHook, hookSourceFile)
	assertFileBytes(t, destination, hookPortableFixtureJSON)

	// The fallback IS the intended posture here: no drift, no phantom repair.
	status := sandbox.status(FeatureHook)
	if status.Drifted != 0 || status.Clean != 1 || !status.OK() {
		t.Fatalf("status on the launcherless machine = %+v, want clean", status.Destinations)
	}

	// The launcher lands. The bytes did not change, but they are no longer what
	// this machine calls for.
	writeSandboxLauncher(t, sandbox, "windows")

	status = sandbox.status(FeatureHook)
	if status.Drifted != 1 || status.Clean != 0 {
		t.Fatalf("status after the launcher landed = %+v, want 1 drifted", status.Destinations)
	}
	if len(status.Destinations) != 1 || status.Destinations[0].State != StateDrifted {
		t.Fatalf("destination states = %+v, want drifted", status.Destinations)
	}
	if !strings.Contains(status.Destinations[0].Detail, "bare PATH name") {
		t.Fatalf("drift detail = %q, want it to name the stale posture", status.Destinations[0].Detail)
	}

	// Repair re-plans and re-applies, which is what pins the command.
	write := ComposeRepair(source)
	hookSteps := 0
	for _, step := range write.Steps() {
		if step.Feature != FeatureHook {
			continue
		}
		hookSteps++
		if step.Kind != StepApply {
			t.Fatalf("repair composed %v for the hook, want an apply", step.Kind)
		}
		if !strings.Contains(strings.Join(step.Lines, "\n"), "pinned to the absolute launcher") {
			t.Fatalf("the repair diff does not name the pin: %v", step.Lines)
		}
		if _, err := step.Run(); err != nil {
			t.Fatalf("repair step: %v", err)
		}
	}
	if hookSteps != 1 {
		t.Fatalf("repair composed %d hook step(s), want 1", hookSteps)
	}

	rewritten, _ := hookCommandRewriteFor(hookPortableFixtureJSON)
	assertFileBytes(t, destination, rewritten)
	status = sandbox.status(FeatureHook)
	if !status.OK() || status.Clean != 1 {
		t.Fatalf("status after the repair = %+v, want clean", status.Destinations)
	}
}

// TestHookRetractArchivesTheRewrittenBytes is requirement 7's retract half: the
// bytes moved into the backup tree are the ones that were live, not the source.
func TestHookRetractArchivesTheRewrittenBytes(t *testing.T) {
	sandbox := newDeploySandbox(t)
	writeSandboxLauncher(t, sandbox, "windows")
	source := hookPortableFixtureContent()

	sandbox.apply(sandbox.plan(FeatureHook, source))
	rewritten, _ := hookCommandRewriteFor(hookPortableFixtureJSON)
	destination := sandbox.dest(FeatureHook, hookSourceFile)
	assertFileBytes(t, destination, rewritten)

	summary, err := Retract(FeatureHook)
	if err != nil {
		t.Fatalf("Retract(hook): %v", err)
	}
	if len(summary.Destinations) != 1 {
		t.Fatalf("retract handled %d destination(s), want 1: %+v", len(summary.Destinations), summary.Destinations)
	}
	archived := summary.Destinations[0]
	if archived.Missing || archived.Drifted || archived.BackupPath == "" {
		t.Fatalf("retracted destination = %+v, want a moved, undrifted destination with a backup path", archived)
	}
	archivedBytes := readFileRaw(t, archived.BackupPath)
	if string(archivedBytes) != string(rewritten) {
		t.Fatalf("the archive holds %q, want the live (rewritten) bytes", string(archivedBytes))
	}
	if string(archivedBytes) == string(hookPortableFixtureJSON) {
		t.Fatal("the archive holds the SOURCE bytes; retract must move what was live")
	}
	assertAbsent(t, destination)
	if status := sandbox.status(FeatureHook); status.Deployed {
		t.Fatalf("status still reports the feature deployed: %+v", status.Destinations)
	}
}

// TestHookForeignFileSurvivesApplyAndRetract is requirement 4: the single-file
// feature keeps its narrow claim. A file nobody recorded is never adopted, never
// rewritten and never moved.
func TestHookForeignFileSurvivesApplyAndRetract(t *testing.T) {
	sandbox := newDeploySandbox(t)
	writeSandboxLauncher(t, sandbox, "windows")
	source := hookPortableFixtureContent()

	foreign := filepath.Join(filepath.Dir(sandbox.dest(FeatureHook, hookSourceFile)), "custom.json")
	writeFileRaw(t, foreign, []byte("{\"mine\":true}\n"))

	sandbox.apply(sandbox.plan(FeatureHook, source))
	assertFileBytes(t, foreign, []byte("{\"mine\":true}\n"))

	if _, err := Retract(FeatureHook); err != nil {
		t.Fatalf("Retract(hook): %v", err)
	}
	assertFileBytes(t, foreign, []byte("{\"mine\":true}\n"))
}

// TestAdoptRefusesTheBareHookOnceALauncherExists is requirement 7's adopt pin: the
// record-only verb records a live file only when it already holds the engine's
// bytes. A bare live file on a machine with a launcher is NOT identical any more,
// so adopt refuses and names apply; the composed adopt surfaces that as a note.
func TestAdoptRefusesTheBareHookOnceALauncherExists(t *testing.T) {
	sandbox := newDeploySandbox(t)
	pinRuntimeGOOS(t, "windows")
	source := hookPortableFixtureContent()
	destination := sandbox.dest(FeatureHook, hookSourceFile)

	// Half one: no launcher, so the bare file on disk IS the engine's content.
	writeFileRaw(t, destination, hookPortableFixtureJSON)
	step, notes, err := ComposeDeployStep(FeatureHook, StepAdopt, source, false)
	if err != nil {
		t.Fatalf("adopt of the bare file on a launcherless machine: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("adopt notes = %v, want none", notes)
	}
	if _, err := step.Run(); err != nil {
		t.Fatalf("adopt step: %v", err)
	}
	entries := sandbox.entriesFor(FeatureHook)
	if len(entries) != 1 || entries[0].Provenance != ProvenanceAdopted {
		t.Fatalf("entries = %+v, want one adopted entry", entries)
	}
	if entries[0].SHA256 != SHA256Hex(hookPortableFixtureJSON) {
		t.Fatalf("adopted digest = %q, want the bare bytes that were on disk", entries[0].SHA256)
	}
	assertFileBytes(t, destination, hookPortableFixtureJSON)

	// Half two: the launcher lands, so the same live file is no longer identical.
	writeSandboxLauncher(t, sandbox, "windows")

	if _, _, err := ComposeDeployStep(FeatureHook, StepAdopt, source, false); err == nil {
		t.Fatal("adopt accepted a bare live file while a launcher exists")
	} else if !strings.Contains(err.Error(), "differ") {
		t.Fatalf("adopt refusal = %v, want it to say the bytes differ", err)
	}

	write, err := ComposeAdopt(source)
	if err != nil {
		t.Fatalf("ComposeAdopt: %v", err)
	}
	joined := strings.Join(write.Notes(), "\n")
	if !strings.Contains(joined, "hook not adopted") {
		t.Fatalf("the composed adopt carries no refusal note for the hook: %v", write.Notes())
	}
	for _, composed := range write.Steps() {
		if composed.Feature == FeatureHook {
			t.Fatalf("the composed adopt composed a hook step even though the bytes differ: %+v", composed.Lines)
		}
	}
	// The refused adopt left the live bytes alone: adopting is not applying.
	assertFileBytes(t, destination, hookPortableFixtureJSON)
}

// TestHookHandEditIsDriftedAndTheStalePlanRefuses is requirement 2's second half:
// a live hook file that no longer matches the recorded (rewritten) bytes is
// drifted, and a plan built before the edit refuses to write over it.
func TestHookHandEditIsDriftedAndTheStalePlanRefuses(t *testing.T) {
	sandbox := newDeploySandbox(t)
	writeSandboxLauncher(t, sandbox, "windows")
	source := hookPortableFixtureContent()

	sandbox.apply(sandbox.plan(FeatureHook, source))
	destination := sandbox.dest(FeatureHook, hookSourceFile)

	// A plan built against the deployed bytes (so: identical), then a hand edit.
	stalePlan := sandbox.plan(FeatureHook, source)
	if stalePlan.Writes() != 0 {
		t.Fatalf("the pre-edit plan writes %d destination(s), want 0", stalePlan.Writes())
	}
	edited := strings.Replace(string(readFileRaw(t, destination)), `"enabled": true`, `"enabled": false`, 1)
	writeFileRaw(t, destination, []byte(edited))

	status := sandbox.status(FeatureHook)
	if status.Drifted != 1 || status.Clean != 0 {
		t.Fatalf("status after the hand edit = %+v, want 1 drifted", status.Destinations)
	}
	if detail := status.Destinations[0].Detail; detail != "" {
		t.Fatalf("a plain digest drift carries detail %q; that field is for posture drift only", detail)
	}

	if _, err := Apply(stalePlan); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("Apply(stale plan) error = %v, want ErrPlanStale", err)
	}
	assertFileBytes(t, destination, []byte(edited))

	// A fresh plan adopts the edit as an update and pins the command again.
	fresh := sandbox.plan(FeatureHook, source)
	if fresh.Writes() != 1 {
		t.Fatalf("the fresh plan writes %d destination(s), want 1", fresh.Writes())
	}
	sandbox.apply(fresh)
	assertFileBytes(t, destination, mustRewrite(t))
	if status := sandbox.status(FeatureHook); !status.OK() {
		t.Fatalf("status after the re-apply = %+v, want clean", status.Destinations)
	}
}

// mustRewrite returns the bytes the fixture deploys on this sandbox, failing the
// test when the rewrite refused a portable fixture.
func mustRewrite(t *testing.T) []byte {
	t.Helper()
	data, posture := hookCommandRewriteFor(hookPortableFixtureJSON)
	if posture.Posture != HookCommandAbsolute {
		t.Fatalf("posture = %+v, want an absolute rewrite", posture)
	}
	return data
}

// TestHookPostureRowIsNotANote pins the renderer choice: the posture is a fact row
// inside the hook's deploy block, and the composed step adds no note. A second note
// on the same missing launcher would break the "exactly one note explains the
// subtracted statusline step" rule the CLI pins.
func TestHookPostureRowIsNotANote(t *testing.T) {
	sandbox := newDeploySandbox(t)
	pinRuntimeGOOS(t, "windows")

	step, notes, err := ComposeDeployStep(FeatureHook, StepApply, hookPortableFixtureContent(), false)
	if err != nil {
		t.Fatalf("ComposeDeployStep: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("the hook step carries notes %v, want none (the posture is a row)", notes)
	}
	joined := strings.Join(step.Lines, "\n")
	if !strings.Contains(joined, "\ncommand") {
		t.Fatalf("the diff carries no column-0 command row:\n%s", joined)
	}
	if !strings.Contains(joined, "keeps the bare PATH name") {
		t.Fatalf("the fallback diff does not name the posture:\n%s", joined)
	}

	// With a launcher the row names the pin instead.
	writeSandboxLauncher(t, sandbox, "windows")
	pinned, _, err := ComposeDeployStep(FeatureHook, StepApply, hookPortableFixtureContent(), false)
	if err != nil {
		t.Fatalf("ComposeDeployStep with a launcher: %v", err)
	}
	if !strings.Contains(strings.Join(pinned.Lines, "\n"), "pinned to the absolute launcher") {
		t.Fatalf("the pinned diff does not name the absolute launcher:\n%s", strings.Join(pinned.Lines, "\n"))
	}
}

// TestSyncGateInputsAreUntouchedByTheRewrite is requirement 2's second half: the
// portable files stay portable. The snapshot and the repo file are still the same
// bare bytes the sync gate compares, and the rewrite exists only in the deployed
// copy.
func TestSyncGateInputsAreUntouchedByTheRewrite(t *testing.T) {
	repoPath := filepath.Join("..", "..", "..", "hooks", hookSourceFile)
	repoBytes, err := os.ReadFile(repoPath)
	if err != nil {
		t.Skipf("no repo-level hooks/%s at %q: a standalone agy-pro checkout ships none (not a product failure)", hookSourceFile, repoPath)
	}
	snapshotBytes, err := fs.ReadFile(profiles.GetToolsFS(), sourceDirHooks+"/"+hookSourceFile)
	if err != nil {
		t.Fatalf("read hooks.json from the embedded snapshot: %v", err)
	}
	if string(snapshotBytes) != string(repoBytes) {
		t.Fatalf("the snapshot (%d bytes) no longer equals the repo file (%d bytes); the sync gate's inputs drifted",
			len(snapshotBytes), len(repoBytes))
	}
	if !hookNamesPortableCommand(snapshotBytes) {
		t.Fatal("the snapshot no longer carries the portable bare launcher; the rewrite would have nothing to pin")
	}
	if !hookNamesPortableCommand(repoBytes) {
		t.Fatal("the repo file no longer carries the portable bare launcher; the rewrite would have nothing to pin")
	}

	hookLauncherSandbox(t, "windows")
	deployed, posture := hookCommandRewriteFor(snapshotBytes)
	if posture.Posture != HookCommandAbsolute {
		t.Fatalf("posture over the real snapshot = %+v, want an absolute rewrite", posture)
	}
	if string(deployed) == string(snapshotBytes) {
		t.Fatal("the rewrite produced the source bytes; the deployed copy would still name the bare launcher")
	}
	if SHA256Hex(repoBytes) != SHA256Hex(snapshotBytes) {
		t.Fatal("the rewrite changed a source file rather than the deployed copy")
	}
}

// TestHookPortableTailIsAWholeWord pins the boundary rule the portable detection
// rides on.
func TestHookPortableTailIsAWholeWord(t *testing.T) {
	cases := []struct {
		value string
		tail  string
		ok    bool
	}{
		{value: "agy-tokens", tail: "", ok: true},
		{value: "agy-tokens --hook", tail: " --hook", ok: true},
		{value: "agy-tokens\t--hook", tail: "\t--hook", ok: true},
		{value: "agy-tokens-extra", ok: false},
		{value: "./agy-tokens", ok: false},
		{value: "/usr/local/bin/agy-tokens", ok: false},
		{value: "agy-tokensish --hook", ok: false},
		{value: "", ok: false},
	}
	for _, testCase := range cases {
		tail, ok := hookPortableTail(testCase.value)
		if ok != testCase.ok || tail != testCase.tail {
			t.Fatalf("hookPortableTail(%q) = %q, %v; want %q, %v", testCase.value, tail, ok, testCase.tail, testCase.ok)
		}
	}
	if _, ok := hookCommandValue("agy-tokens", ""); ok {
		t.Fatal("an empty launcher produced a command value")
	}
}

// TestHookCommandRewriteWithoutLauncherReportsTheReason pins the fallback record's
// completeness: a bare posture always names the documented shape, the diagnosis and
// the candidates it consulted, so a door can print why the command was not pinned.
func TestHookCommandRewriteWithoutLauncherReportsTheReason(t *testing.T) {
	home, launcher := hookLauncherSandbox(t, "windows")
	if err := os.Remove(launcher); err != nil {
		t.Fatalf("remove the sandbox launcher: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "antigravity-cli", "bin", "agy-tokens.bat")); !os.IsNotExist(err) {
		t.Fatalf("the launcher is still on disk: %v", err)
	}

	_, posture := hookCommandRewriteFor(hookPortableFixtureJSON)
	if posture.Posture != HookCommandBare {
		t.Fatalf("posture = %+v, want the bare fallback", posture)
	}
	if posture.Shape != launcherShapeWindows || posture.Detail == "" {
		t.Fatalf("the fallback record is incomplete: %+v", posture)
	}
	if len(posture.Candidates) == 0 {
		t.Fatalf("the fallback record names no candidate: %+v", posture)
	}
	for _, candidate := range posture.Candidates {
		if !filepath.IsAbs(candidate) {
			t.Fatalf("candidate %q is not absolute", candidate)
		}
	}
}
