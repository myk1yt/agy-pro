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
	root := filepath.Join("..", "..", "..", "plugins", "agypro_settings")

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
	text := string(body)
	if !strings.HasPrefix(text, "---\n") {
		t.Error("command body missing frontmatter")
	}

	// Honesty label: must identify as a plugin command.
	if !strings.Contains(text, "PLUGIN slash command") && !strings.Contains(text, "plugin slash command") {
		t.Error("command body lacks the plugin-command honesty label")
	}

	// F-PLUG: never claim to contain/launch/embed the TUI.
	tuiClaims := []string{
		"launches the TUI",
		"launch the TUI",
		"opens the full-screen",
		"open the full-screen",
		"contains the TUI",
		"embedded panel",
		"renders the panel",
		"native Settings Editor overlay",
		"shows the Settings Editor",
	}
	lower := strings.ToLower(text)
	for _, c := range tuiClaims {
		if strings.Contains(lower, strings.ToLower(c)) {
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
