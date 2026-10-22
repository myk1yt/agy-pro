package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginAssetLint (contract §2.9 U6 tests, F-PLUG): the M1 plugin assets
// must validate against the metadata-only manifest schema, register the slash
// command via the FILE name (research E3), stay labeled as a plugin command,
// and never claim to contain or launch the M2 TUI.
func TestPluginAssetLint(t *testing.T) {
	root := siblingPluginRoot(t, "agypro_settings")

	// --- plugin.json: metadata only, no command/hook fields. ---
	manifestPath := filepath.Join(root, "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("plugin.json invalid JSON: %v", err)
	}
	if _, ok := fields["name"]; !ok {
		t.Error("plugin.json missing required name")
	}
	allowed := map[string]bool{
		"name": true, "description": true, "version": true, "author": true,
		"license": true, "homepage": true, "repository": true, "keywords": true,
		"displayName": true,
	}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("plugin.json has non-metadata field %q (metadata-only schema)", k)
		}
	}
	for _, banned := range []string{"commands", "command", "slashCommands", "hooks", "mcpServers"} {
		if _, ok := fields[banned]; ok {
			t.Errorf("plugin.json must not declare %q (commands live as FILES)", banned)
		}
	}
	var meta struct{ Name, Description string }
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("plugin.json metadata decode: %v", err)
	}
	if strings.TrimSpace(meta.Description) == "" {
		t.Error("plugin.json missing description")
	}

	// --- commands/setting_agypro.md: file name = slash name (research E3). ---
	cmdPath := filepath.Join(root, "commands", "setting_agypro.md")
	body, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatalf("read command body: %v", err)
	}
	text := normalizeEOL(body)
	if !strings.HasPrefix(text, "---\n") {
		t.Error("command body missing frontmatter")
	}

	// Honesty label: must identify as a plugin command.
	if !strings.Contains(text, "PLUGIN slash command") && !strings.Contains(text, "plugin slash command") {
		t.Error("command body lacks the plugin-command honesty label")
	}

	lower := strings.ToLower(text)
	for _, c := range tuiClaimPhrases {
		if strings.Contains(lower, c) {
			t.Errorf("command body claims TUI capability (%q) — F-PLUG violation", c)
		}
	}

	// Must point at the user-launched M2 TUI explicitly.
	if !strings.Contains(text, "`agy pro settings`") {
		t.Error("command body missing the explicit `agy pro settings` pointer")
	}
	if !strings.Contains(text, "in your own terminal") {
		t.Error("command body missing the user-launched TUI hint (loop footer)")
	}

	// Must drive verbs, not free-form flags.
	for _, verb := range []string{
		"agy pro settings show",
		"agy pro settings set",
		"agy pro settings providers",
		"agy pro settings mode",
		"agy pro settings doctor",
	} {
		if !strings.Contains(text, verb) {
			t.Errorf("command body missing verb %q", verb)
		}
	}

	// Numbered selection is the single choice language.
	if !strings.Contains(text, "[1]") || !strings.Contains(text, "Select [0-7]") {
		t.Error("command body missing the numbered menu protocol")
	}

	// U8 redaction rules present; no key-shaped literals in the body.
	if !strings.Contains(text, "first-5") && !strings.Contains(text, "first 5") {
		t.Error("command body missing the first-5 masking rule")
	}
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '`' || r == '"' || r == '\'' || r == ',' || r == '.'
	}) {
		if looksLikePlantedKey(w) {
			t.Errorf("command body contains a key-shaped literal %q", w)
		}
	}

	// --- skills/setting_agypro/SKILL.md: same-name skill exists. ---
	skillPath := filepath.Join(root, "skills", "setting_agypro", "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(skill), "name: setting_agypro") {
		t.Error("SKILL.md frontmatter name must be setting_agypro")
	}
	if !strings.Contains(string(skill), "plugin") {
		t.Error("SKILL.md must label the surface as a plugin skill")
	}
}

// normalizeEOL folds CRLF to LF. agy checks tracked *.md out as CRLF
// (.gitattributes `*.md text` + core.autocrlf=true), so every frontmatter and
// prefix assertion in this file must compare against the normalized text.
func normalizeEOL(b []byte) string {
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// siblingPluginRoot resolves the repo-level plugins/<name> tree that sits three
// directories above this package. A standalone agy-pro checkout (cloned or
// downloaded without its parent workspace) carries no sibling plugin trees, so
// there is no artifact to assert against and the lint SKIPS with the missing
// path in the message instead of failing the whole suite. When the tree IS
// present - always the case in the full repo - the caller runs its assertions
// unchanged: this helper only decides whether the subject exists, it never
// weakens a check.
func siblingPluginRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join("..", "..", "..", "plugins", name)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Skipf("plugins/%s not found at %q: a standalone agy-pro checkout ships no sibling plugin trees, so this lint has no target to assert (not a product failure)", name, root)
	}
	return root
}

// tuiClaimPhrases is the single shared F-PLUG denial list: an agent-consumed
// plugin command body must never claim to contain, launch, or render a TUI.
var tuiClaimPhrases = []string{
	"launches the tui",
	"launch the tui",
	"opens the full-screen",
	"open the full-screen",
	"contains the tui",
	"embedded panel",
	"renders the panel",
	"native settings editor overlay",
	"shows the settings editor",
}

