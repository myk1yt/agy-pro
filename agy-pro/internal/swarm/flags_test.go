package swarm

import (
	"strings"
	"testing"
)

// A2 (contract U3/C2): the interactive builder can edit the tools allow-list
// and the free-text task (builder.go editField cases 2 and 1), but the
// non-interactive flag surface had no equivalent, so a CI run could not
// reproduce an interactive build. The swarm-owned parsing surface now carries
// --tools and --task.
func TestParseSwarmFlagsToolsAndTask(t *testing.T) {
	args := []string{
		"--template", "code-review",
		"--seats", "1,3",
		"--tools", "read,search,write",
		"--task", "security",
		"--root", "/ws",
		"--timeout", "300",
		"--dry-run",
	}
	var errw strings.Builder
	f, code := ParseSwarmFlags(args, &errw)
	if code != 0 {
		t.Fatalf("ParseSwarmFlags exit = %d; want 0 (stderr=%q)", code, errw.String())
	}
	if f.Tools != "read,search,write" || f.Task != "security" {
		t.Fatalf("tools/task = %q/%q; want read,search,write/security", f.Tools, f.Task)
	}
	if !f.Set["tools"] || !f.Set["task"] {
		t.Errorf("Set = %v; want tools and task marked present", f.Set)
	}
	if !f.NonInteractive() {
		t.Error("any present flag must switch to the non-interactive path (F-B)")
	}
	if f.Template != "code-review" || f.Seats != "1,3" || f.Root != "/ws" || f.Timeout != 300 || !f.DryRun {
		t.Errorf("rest of the surface lost: %+v", f)
	}
	if got := f.EffectiveRoot(); got != "/ws" {
		t.Errorf("EffectiveRoot = %q; want /ws", got)
	}

	// The overrides land on the template BuildFanoutArgs already emits.
	tmpl, ok := f.ResolveTemplate()
	if !ok {
		t.Fatal("ResolveTemplate failed for code-review")
	}
	if tmpl.Tools != "read,search,write" || tmpl.Task != "security" {
		t.Errorf("resolved template = %+v; want the overridden tools/task", tmpl)
	}

	// ... and therefore reach the fanout argv.
	got := BuildFanoutArgs(tmpl, nil, f.EffectiveRoot(), "p", f.Timeout)
	want := map[string]string{"--tools": "read,search,write", "--task": "security"}
	for i := 0; i+1 < len(got); i += 2 {
		if w, ok := want[got[i]]; ok && got[i+1] != w {
			t.Errorf("argv %s = %q; want %q", got[i], got[i+1], w)
		}
	}
}

// A2: an unset --tools / --task leaves the template defaults alone, so the
// flag surface is purely additive.
func TestSwarmFlagsLeaveTemplateDefaultsWhenUnset(t *testing.T) {
	var errw strings.Builder
	f, code := ParseSwarmFlags([]string{"--template", "security-audit", "--seats", "p1"}, &errw)
	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if f.Set["tools"] || f.Set["task"] {
		t.Errorf("Set = %v; want tools/task absent", f.Set)
	}
	tmpl, ok := f.ResolveTemplate()
	if !ok {
		t.Fatal("ResolveTemplate failed for security-audit")
	}
	if tmpl.Tools != "read,search" || tmpl.Task != "security" {
		t.Errorf("template defaults = tools:%q task:%q; want read,search/security", tmpl.Tools, tmpl.Task)
	}

	// An unknown template name is reported (the caller maps it to exit 2).
	bad, code := ParseSwarmFlags([]string{"--template", "nope"}, &errw)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (parse-level)", code)
	}
	if _, ok := bad.ResolveTemplate(); ok {
		t.Error("an unknown template must not resolve")
	}
}

// A2: --tools is validated fail-closed with the SAME parser the fanout CLI
// uses (S-8): an unknown token is an error, never a substring match.
func TestSwarmValidateToolsFailClosed(t *testing.T) {
	if got, err := ValidateTools("read,search,write,exec"); err != nil || got != "read,search,write,exec" {
		t.Errorf("ValidateTools = %q, %v; want the canonical form", got, err)
	}
	for _, bad := range []string{"read,nowrite", "noexec", "read;search", "write_file"} {
		if _, err := ValidateTools(bad); err == nil {
			t.Errorf("ValidateTools(%q) must be rejected", bad)
		}
	}
}

// A2: the usage text documents both new flags, and an unknown flag / a
// positional argument is a usage error (exit 2).
func TestParseSwarmFlagsUsageAndErrors(t *testing.T) {
	for _, name := range []string{"--tools", "--task"} {
		if !strings.Contains(SwarmUsage, name) {
			t.Errorf("SwarmUsage must document %s:\n%s", name, SwarmUsage)
		}
	}
	for _, name := range SwarmFlagNames {
		if !strings.Contains(SwarmUsage, name) {
			t.Errorf("SwarmUsage must document %s:\n%s", name, SwarmUsage)
		}
	}

	var errw strings.Builder
	if _, code := ParseSwarmFlags([]string{"--nope"}, &errw); code != 2 {
		t.Errorf("unknown flag exit = %d; want 2", code)
	}
	if !strings.Contains(errw.String(), "Usage: agy pro swarm") {
		t.Errorf("unknown flag must print the usage text, got %q", errw.String())
	}
	errw.Reset()
	if _, code := ParseSwarmFlags([]string{"extra-arg"}, &errw); code != 2 {
		t.Errorf("positional argument exit = %d; want 2", code)
	}
	// No flags at all: the interactive path (F-B).
	f, code := ParseSwarmFlags(nil, &errw)
	if code != 0 {
		t.Fatalf("no-args exit = %d; want 0", code)
	}
	if f.NonInteractive() {
		t.Error("no flags must stay on the interactive path")
	}
}
