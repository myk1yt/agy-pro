package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/integrations"
)

// FIX-12 / F-S9F-1: the integrations transcript is a security surface (it reports
// F-V1 posture), and two of the strings it prints are read out of the user-editable
// live config. providers.MaskOperand masks credentials, not control characters, so a
// crafted hooks.json command value or settings.json field name could carry ESC and
// steer the operator's screen. The strip lives at the PRINT sinks (transcript.go).

// TestStripTerminalControl pins the neutralisation rule itself: Cc controls and Cf
// format characters are dropped, tab survives, printable text is untouched.
func TestStripTerminalControl(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"CSI clear screen", "\x1b[2J\x1b[Hpinned", "[2J[Hpinned"},
		{"cursor up overwrite", "clean\x1b[3Aforged", "clean[3Aforged"},
		{"OSC-8 hyperlink", "\x1b]8;;http://x\x07link\x1b]8;;\x07", "]8;;http://xlink]8;;"},
		{"bidi override (Cf)", "safe\u202Eevil", "safeevil"},
		{"zero width space (Cf)", "ag\u200By-tokens", "agy-tokens"},
		{"newline and carriage return", "one\ntwo\rthree", "onetwothree"},
		{"DEL", "keep\x7fme", "keepme"},
		{"tab survives", "col1\tcol2", "col1\tcol2"},
		{"clean text unchanged", "a normal Detail sentence (0x1b free)", "a normal Detail sentence (0x1b free)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripTerminalControl(tc.in)
			if got != tc.want {
				t.Fatalf("stripTerminalControl(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.ContainsRune(got, 0x1b) {
				t.Fatalf("stripTerminalControl(%q) kept an ESC byte: %q", tc.in, got)
			}
		})
	}
}

// TestIntegrationsStatusNeutralisesALiveHookCommandEscape is the finding's own PoC
// as a CLI pin: a live hooks.json (user-editable) whose command value is an absolute
// launcher path carrying ESC bytes. hookLiveCommandPosture reads that value, finds
// the launcher gone, and reports it in the destination Detail. The engine's string
// still carries the raw ESC (first half, so the test is not vacuous); the printed
// status transcript must not.
func TestIntegrationsStatusNeutralisesALiveHookCommandEscape(t *testing.T) {
	sandboxIntegrations(t)
	installStatusLineLauncher(t)
	if code, _, errw := runIntegrations(t, "apply", "hook", "--yes"); code != 0 {
		t.Fatalf("apply hook exit = %d, stderr:\n%s", code, errw)
	}
	deployed := filepath.Join(os.Getenv("GEMINI_CONFIG_DIR"), "hooks", "hooks.json")

	candidates, err := integrations.StatusLineLauncherCandidates()
	if err != nil || len(candidates) == 0 {
		t.Fatalf("StatusLineLauncherCandidates = %v (%v)", candidates, err)
	}
	launcher := candidates[0]
	// Same launcher BASENAME (so the value is a launcher this engine claims), in a
	// directory that does not exist, with the spoofing rune inside the path.
	//
	// Escalation spelling: Win32 rejects a file name carrying a C0 control character,
	// so os.Stat on an ESC-bearing path returns "invalid name" (not IsNotExist) and the
	// engine stays quiet: the ESC spelling of this PoC is reachable on a POSIX host
	// only, where a file name may legally carry 0x1b. The Cf spelling (a
	// right-to-left override) is a legal name on both hosts and spoofs a rendered line
	// the same way, so it is the arm this pin drives on Windows. The ESC byte is
	// pinned at the same sinks by TestStripTerminalControl and
	// TestConsentGateNeutralisesDiffRows.
	marker, stripped := "\x1b[2J\x1b[H", "[2J[H"
	if runtime.GOOS == "windows" {
		marker, stripped = "\u202E", ""
	}
	hostile := filepath.Join(filepath.Dir(launcher), "held"+marker+"dead", filepath.Base(launcher))

	raw, err := os.ReadFile(deployed)
	if err != nil {
		t.Fatalf("read the deployed hook: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode the deployed hook: %v", err)
	}
	hooks, ok := document["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("the deployed hook carries no hooks object: %s", raw)
	}
	post, ok := hooks["PostInvocation"].(map[string]any)
	if !ok {
		t.Fatalf("the deployed hook carries no PostInvocation object: %s", raw)
	}
	post["command"] = hostile
	edited, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("encode the hostile hook: %v", err)
	}
	edited = append(edited, '\n')
	if err := os.WriteFile(deployed, edited, 0o644); err != nil {
		t.Fatalf("write the hostile hook: %v", err)
	}
	// Record the edited bytes as the deployed digest, so the destination reads CLEAN
	// and the advisory branch (not the drift branch) is the one that speaks.
	manifest, err := integrations.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	entry, found := manifest.Entry(deployed)
	if !found {
		t.Fatalf("the manifest does not own %q", deployed)
	}
	entry.SHA256 = integrations.SHA256Hex(edited)
	if err := manifest.Set(entry); err != nil {
		t.Fatalf("manifest.Set: %v", err)
	}
	if err := integrations.SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	// First half: the engine's Detail carries the raw escape (the source is the live
	// config, and MaskOperand is not a control-character boundary).
	status, err := integrations.Status(integrations.FeatureHook)
	if err != nil {
		t.Fatalf("Status(hook): %v", err)
	}
	if len(status.Destinations) != 1 {
		t.Fatalf("hook destinations = %+v, want one", status.Destinations)
	}
	detail := status.Destinations[0].Detail
	if !strings.Contains(detail, marker) {
		t.Fatalf("the engine's Detail lost the spoofing rune (test would be vacuous): %q (state=%s hostile=%q)",
			detail, status.Destinations[0].State, hostile)
	}

	// Second half: the transcript prints the stripped form.
	code, out, errw := runIntegrations(t, "status")
	if code != 0 {
		t.Fatalf("status exit = %d, stderr:\n%s", code, errw)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("the status transcript carries a raw ESC byte:\n%q", out)
	}
	if strings.Contains(out, marker) {
		t.Fatalf("the status transcript carries the raw spoofing rune %q:\n%q", marker, out)
	}
	if !strings.Contains(out, "which is not on disk") {
		t.Fatalf("the status transcript never printed the advisory (test would be vacuous):\n%s", out)
	}
	if !strings.Contains(out, "held"+stripped+"dead") {
		t.Fatalf("the stripped path is not in the transcript (the strip dropped more than the controls):\n%s", out)
	}
}