// looksLikePlantedKey is a conservative static check for accidentally pasted
// credentials in agent-consumed files.
func looksLikePlantedKey(w string) bool {
	if len(w) < 16 {
		return false
	}
	lower := strings.ToLower(w)
	for _, p := range []string{"sk-", "gsk_", "nvapi-", "key-", "live_", "secret_"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// TestPluginBodyShellSafeInterpolation (B5): the plugin body must mandate
// quoted tokens on every run_command line and prefer the stdin add-json shape,
// so a user-supplied provider name can never be spliced raw into a shell line
// (indirect prompt injection / confused deputy).
func TestPluginBodyShellSafeInterpolation(t *testing.T) {
	root := siblingPluginRoot(t, "agypro_settings")
	body, err := os.ReadFile(filepath.Join(root, "commands", "setting_agypro.md"))
	if err != nil {
		t.Fatalf("read command body: %v", err)
	}
	text := normalizeEOL(body)

	for _, want := range []string{
		"Shell-safe interpolation",
		"quoted",
		"add-json -",
		"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$",
		"never splice a user-supplied name raw",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("command body missing shell-safe rule element %q", want)
		}
	}
	// The name-taking verbs must show quoted tokens.
	for _, want := range []string{
		`providers test "<name>"`,
		`providers rename "<old>" "<new>"`,
		`providers delete "<name>"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("command body missing quoted-verb form %q", want)
		}
	}
}

// TestSwarmPluginAssetLint: the agypro_swarm plugin (the in-TUI swarm route)
// must follow the same contract shape as agypro_settings - metadata-only
// manifest, plugin-command honesty label, numbered-only selection, U8 masking,
// shell-safe interpolation - plus the swarm-specific binding rules: the XOR
// boundary is stated plainly, cost confirmation is mandatory before any paid
// run, and dry-run comes first.
func TestSwarmPluginAssetLint(t *testing.T) {
	root := siblingPluginRoot(t, "agypro_swarm")

	manifestPath := filepath.Join(root, "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("plugin.json invalid JSON: %v", err)
	}
	if _, ok := fields["name"]; !ok {
		t.Error("plugin.json missing required name")
	}
	allowed := map[string]bool{
		"name": true, "description": true, "version": true, "author": true,
		"license": true, "homepage": true, "repository": true, "keywords": true,
		"displayName": true,
	}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("plugin.json has non-metadata field %q (metadata-only schema)", k)
		}
	}

	cmdPath := filepath.Join(root, "commands", "swarm.md")
	body, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatalf("read command body: %v", err)
	}
	text := normalizeEOL(body)
	if !strings.HasPrefix(text, "---\n") {
		t.Error("command body missing frontmatter")
	}
	if !strings.Contains(text, "plugin slash command") && !strings.Contains(text, "PLUGIN slash command") {
		t.Error("command body lacks the plugin-command honesty label")
	}
	lower := strings.ToLower(text)
	for _, c := range tuiClaimPhrases {
		if strings.Contains(lower, c) {
			t.Errorf("command body claims TUI capability (%q)", c)
		}
	}

	// Swarm-specific binding rules.
	for _, want := range []string{
		"XOR boundary",
		"Cost confirmation is mandatory",
		"Dry-run first by default",
		"--dry-run",
		"--prompt-file",
		"agy pro swarm --template",
		"agy pro fanout",
		"agy pro models --all",
		"summary.json",
		"reconciliation.md",
		"run_command",
		"in your own terminal",
		"first-5",
		"never splice a user-supplied string raw",
		"^[A-Za-z0-9][A-Za-z0-9._/:,-]*$",
		"untrusted workspace and model output",
		"agy plugin install",
		"owner-only",
		"Delete the temp file",
		"information only",
		"NEVER execute or paste",
		"confined to the current",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("command body missing %q", want)
		}
	}

	// Numbered selection is the single choice language.
	if !strings.Contains(text, "[1]") || !strings.Contains(text, "[0] Exit") {
		t.Error("command body missing the numbered menu protocol")
	}

	// No key-shaped literals in the body.
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '`' || r == '"' || r == '\'' || r == ',' || r == '.'
	}) {
		if looksLikePlantedKey(w) {
			t.Errorf("command body contains a key-shaped literal %q", w)
		}
	}

	// The skills/swarm/SKILL.md mirror is what makes the command discoverable
	// in the slash palette on this agy build; the lint must pin its existence.
	skillPath := filepath.Join(root, "skills", "swarm", "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read SKILL.md mirror: %v", err)
	}
	skillText := normalizeEOL(skill)
	if !strings.Contains(skillText, "name: swarm") {
		t.Error("SKILL.md frontmatter name must be swarm")
	}
	if !strings.Contains(skillText, "commands/swarm.md") {
		t.Error("SKILL.md must point at the authoritative commands/swarm.md body")
	}
	if !strings.Contains(skillText, "plugin") {
		t.Error("SKILL.md must label the surface as a plugin skill")
	}
}
