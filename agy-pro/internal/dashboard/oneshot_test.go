package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agypro/internal/testfixtures"
)

// helpers ------------------------------------------------------------------

// fakeBin writes a fake executable named `name` into dir, using the extension
// the host's exec.LookPath actually resolves (a bare file on POSIX, a .cmd shim
// on Windows, which is also what an npm global install looks like).
func fakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	file := name
	if runtime.GOOS == "windows" {
		file = name + ".cmd"
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte("@echo off\r\nexit /b 0\r\n"), 0o755); err != nil {
		t.Fatalf("write fake bin %s: %v", path, err)
	}
	return path
}

// fakeNode writes a file LookPath resolves as `node` on the host.
func fakeNode(t *testing.T, dir string) string {
	t.Helper()
	name := "node"
	if runtime.GOOS == "windows" {
		name = "node.exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake node %s: %v", path, err)
	}
	return path
}

// fakeToolsRoot builds a directory shaped like an agy-tools checkout: the only
// thing the ladder looks for is bin/agy-tokens.js.
func fakeToolsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", bin, err)
	}
	entry := filepath.Join(bin, "agy-tokens.js")
	if err := os.WriteFile(entry, []byte("// fake entry\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", entry, err)
	}
	return root
}

// isolatePath replaces PATH with exactly dirs, so the ladder sees only the
// fakes a subtest planted.
func isolatePath(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

// helloDashboardPayload is the fixture the helper child writes: a v3 payload
// whose numbers are distinctive enough that a reload assertion cannot pass on
// an empty or stale snapshot.
const helloDashboardPayload = `{
  "version": 3,
  "generatedAt": "2026-10-22T18:00:00.000Z",
  "summaries": {
    "today": {"totalSessions": 4, "totalTokens": 51000, "costUsd": 0.25},
    "last7d": {"totalSessions": 9, "totalTokens": 120000, "costUsd": 1.5}
  },
  "models": [
    {"model": "fixture-model", "totalTokens": 51000, "costUsd": 0.25, "cacheHitRate": 0.5, "sessions": 4}
  ]
}`

// helperInvocation finds the `-- <mode> <dir>` trailer the spawn seam appends.
// The normal `go test` run carries no trailer, so the helper returns
// immediately and this file needs no TestMain.
func helperInvocation() (mode, dir string, ok bool) {
	for i, a := range os.Args {
		if a == "--" && len(os.Args) > i+2 {
			return os.Args[i+1], os.Args[i+2], true
		}
	}
	return "", "", false
}

// TestOneShotHelperProcess is the child-side helper: it plays the Node process
// without needing Node. Modes:
//
//	write - writes dashboard-data.json into <dir> plus child-env.txt, exits 0
//	fail  - writes a diagnostic to stderr, exits 3
//	sleep - sleeps two seconds (deadline tests), exits 0
func TestOneShotHelperProcess(t *testing.T) {
	mode, dir, ok := helperInvocation()
	if !ok {
		return
	}
	switch mode {
	case "write":
		if err := os.WriteFile(filepath.Join(dir, dataFileName), []byte(helloDashboardPayload), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "helper: write fixture:", err)
			os.Exit(9)
		}
		env := strings.Join(os.Environ(), "\n")
		if err := os.WriteFile(filepath.Join(dir, "child-env.txt"), []byte(env), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "helper: write env:", err)
			os.Exit(9)
		}
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "boom: the helper failed on purpose")
		os.Exit(3)
	case "sleep":
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}
	os.Exit(0)
}

// spawnHelper swaps the exec seam for the test-binary helper. The trailer is
// positional, so the helper needs no environment of its own (Refresh hands the
// child the default-deny allow-list, which would drop any helper-only variable).
func spawnHelper(t *testing.T, mode, dir string) {
	t.Helper()
	prev := spawnOneShot
	spawnOneShot = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestOneShotHelperProcess", "--", mode, dir)
	}
	t.Cleanup(func() { spawnOneShot = prev })
}