// TestIntegrationsStatusNeutralisesTheLedgerFieldName covers the settings-door arm of
// F-S9F-1: a settings.json whose statusLine object carries an undeclared field with an
// ESC sequence in its NAME. Measured fact: Go's json decoder renders an unknown field
// name through its own quoting, so this arm already reaches the transcript as the four
// printable characters "\x1b" rather than as a raw escape. The pin records that
// (a quote-escaped name is what the ledger produces, so this arm is not the live
// injection path) and asserts the sink still refuses to let a raw escape through when
// one arrives from any other source.
func TestIntegrationsStatusNeutralisesTheLedgerFieldName(t *testing.T) {
	sandboxIntegrations(t)
	settingsPath, err := integrationSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	// The hostile field name, written as a JSON escape so the document stays valid.
	document := "{\n" +
		"  \"statusLine\": {\n" +
		"    \"type\": \"command\",\n" +
		"    \"command\": \"agy-tokens --hook\",\n" +
		"    \"enabled\": true,\n" +
		"    \"forge\\u001b[2J\\u001b[Hd\": \"x\"\n" +
		"  }\n" +
		"}\n"
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatalf("mkdir the settings dir: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write the hostile settings.json: %v", err)
	}

	planErr := func() error { _, err := integrations.StatusLinePlan(""); return err }()
	if planErr == nil {
		t.Fatal("the ledger accepted a statusLine object with an undeclared field")
	}
	if strings.ContainsRune(planErr.Error(), 0x1b) {
		t.Fatalf("the ledger emitted a RAW escape (the transcript pin below is now load-bearing): %q", planErr.Error())
	}
	if !strings.Contains(planErr.Error(), `\x1b`) {
		t.Fatalf("the ledger's foreign-field error does not name the field in its escaped spelling: %q", planErr.Error())
	}

	code, out, errw := runIntegrations(t, "status")
	if code != 0 {
		t.Fatalf("status exit = %d, stderr:\n%s", code, errw)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("the status transcript carries a raw ESC byte:\n%q", out)
	}
	if !strings.Contains(out, "repair decision needed") {
		t.Fatalf("the status transcript never reached the door row (test would be vacuous):\n%s", out)
	}
}

// TestConsentGateNeutralisesDiffRows pins the shared consent transcript: every
// writing verb's diff passes confirmPersistedWrite, including the composed
// integrations rows that can embed a live-config path or the ledger's field name.
func TestConsentGateNeutralisesDiffRows(t *testing.T) {
	var out, errw bytes.Buffer
	diff := []string{
		"step hook: command C:\\Users\\me\\agy-tokens\u001b[2J\u001b[H --hook",
		"note settings: the document names the field forge\u001b[Kd",
	}
	proceed, code := confirmPersistedWrite(strings.NewReader(""), &out, &errw,
		"[agy-pro] Approve Test Write", diff, "the test store", "a test write", true)
	if !proceed || code != 0 {
		t.Fatalf("confirmPersistedWrite = (%v, %d), want (true, 0) with --yes", proceed, code)
	}
	transcript := out.String()
	if strings.ContainsRune(transcript, 0x1b) {
		t.Fatalf("the consent transcript carries a raw ESC byte:\n%q", transcript)
	}
	if !strings.Contains(transcript, "[2J[H") || !strings.Contains(transcript, "[K") {
		t.Fatalf("the consent transcript lost the stripped rows (test would be vacuous):\n%s", transcript)
	}
}

// TestIntegrationsErrorRowsNeutraliseLiveConfigText pins the door's error printer:
// an explicit-door apply on a foreign settings.json fails closed with the ledger's
// sentence, which names the offending field out of the live document.
func TestIntegrationsErrorRowsNeutraliseLiveConfigText(t *testing.T) {
	sandboxIntegrations(t)
	settingsPath, err := integrationSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	document := "{\n  \"statusLine\": {\n    \"type\": \"command\",\n" +
		"    \"command\": \"agy-tokens --hook\",\n    \"forge\\u001b[2Jd\": \"x\"\n  }\n}\n"
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatalf("mkdir the settings dir: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write the hostile settings.json: %v", err)
	}
	code, out, errw := runIntegrations(t, "apply", "statusline", "--yes")
	if code == 0 {
		t.Fatalf("apply statusline accepted a foreign settings.json:\n%s", out)
	}
	if strings.ContainsRune(errw, 0x1b) || strings.ContainsRune(out, 0x1b) {
		t.Fatalf("the refusal carries a raw ESC byte:\nstdout=%q\nstderr=%q", out, errw)
	}
}