// TestSplitArgv pins the AGY_DASHBOARD_ENTRY tokenizer: whitespace separates,
// double quotes group (so a Windows path with spaces is expressible), and an
// unbalanced quote is an error rather than a silently mangled argv.
func TestSplitArgv(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		bad  bool
	}{
		{name: "plain", in: "agy-tokens --html", want: []string{"agy-tokens", "--html"}},
		{name: "collapsing whitespace", in: "  node \t x.js \n --fresh ", want: []string{"node", "x.js", "--fresh"}},
		{name: "quoted group", in: `"C:/Program Files/nodejs/node.exe" C:/tools/bin/agy-tokens.js`,
			want: []string{"C:/Program Files/nodejs/node.exe", "C:/tools/bin/agy-tokens.js"}},
		{name: "quoted empty token", in: `a "" b`, want: []string{"a", "", "b"}},
		{name: "unbalanced quote", in: `"C:/Program Files/node`, bad: true},
		{name: "empty", in: "   ", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitArgv(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("splitArgv(%q) = %v, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitArgv(%q): %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("splitArgv(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitArgv(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestResolveEntry walks every rung of the discovery ladder plus its failure.
func TestResolveEntry(t *testing.T) {
	t.Run("env override wins over PATH", func(t *testing.T) {
		dir := t.TempDir()
		fakeBin(t, dir, "agy-tokens")
		isolatePath(t, dir)
		t.Setenv(EnvDashboardEntry, `"C:/a b/node.exe" "C:/a b/bin/agy-tokens.js" --fresh`)
		t.Setenv(EnvProToolsRoot, "")

		entry, err := ResolveEntry()
		if err != nil {
			t.Fatalf("ResolveEntry: %v", err)
		}
		if entry.Source != SourceEnvOverride {
			t.Fatalf("source = %q, want %q", entry.Source, SourceEnvOverride)
		}
		want := []string{"C:/a b/node.exe", "C:/a b/bin/agy-tokens.js", "--fresh"}
		if strings.Join(entry.Argv, "|") != strings.Join(want, "|") {
			t.Fatalf("argv = %#v, want %#v", entry.Argv, want)
		}
	})

	t.Run("agy-tokens on PATH", func(t *testing.T) {
		dir := t.TempDir()
		bin := fakeBin(t, dir, "agy-tokens")
		isolatePath(t, dir)
		t.Setenv(EnvDashboardEntry, "")
		t.Setenv(EnvProToolsRoot, "")

		entry, err := ResolveEntry()
		if err != nil {
			t.Fatalf("ResolveEntry: %v", err)
		}
		if entry.Source != SourcePath {
			t.Fatalf("source = %q, want %q", entry.Source, SourcePath)
		}
		if len(entry.Argv) != 1 || entry.Argv[0] != bin {
			t.Fatalf("argv = %#v, want [%q]", entry.Argv, bin)
		}
	})

	t.Run("agy-dashboard is the second PATH candidate", func(t *testing.T) {
		dir := t.TempDir()
		bin := fakeBin(t, dir, "agy-dashboard")
		isolatePath(t, dir)
		t.Setenv(EnvDashboardEntry, "")
		t.Setenv(EnvProToolsRoot, "")

		entry, err := ResolveEntry()
		if err != nil {
			t.Fatalf("ResolveEntry: %v", err)
		}
		if entry.Argv[0] != bin {
			t.Fatalf("argv[0] = %q, want %q", entry.Argv[0], bin)
		}
		if !strings.Contains(entry.Detail, dashboardBinName) {
			t.Fatalf("detail = %q, want it to name %q", entry.Detail, dashboardBinName)
		}
	})

	t.Run("node plus tools root", func(t *testing.T) {
		dir := t.TempDir()
		node := fakeNode(t, dir)
		root := fakeToolsRoot(t)
		isolatePath(t, dir)
		t.Setenv(EnvDashboardEntry, "")
		t.Setenv(EnvProToolsRoot, root)

		entry, err := ResolveEntry()
		if err != nil {
			t.Fatalf("ResolveEntry: %v", err)
		}
		if entry.Source != SourceNodeEntry {
			t.Fatalf("source = %q, want %q", entry.Source, SourceNodeEntry)
		}
		wantEntry := filepath.Join(root, "bin", "agy-tokens.js")
		if len(entry.Argv) != 2 || entry.Argv[0] != node || entry.Argv[1] != wantEntry {
			t.Fatalf("argv = %#v, want [%q %q]", entry.Argv, node, wantEntry)
		}
	})

	t.Run("batch shim without node stays the shim", func(t *testing.T) {
		dir := t.TempDir()
		bin := fakeBin(t, dir, "agy-tokens")
		isolatePath(t, dir)
		t.Setenv(EnvDashboardEntry, "")
		t.Setenv(EnvProToolsRoot, "")

		entry, err := ResolveEntry()
		if err != nil {
			t.Fatalf("ResolveEntry: %v", err)
		}
		if entry.Argv[0] != bin {
			t.Fatalf("argv[0] = %q, want %q", entry.Argv[0], bin)
		}
		if runtime.GOOS != "windows" {
			// Off Windows the fake is a plain file and there is no shim to wrap;
			// the wrap itself is pinned by TestBatchCmdWrapsWindowsShim.
			return
		}
		if !isBatchScript(entry.Argv[0]) {
			t.Fatalf("expected the Windows shim %q to be a batch script", entry.Argv[0])
		}
	})

	t.Run("windows shim prefers node plus entry", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("npm .cmd shim selection is Windows-only")
		}
		dir := t.TempDir()
		node := fakeNode(t, dir)
		fakeBin(t, dir, "agy-tokens")
		root := fakeToolsRoot(t)
		isolatePath(t, dir)
		t.Setenv(EnvDashboardEntry, "")
		t.Setenv(EnvProToolsRoot, root)

		entry, err := ResolveEntry()
		if err != nil {
			t.Fatalf("ResolveEntry: %v", err)
		}
		if entry.Source != SourceNodeEntry {
			t.Fatalf("source = %q, want %q (a resolvable node + entry must beat the batch wrap)", entry.Source, SourceNodeEntry)
		}
		if entry.Argv[0] != node {
			t.Fatalf("argv[0] = %q, want the interpreter %q", entry.Argv[0], node)
		}
	})

	t.Run("not found", func(t *testing.T) {
		isolatePath(t, t.TempDir())
		t.Setenv(EnvDashboardEntry, "")
		t.Setenv(EnvProToolsRoot, "")

		_, err := ResolveEntry()
		if err == nil {
			t.Fatal("ResolveEntry succeeded on an empty PATH with no override")
		}
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("error = %v (%T), want *NotFoundError", err, err)
		}
		if len(nf.Checked) == 0 {
			t.Fatal("the not-found error names no checked rung")
		}
		for _, want := range []string{EnvDashboardEntry, tokensBinName, dashboardBinName} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not name %q", err.Error(), want)
			}
		}
	})

	t.Run("env override refuses --open", func(t *testing.T) {
		t.Setenv(EnvDashboardEntry, "agy-tokens --open")
		_, err := ResolveEntry()
		var refused *RefusedArgvError
		if !errors.As(err, &refused) {
			t.Fatalf("error = %v (%T), want *RefusedArgvError", err, err)
		}
		if refused.Token != "--open" {
			t.Fatalf("refused token = %q, want --open", refused.Token)
		}
	})

	t.Run("env override refuses --serve", func(t *testing.T) {
		t.Setenv(EnvDashboardEntry, "agy-tokens --serve=9000")
		_, err := ResolveEntry()
		var refused *RefusedArgvError
		if !errors.As(err, &refused) {
			t.Fatalf("error = %v (%T), want *RefusedArgvError", err, err)
		}
	})

	t.Run("malformed env override is reported, not ignored", func(t *testing.T) {
		t.Setenv(EnvDashboardEntry, `"unbalanced`)
		_, err := ResolveEntry()
		if err == nil {
			t.Fatal("ResolveEntry accepted an unbalanced override")
		}
		if !strings.Contains(err.Error(), EnvDashboardEntry) {
			t.Fatalf("error %q does not name %s", err.Error(), EnvDashboardEntry)
		}
	})
}

// TestRefreshAppendsHeadlessFlags pins the argv contract: the refresh flags are
// appended AFTER the resolved entry, so an entry that is itself a multi-token
// argv (node + script, an env override with options) still receives them.
func TestRefreshAppendsHeadlessFlags(t *testing.T) {
	dir := t.TempDir()
	spawnHelper(t, "write", dir)

	var got []string
	prev := spawnOneShot
	spawnOneShot = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		return prev(ctx, name, args...)
	}
	t.Cleanup(func() { spawnOneShot = prev })

	res, err := Refresh(context.Background(), []string{"node", "bin/agy-tokens.js", "--fresh"}, 0)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := []string{"node", "bin/agy-tokens.js", "--fresh", refreshHTMLFlag, refreshNoOpenFlag}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("spawned argv = %#v, want %#v", got, want)
	}
	if strings.Join(res.Argv, "|") != strings.Join(want, "|") {
		t.Fatalf("Result.Argv = %#v, want %#v", res.Argv, want)
	}
	for _, a := range res.Argv {
		if a == "--open" {
			t.Fatalf("refresh argv carries --open: %#v", res.Argv)
		}
	}
}

// TestRefreshRefusesBrowserFlag is the belt pin on the exec side: a caller that
// hands Refresh an --open argv gets a typed refusal and NO child.
func TestRefreshRefusesBrowserFlag(t *testing.T) {
	spawned := 0
	prev := spawnOneShot
	spawnOneShot = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		spawned++
		return prev(ctx, name, args...)
	}
	t.Cleanup(func() { spawnOneShot = prev })

	for _, argv := range [][]string{
		{"agy-tokens", "--open"},
		{"agy-tokens", "--OPEN"},
		{"agy-tokens", "--open=true"},
		{"agy-tokens", "--serve", "--no-open"},
	} {
		_, err := Refresh(context.Background(), argv, time.Second)
		var refused *RefusedArgvError
		if !errors.As(err, &refused) {
			t.Fatalf("Refresh(%#v) error = %v (%T), want *RefusedArgvError", argv, err, err)
		}
	}
	if spawned != 0 {
		t.Fatalf("a refused argv still spawned %d children", spawned)
	}

	if _, err := Refresh(context.Background(), nil, time.Second); err == nil {
		t.Fatal("Refresh(nil argv) succeeded")
	}
}

// TestRefreshReloadsArtifacts is the exec-plus-reload round trip: the helper
// child (standing in for the Node process) writes a real payload into the
// package's artifact directory, and the S1 loader reads it back OK. This is the
// exact pair the screen performs, minus the API: refresh, then compose.
func TestRefreshReloadsArtifacts(t *testing.T) {
	dir := redirect(t)
	spawnHelper(t, "write", dir)

	res, err := Refresh(context.Background(), []string{"agy-tokens"}, RefreshTimeout)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
	if res.Elapsed <= 0 {
		t.Fatalf("elapsed = %v, want a measured duration", res.Elapsed)
	}
	if _, err := os.Stat(filepath.Join(dir, dataFileName)); err != nil {
		t.Fatalf("the child did not write %s: %v", dataFileName, err)
	}

	payload, status := LoadPayload()
	if status != StatusOK {
		t.Fatalf("reload status = %v, want ok", status)
	}
	if got := payload.Summaries.Today.TotalTokens; got != 51000 {
		t.Fatalf("today tokens = %d, want 51000 (the reload did not see the child's payload)", got)
	}
	if len(payload.Models) != 1 || payload.Models[0].Model != "fixture-model" {
		t.Fatalf("models = %#v, want the single fixture row", payload.Models)
	}
}

// TestRefreshScrubsChildEnv pins the default-deny child environment: a
// provider key value in the shim environment must not reach the child, while
// the OS variables a Node process needs (HOME, PATH) must.
func TestRefreshScrubsChildEnv(t *testing.T) {
	dir := redirect(t)
	spawnHelper(t, "write", dir)

	const sentinel = "OPAQUE-SENTINEL-VALUE"
	t.Setenv("OPENAI_API_KEY", sentinel)
	t.Setenv("AGY_LLM_GATEWAY_API_KEY", sentinel)

	if _, err := Refresh(context.Background(), []string{"agy-tokens"}, RefreshTimeout); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "child-env.txt"))
	if err != nil {
		t.Fatalf("read child env dump: %v", err)
	}
	dump := string(raw)
	if strings.Contains(dump, sentinel) {
		t.Fatalf("a provider key value reached the child:\n%s", dump)
	}
	for _, name := range []string{"OPENAI_API_KEY=", "AGY_LLM_GATEWAY_API_KEY="} {
		if strings.Contains(dump, name) {
			t.Fatalf("%s reached the child:\n%s", strings.TrimSuffix(name, "="), dump)
		}
	}
	// The allow-list half: the child cannot start Node without these.
	if !strings.Contains(dump, "PATH=") {
		t.Fatalf("PATH was dropped from the child environment:\n%s", dump)
	}
	home := "HOME="
	if runtime.GOOS == "windows" {
		home = "USERPROFILE="
	}
	if !strings.Contains(dump, home) {
		t.Fatalf("%s was dropped from the child environment:\n%s", strings.TrimSuffix(home, "="), dump)
	}
}

// TestRefreshTimeout pins the bounded run: a child that outlives the deadline
// yields a typed timeout, not a hung screen, and the error unwraps to
// context.DeadlineExceeded so a caller can match either form.
func TestRefreshTimeout(t *testing.T) {
	dir := t.TempDir()
	spawnHelper(t, "sleep", dir)

	start := time.Now()
	_, err := Refresh(context.Background(), []string{"agy-tokens"}, 150*time.Millisecond)
	if err == nil {
		t.Fatal("Refresh on a 2s child with a 150ms deadline returned nil")
	}
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v (%T), want *TimeoutError", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error does not unwrap to context.DeadlineExceeded: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("Refresh took %v, want the deadline to bite near 150ms", elapsed)
	}
}

// TestRefreshClampsTimeout pins the ceiling: a caller asking for longer than
// RefreshTimeout gets RefreshTimeout, so no code path can stall the key loop
// for more than the contract allows.
func TestRefreshClampsTimeout(t *testing.T) {
	dir := t.TempDir()
	var remaining time.Duration
	prev := spawnOneShot
	spawnOneShot = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if dl, ok := ctx.Deadline(); ok {
			remaining = time.Until(dl)
		}
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestOneShotHelperProcess", "--", "write", dir)
	}
	t.Cleanup(func() { spawnOneShot = prev })

	if _, err := Refresh(context.Background(), []string{"agy-tokens"}, 10*time.Minute); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if remaining <= 0 || remaining > RefreshTimeout+200*time.Millisecond {
		t.Fatalf("child deadline = %v away, want <= %v", remaining, RefreshTimeout)
	}

	remaining = 0
	if _, err := Refresh(context.Background(), []string{"agy-tokens"}, 0); err != nil {
		t.Fatalf("Refresh(timeout 0): %v", err)
	}
	if remaining <= 0 || remaining > RefreshTimeout+200*time.Millisecond {
		t.Fatalf("timeout 0 gave a %v deadline, want the %v default", remaining, RefreshTimeout)
	}
}

// TestRefreshExecFailure pins the non-zero-exit branch: a typed ExecError with
// the child's code and a bounded, masked detail line.
func TestRefreshExecFailure(t *testing.T) {
	dir := t.TempDir()
	spawnHelper(t, "fail", dir)

	_, err := Refresh(context.Background(), []string{"agy-tokens"}, RefreshTimeout)
	if err == nil {
		t.Fatal("Refresh on an exit-3 child returned nil")
	}
	var xe *ExecError
	if !errors.As(err, &xe) {
		t.Fatalf("error = %v (%T), want *ExecError", err, err)
	}
	if xe.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", xe.ExitCode)
	}
	if !strings.Contains(xe.Detail, "boom") {
		t.Fatalf("detail = %q, want the child's own stderr", xe.Detail)
	}
	if strings.Contains(xe.Detail, "\n") {
		t.Fatalf("detail = %q, want one collapsed line", xe.Detail)
	}
}

// TestRefreshMasksKeyShapedChildOutput pins the masking belt: a child that
// echoes a key-shaped operand cannot carry it into an error value.
func TestRefreshMasksKeyShapedChildOutput(t *testing.T) {
	// The planted value comes from internal/testfixtures (built by
	// concatenation there), so this file spells no contiguous key shape.
	planted := testfixtures.SKProjMixed
	raw := maskedTail(planted, "")
	if strings.Contains(raw, planted) {
		t.Fatalf("maskedTail kept the planted key: %q", raw)
	}
	if !strings.Contains(raw, "****") {
		t.Fatalf("maskedTail = %q, want a masked form", raw)
	}
	// Long output is collapsed and cut to one bounded line.
	long := maskedTail(strings.Repeat("word ", 200), "")
	if len([]rune(long)) > 203 {
		t.Fatalf("maskedTail = %d runes, want <= 203", len([]rune(long)))
	}
}

// TestBatchCmdWrapsWindowsShim pins the B9 hazard guard: a plain binary is
// spawned directly, and a Windows .cmd target is wrapped through cmd.exe (the
// SysProcAttr half of that assertion lives in oneshot_cmdline_windows_test.go,
// because the field only exists on Windows). The escaping itself is a pure
// string transform and is pinned here on every host.
func TestBatchCmdWrapsWindowsShim(t *testing.T) {
	plain := batchCmd(context.Background(), "agy-tokens", refreshHTMLFlag, refreshNoOpenFlag)
	if got := filepath.Base(plain.Path); strings.EqualFold(got, "cmd.exe") {
		t.Fatalf("a non-batch target was wrapped through cmd.exe (%q)", plain.Path)
	}

	line := buildBatchCmdLine(`C:\Users\example\AppData\Roaming\npm\agy-tokens.cmd`,
		[]string{refreshHTMLFlag, refreshNoOpenFlag})
	for _, want := range []string{"/d", "/v:off", "/s", "/c", "agy-tokens.cmd", refreshHTMLFlag, refreshNoOpenFlag} {
		if !strings.Contains(line, want) {
			t.Fatalf("raw command line %q is missing %q", line, want)
		}
	}
	// The escaping is the copied B9 cluster; a path with a space must stay one
	// quoted token, not two arguments.
	spaced := buildBatchCmdLine(`C:\Program Files\npm\agy-tokens.cmd`, nil)
	if !strings.Contains(spaced, `"C:\Program Files\npm\agy-tokens.cmd"`) {
		t.Fatalf("a spaced shim path was not quoted as one token: %q", spaced)
	}
	// A metacharacter outside cmd quotes needs the triple caret; `%` needs the
	// quoted step-out so cmd cannot expand it as a variable.
	if got := buildBatchCmdLine(`C:\t\a.cmd`, []string{`a&b`}); !strings.Contains(got, "a&b") {
		t.Fatalf("metacharacter token lost its bytes: %q", got)
	}
	if got := buildBatchCmdLine(`C:\t\a.cmd`, []string{`50%off`}); strings.Contains(got, `%off"`) && !strings.Contains(got, `^%`) {
		t.Fatalf("percent token is not caret-escaped: %q", got)
	}
}

// realDashboardSnapshot captures the real artifact directory's entries with
// size and mtime, so the gated smoke can prove it did not touch them.
type realSnapshot struct {
	dir     string
	existed bool
	names   []string
}

func snapshotRealDashboardDir(t *testing.T, home string) realSnapshot {
	t.Helper()
	dir := filepath.Join(home, ".gemini", DirName)
	snap := realSnapshot{dir: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return snap
	}
	snap.existed = true
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", filepath.Join(dir, e.Name()), err)
		}
		snap.names = append(snap.names, fmt.Sprintf("%s|%d|%s", e.Name(), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano)))
	}
	return snap
}

// fileState is a (present, size, mtime) triple used for the user-config proof.
type fileState struct {
	present bool
	size    int64
	mod     string
}

func statFile(t *testing.T, path string) fileState {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileState{}
		}
		t.Fatalf("stat %s: %v", path, err)
	}
	return fileState{present: true, size: info.Size(), mod: info.ModTime().UTC().Format(time.RFC3339Nano)}
}

// TestRefreshRealOneShotSmoke is the gated live smoke: the REAL Node one-shot,
// with the child's home redirected so the artifacts land in a temp tree. It
// skips (named reason) when Node or the repo entry is missing.
//
// Why the redirect is mandatory: `--html` writes
// ~/.gemini/antigravity-dashboard/{dashboard-data.json,dashboard-data.js,dashboard.html}
// for the CURRENT home. Running it against the real user home would overwrite
// the operator's live artifacts, so both HOME and USERPROFILE are pointed at a
// temp dir. Both, not just HOME: Node's os.homedir() on Windows reads
// USERPROFILE (wave-1 blind-QA finding: a HOME-only redirect is insufficient
// under Git Bash, where HOME is set but Node does not consult it).
func TestRefreshRealOneShotSmoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the real one-shot smoke needs the Node runtime")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	entryScript := filepath.Join(repoRoot, "bin", "agy-tokens.js")
	if _, err := os.Stat(entryScript); err != nil {
		t.Skipf("repo entry %s is not present: %v", entryScript, err)
	}

	// The real home is measured, never written: it is the leak canary.
	realHome, err := homeDir()
	if err != nil {
		t.Skipf("the real user home does not resolve, so there is nothing to protect: %v", err)
	}
	realDir := filepath.Join(realHome, ".gemini", DirName)
	before := snapshotRealDashboardDir(t, realHome)
	agyHome := filepath.Join(realHome, ".agy-pro")
	stateBefore := statFile(t, filepath.Join(agyHome, "state.json"))
	providersBefore := statFile(t, filepath.Join(agyHome, "providers.json"))

	// Redirect the child's home. t.Setenv is restored automatically.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)
	t.Setenv(EnvProToolsRoot, "")
	t.Setenv(EnvDashboardEntry, `"`+node+`" "`+entryScript+`"`)

	resolved, err := ResolveEntry()
	if err != nil {
		t.Fatalf("ResolveEntry: %v", err)
	}
	if resolved.Source != SourceEnvOverride {
		t.Fatalf("source = %q, want the env override", resolved.Source)
	}

	res, err := Refresh(context.Background(), resolved.Argv, RefreshTimeout)
	if err != nil {
		t.Fatalf("real one-shot refresh failed: %v (stdout=%q stderr=%q)", err, res.Stdout, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("real one-shot exit = %d, want 0", res.ExitCode)
	}
	if res.Elapsed > RefreshTimeout {
		t.Fatalf("real one-shot took %v, want <= %v", res.Elapsed, RefreshTimeout)
	}

	// 1. The artifacts appeared under the REDIRECTED home.
	redirectedDir := filepath.Join(fakeHome, ".gemini", DirName)
	for _, name := range []string{dataFileName, htmlFileName, "dashboard-data.js"} {
		if _, err := os.Stat(filepath.Join(redirectedDir, name)); err != nil {
			t.Fatalf("the redirected run did not write %s under %s: %v", name, redirectedDir, err)
		}
	}
	// 2. Headless: no server record, so no --serve run happened.
	if _, err := os.Stat(filepath.Join(redirectedDir, serverFileName)); err == nil {
		t.Fatalf("the refresh wrote %s: a server start must be impossible here", serverFileName)
	}
	// 3. The payload the child wrote reloads through the S1 loader.
	prevOverride := dirOverride
	dirOverride = redirectedDir
	t.Cleanup(func() { dirOverride = prevOverride })
	payload, status := LoadPayload()
	if status != StatusOK {
		t.Fatalf("reload status = %v, want ok (the child's payload did not clear the version gate)", status)
	}
	if payload.Version != DashboardPayloadVersion {
		t.Fatalf("payload version = %d, want %d", payload.Version, DashboardPayloadVersion)
	}
	if strings.TrimSpace(payload.GeneratedAt) == "" {
		t.Fatal("the child's payload carries no generatedAt")
	}

	// 4. The REAL ~/.gemini dashboard artifacts were not touched. The window is
	// this test's own run (sub-second); a concurrent writer would move these
	// mtimes, which is a failure, not a pass.
	after := snapshotRealDashboardDir(t, realHome)
	if before.existed != after.existed || strings.Join(before.names, "\n") != strings.Join(after.names, "\n") {
		t.Fatalf("the real dashboard directory changed during a redirected run:\nbefore(%s):\n%s\nafter:\n%s",
			realDir, strings.Join(before.names, "\n"), strings.Join(after.names, "\n"))
	}
	// 5. The real user config was not touched either.
	if got := statFile(t, filepath.Join(agyHome, "state.json")); got != stateBefore {
		t.Fatalf("~/.agy-pro/state.json changed: %+v -> %+v", stateBefore, got)
	}
	if got := statFile(t, filepath.Join(agyHome, "providers.json")); got != providersBefore {
		t.Fatalf("~/.agy-pro/providers.json changed: %+v -> %+v", providersBefore, got)
	}
	// 6. Nothing in the child's output claims to have opened a browser.
	for _, stream := range []string{res.Stdout, res.Stderr} {
		if strings.Contains(strings.ToLower(stream), "--open") {
			t.Fatalf("child output mentions --open: %q", stream)
		}
	}
}

// TestHelloPayloadFixtureIsValidJSON is a compile-time guard that the fixture
// the helper child writes is valid JSON (an unparsable fixture would make the
// reload assertions vacuous).
func TestHelloPayloadFixtureIsValidJSON(t *testing.T) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(helloDashboardPayload), &probe); err != nil {
		t.Fatalf("helloDashboardPayload is not valid JSON: %v", err)
	}
	if _, ok := probe["version"]; !ok {
		t.Fatal("helloDashboardPayload carries no version key")
	}
}
