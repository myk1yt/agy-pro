package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agypro/internal/agysettings"
	"agypro/internal/integrations"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/profiles"
)

// sandboxIntegrations points every path this surface touches at one temp tree and
// returns the fake home. os.UserHomeDir honors USERPROFILE on Windows and HOME
// elsewhere, so both are set (the discipline internal/integrations' helper uses);
// the agy-pro home and the Gemini config dir are redirected too, so the real
// ~/.agy-pro and ~/.gemini are never read or written.
func sandboxIntegrations(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv(paths.EnvAgyProHome, filepath.Join(home, "agy-pro"))
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(home, "gemini-config"))
	return home
}

// installStatusLineLauncher creates the wrapper the settings door refuses to bake
// a command without. The door checks the OS ladder, and the name it looks for is
// per-OS, so the fixture is built from the engine's own candidate list.
func installStatusLineLauncher(t *testing.T) {
	t.Helper()
	candidates, err := integrations.StatusLineLauncherCandidates()
	if err != nil {
		t.Fatalf("StatusLineLauncherCandidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("no launcher candidate on this host")
	}
	target := candidates[0]
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir launcher dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatalf("write launcher: %v", err)
	}
}

// runIntegrations drives the verb with a pipe stdin (not a console) and returns
// the exit code plus both streams.
func runIntegrations(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := runIntegrationsCmd(args, strings.NewReader(""), &out, &errw)
	return code, out.String(), errw.String()
}

// runIntegrationsConsole drives the verb as a console host would: stdinIsTTY is
// stubbed true and the answer line is the caller's. It is the seam the P5-1 gate
// tests use, because headless discipline forbids attaching a real console.
func runIntegrationsConsole(t *testing.T, answer string, args ...string) (int, string, string) {
	t.Helper()
	stubStdinIsTTY(t, true)
	var out, errw bytes.Buffer
	code := runIntegrationsCmd(args, strings.NewReader(answer), &out, &errw)
	return code, out.String(), errw.String()
}

// confConfigDir resolves the live config dir inside the sandbox.
func confConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := paths.GeminiConfigDir()
	if err != nil {
		t.Fatalf("GeminiConfigDir: %v", err)
	}
	return dir
}

// stateRow reads one persisted state.json row.
func stateRow(t *testing.T, read func(*mode.State) string) string {
	t.Helper()
	st, err := mode.LoadState()
	if err != nil || st == nil {
		t.Fatalf("LoadState: %v", err)
	}
	return read(st)
}

// TestIntegrationsStatusReportsIntentAndEngine pins the read-only surface: one
// line per feature carrying the state.json intent and the engine's own verdict,
// plus the preset row, and the sentence that keeps "unset" and "off" apart.
func TestIntegrationsStatusReportsIntentAndEngine(t *testing.T) {
	sandboxIntegrations(t)
	code, out, errw := runIntegrations(t, "apply", "rules", "--yes")
	if code != 0 {
		t.Fatalf("apply rules exit = %d, stderr:\n%s", code, errw)
	}
	// An explicit off on a second feature, so both postures render side by side.
	if err := mode.SaveState(&mode.State{
		CurrentMode:       "clean",
		IntegrationRules:  mode.IntegrationToggleOn,
		IntegrationSkills: mode.IntegrationToggleOff,
		IntegrationPreset: "custom",
		ManagedItems:      append([]string(nil), mode.DefaultManagedItems...),
		UpdatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	code, out, errw = runIntegrations(t, "status")
	if code != 0 {
		t.Fatalf("status exit = %d, stderr:\n%s", code, errw)
	}
	for _, want := range []string{
		"feature", "intent", "engine",
		"rules", "on", "clean",
		"skills", "off", "not deployed",
		"statusline", "unset",
		"preset", "custom",
		"unset intent is the legacy posture",
		"It is not the same as off",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q; got:\n%s", want, out)
		}
	}
	// A read-only verb never prints a consent banner.
	if strings.Contains(out, "Approve Integrations Write") {
		t.Fatalf("status asked for consent; it must stay read-only:\n%s", out)
	}
}

// TestIntegrationsApplyPipeRefusalNamesYes pins the gate contract on a pipe: the
// diff prints, the refusal names --yes, exit 1, and nothing is written (no config
// tree, no manifest, no state row).
func TestIntegrationsApplyPipeRefusalNamesYes(t *testing.T) {
	sandboxIntegrations(t)
	code, out, errw := runIntegrations(t, "apply", "rules")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "[agy-pro] Approve Integrations Write") {
		t.Fatalf("the exact diff must print before the refusal; got:\n%s", out)
	}
	if !strings.Contains(out, "create") {
		t.Fatalf("the diff must name the destinations to create; got:\n%s", out)
	}
	if !strings.Contains(errw, "--yes") {
		t.Fatalf("refusal must name --yes; got:\n%s", errw)
	}
	if _, err := os.Stat(filepath.Join(confConfigDir(t), "rules")); !os.IsNotExist(err) {
		t.Fatalf("a refused apply created the destination tree (err = %v)", err)
	}
	if data, readErr := os.ReadFile(statePathForTest(t)); readErr == nil && strings.Contains(string(data), "integration_rules") {
		t.Fatalf("a refused apply wrote the intent row:\n%s", data)
	}
}

// TestIntegrationsApplyYesDeploysFromSnapshot pins the whole deploy path: the
// files come from the embedded snapshot, the manifest records them as
// go-applied, and the intent rows land in state.json.
func TestIntegrationsApplyYesDeploysFromSnapshot(t *testing.T) {
	sandboxIntegrations(t)
	code, out, errw := runIntegrations(t, "apply", "rules", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] rules deployed") {
		t.Fatalf("missing success line; got:\n%s", out)
	}
	snapshot, err := fs.ReadFile(profiles.GetToolsFS(), "rules/AGENTS.md")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	live, err := os.ReadFile(filepath.Join(confConfigDir(t), "rules", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read deployed file: %v", err)
	}
	if !bytes.Equal(live, snapshot) {
		t.Fatal("the deployed bytes are not the embedded snapshot's bytes")
	}
	manifest, err := integrations.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	entry, ok := manifest.Entry(filepath.Join(confConfigDir(t), "rules", "AGENTS.md"))
	if !ok {
		t.Fatal("the manifest does not record the deployed destination")
	}
	if entry.Provenance != integrations.ProvenanceGoApplied {
		t.Fatalf("provenance = %q, want go-applied", entry.Provenance)
	}
	if got := stateRow(t, func(s *mode.State) string { return s.IntegrationRules }); got != mode.IntegrationToggleOn {
		t.Fatalf("integration_rules = %q, want on", got)
	}
	if got := stateRow(t, func(s *mode.State) string { return s.IntegrationPreset }); got != "custom" {
		t.Fatalf("integration_preset = %q, want custom (one feature on)", got)
	}
}

// TestIntegrationsPresetToolsUsesOneConsent is the composition pin: a preset apply
// touches the settings door, four content features and six intent rows, and the
// operator answers exactly ONE prompt. A second gate anywhere would show a second
// banner or a second [y/N] prompt.
func TestIntegrationsPresetToolsUsesOneConsent(t *testing.T) {
	sandboxIntegrations(t)
	installStatusLineLauncher(t)
	code, out, errw := runIntegrationsConsole(t, "y\n", "apply", "tools")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, errw)
	}
	if n := strings.Count(out, "[agy-pro] Approve Integrations Write"); n != 1 {
		t.Fatalf("consent banners = %d, want exactly 1; output:\n%s", n, out)
	}
	if n := strings.Count(out, "[y/N]"); n != 1 {
		t.Fatalf("prompts = %d, want exactly 1; output:\n%s", n, out)
	}
	// Every feature deployed, every intent row written, one prompt.
	for _, feature := range integrations.AllFeatures() {
		if feature == integrations.FeatureStatusline {
			continue
		}
		if got := stateRow(t, func(s *mode.State) string { return integrationRowForTest(s, feature) }); got != mode.IntegrationToggleOn {
			t.Fatalf("intent for %s = %q, want on", feature, got)
		}
	}
	if got := stateRow(t, func(s *mode.State) string { return s.IntegrationPreset }); got != "tools" {
		t.Fatalf("integration_preset = %q, want tools", got)
	}
	if _, err := os.Stat(filepath.Join(confConfigDir(t), "skills")); err != nil {
		t.Fatalf("skills tree not deployed: %v", err)
	}
	// The summary of the state rows appears inside the one diff, so the approved
	// diff is the whole write, not just the file half.
	for _, want := range []string{
		"integration_preset: (unset) -> tools",
		"integration_rules: (unset) -> on",
		"state.json will be written (0600, atomic)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the composed diff is missing %q; output:\n%s", want, out)
		}
	}
}

// TestIntegrationsPresetApplyDegradesWithoutLauncher is the T8 §2 rule 5 pin this
// surface was missing (F-S9C-02). A whole-posture apply that meets a machine with
// no agy-tokens launcher must SUBTRACT the statusline step - a note in the same
// diff names the reason - and deploy the four content features anyway. Before this
// pin the preset composed as an explicit target, so the missing launcher aborted
// the whole plan: install.bat's npm-link success branch creates no launcher at all,
// so the common install path exited 1 having written nothing.
//
// The intent rows are the composer's own facts (TestFeatureWriteComposesIntentFacts
// pins the same five rows plus the preset for this request shape), not a second
// decision taken here: the pending apply never lands in a state where statusline
// intent is anything but on either, because the operand-less set is built from
// features whose intent already reads on.
//
// FIX-9 (F-R2-02) pins the note's POSITION too: it leads the approved diff. The
// transcript and the TUI modal render the same composed lines, so a note that
// trails a 100+ line deploy diff is a note the modal's height budget can drop.
func TestIntegrationsPresetApplyDegradesWithoutLauncher(t *testing.T) {
	sandboxIntegrations(t)
	code, out, errw := runIntegrations(t, "apply", "tools", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (a whole-posture apply degrades); stderr:\n%s", code, errw)
	}
	// Exactly one note, carrying the launcher reason inside the approved diff.
	notes := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "  note ") {
			continue
		}
		notes++
		if !strings.Contains(line, "statusline skipped") || !strings.Contains(line, "agy-tokens") {
			t.Fatalf("the note does not name the skipped statusline and the launcher: %q", line)
		}
	}
	if notes != 1 {
		t.Fatalf("notes = %d, want exactly 1; output:\n%s", notes, out)
	}
	// The note leads the diff: a step line is printed with its feature name at
	// column 0 (continuation rows and intent rows are indented past it), so the
	// first two-space line that is not a note is the first step.
	noteLine, firstStep := -1, -1
	for i, line := range strings.Split(out, "\n") {
		switch {
		case noteLine < 0 && strings.HasPrefix(line, "  note "):
			noteLine = i
		case firstStep < 0 && strings.HasPrefix(line, "  ") &&
			!strings.HasPrefix(line, "   ") && !strings.HasPrefix(line, "  note "):
			firstStep = i
		}
	}
	if noteLine < 0 || firstStep < 0 || noteLine > firstStep {
		t.Fatalf("the diff does not lead with the note (note at line %d, first step at line %d); output:\n%s",
			noteLine, firstStep, out)
	}
	// The door step was subtracted, not written: no settings.json exists.
	settingsPath, pathErr := integrationSettingsPath()
	if pathErr != nil {
		t.Fatalf("GlobalSettingsPath: %v", pathErr)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("the degraded apply wrote settings.json (err = %v)", err)
	}
	// The four content features deployed, and the manifest claims destinations
	// under their layouts only (no settings.json, no statusline half).
	var contentDirs []string
	for _, feature := range integrations.AllFeatures() {
		if feature == integrations.FeatureStatusline {
			continue
		}
		layout, err := integrations.LayoutFor(feature)
		if err != nil {
			t.Fatalf("LayoutFor(%s): %v", feature, err)
		}
		contentDirs = append(contentDirs, layout.DestDir)
		if _, err := os.Stat(layout.DestDir); err != nil {
			t.Fatalf("%s was not deployed by the degraded apply: %v", feature, err)
		}
	}
	manifest, err := integrations.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if manifest.Len() == 0 {
		t.Fatal("the degraded apply recorded no destination")
	}
	for _, path := range manifest.Paths() {
		inside := false
		for _, dir := range contentDirs {
			if rel, relErr := filepath.Rel(dir, path); relErr == nil && !strings.HasPrefix(rel, "..") {
				inside = true
				break
			}
		}
		if !inside {
			t.Fatalf("the manifest records %q, which is not a content destination", path)
		}
	}
	// The posture rows the same consent covers: four content features and the
	// preset, with the statusline row the composer's facts carry.
	for _, feature := range integrations.AllFeatures() {
		if got := stateRow(t, func(s *mode.State) string { return integrationRowForTest(s, feature) }); got != mode.IntegrationToggleOn {
			t.Fatalf("intent for %s = %q, want on", feature, got)
		}
	}
	if got := stateRow(t, func(s *mode.State) string { return s.IntegrationPreset }); got != "tools" {
		t.Fatalf("integration_preset = %q, want tools", got)
	}
}

// TestIntegrationsRetractRestoresCleanState pins the removal half: the recorded
// destinations leave the config tree (into a backup), the manifest forgets them,
// and the intent row records the explicit off. A second retract is stable.
func TestIntegrationsRetractRestoresCleanState(t *testing.T) {
	sandboxIntegrations(t)
	if code, _, errw := runIntegrations(t, "apply", "rules", "--yes"); code != 0 {
		t.Fatalf("apply exit = %d, stderr:\n%s", code, errw)
	}
	code, out, errw := runIntegrations(t, "retract", "rules", "--yes")
	if code != 0 {
		t.Fatalf("retract exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] rules retracted") {
		t.Fatalf("missing retract success line; got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(confConfigDir(t), "rules", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("the deployed file survived the retract (err = %v)", err)
	}
	manifest, err := integrations.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got := manifest.Len(); got != 0 {
		t.Fatalf("manifest entries = %d, want 0", got)
	}
	if got := stateRow(t, func(s *mode.State) string { return s.IntegrationRules }); got != mode.IntegrationToggleOff {
		t.Fatalf("integration_rules = %q, want off", got)
	}
	// Stable: a second retract writes no new intent row and does not fail.
	code, out, errw = runIntegrations(t, "retract", "rules", "--yes")
	if code != 0 {
		t.Fatalf("second retract exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "Nothing to write") {
		t.Fatalf("the second retract presented a write; got:\n%s", out)
	}
}

// TestIntegrationsRollbackWithoutSnapshotIsNamedEmptyState pins the no-backup
// branch: a named empty state on stdout, exit 0, no consent gate, and no engine
// call (nothing is created beside a settings file that does not exist).
func TestIntegrationsRollbackWithoutSnapshotIsNamedEmptyState(t *testing.T) {
	sandboxIntegrations(t)
	code, out, errw := runIntegrations(t, "rollback")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, errw)
	}
	for _, want := range []string{"No settings.json snapshot to roll back to", "Nothing was written"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rollback output missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Approve Integrations Write") {
		t.Fatalf("an empty rollback asked for consent:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(confConfigDir(t), "integrations.json")); !os.IsNotExist(err) {
		t.Fatalf("rollback created a manifest (err = %v)", err)
	}
}

// TestIntegrationsRepairReconcilesDrift pins the repair verb: a destination that
// drifted is re-planned and re-applied, a second run finds nothing to repair, and
// the verb never invents a feature that has nothing recorded.
func TestIntegrationsRepairReconcilesDrift(t *testing.T) {
	sandboxIntegrations(t)
	if code, _, errw := runIntegrations(t, "apply", "rules", "--yes"); code != 0 {
		t.Fatalf("apply exit = %d, stderr:\n%s", code, errw)
	}
	target := filepath.Join(confConfigDir(t), "rules", "AGENTS.md")
	if err := os.WriteFile(target, []byte("hand-edited\n"), 0o600); err != nil {
		t.Fatalf("drift the destination: %v", err)
	}
	code, out, errw := runIntegrations(t, "repair", "--yes")
	if code != 0 {
		t.Fatalf("repair exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "1 drifted") {
		t.Fatalf("repair diff does not name the drift; got:\n%s", out)
	}
	snapshot, err := fs.ReadFile(profiles.GetToolsFS(), "rules/AGENTS.md")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	live, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read repaired file: %v", err)
	}
	if !bytes.Equal(live, snapshot) {
		t.Fatal("repair did not restore the engine's bytes")
	}
	code, out, errw = runIntegrations(t, "repair", "--yes")
	if code != 0 {
		t.Fatalf("second repair exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "Nothing to repair") {
		t.Fatalf("a clean tree still presented a repair write; got:\n%s", out)
	}
}

// TestIntegrationsAdoptRecordsWithoutWriting pins the record-only adopt: a
// destination that already holds the engine's bytes is recorded as adopted and its
// bytes are untouched.
func TestIntegrationsAdoptRecordsWithoutWriting(t *testing.T) {
	sandboxIntegrations(t)
	// Plant the engine's own bytes by hand, so no engine write happened first.
	sfs := profiles.GetToolsFS()
	err := fs.WalkDir(sfs, "rules", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := fs.ReadFile(sfs, path)
		if readErr != nil {
			return readErr
		}
		rel := strings.TrimPrefix(path, "rules/")
		dest := filepath.Join(confConfigDir(t), "rules", filepath.FromSlash(rel))
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(dest, data, 0o600)
	})
	if err != nil {
		t.Fatalf("plant snapshot bytes: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(confConfigDir(t), "rules", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read planted file: %v", err)
	}
	code, out, errw := runIntegrations(t, "adopt", "--yes")
	if code != 0 {
		t.Fatalf("adopt exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "rules adopted") {
		t.Fatalf("missing adopt line; got:\n%s", out)
	}
	after, err := os.ReadFile(filepath.Join(confConfigDir(t), "rules", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read file after adopt: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("adopt rewrote bytes it promised not to touch")
	}
	manifest, err := integrations.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	entry, ok := manifest.Entry(filepath.Join(confConfigDir(t), "rules", "AGENTS.md"))
	if !ok {
		t.Fatal("adopt did not record the destination")
	}
	if entry.Provenance != integrations.ProvenanceAdopted {
		t.Fatalf("provenance = %q, want adopted", entry.Provenance)
	}
	// No intent row: an adopt records bookkeeping, it does not claim intent.
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IntegrationRules != "" {
		t.Fatalf("adopt wrote intent %q; it must not", st.IntegrationRules)
	}
}

// TestIntegrationsPendingApplyUsesRecordedIntent pins the operand-less apply: it
// deploys exactly the features whose intent is an explicit on, and reports an
// honest empty state when nothing is recorded.
func TestIntegrationsPendingApplyUsesRecordedIntent(t *testing.T) {
	sandboxIntegrations(t)
	code, out, errw := runIntegrations(t, "apply", "--yes")
	if code != 0 {
		t.Fatalf("empty pending exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "Nothing is pending") {
		t.Fatalf("expected the named empty state; got:\n%s", out)
	}
	if err := mode.SaveState(&mode.State{
		CurrentMode:      "clean",
		IntegrationRules: mode.IntegrationToggleOn,
		ManagedItems:     append([]string(nil), mode.DefaultManagedItems...),
		UpdatedAt:        time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	code, out, errw = runIntegrations(t, "apply", "--yes")
	if code != 0 {
		t.Fatalf("pending exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] rules deployed") {
		t.Fatalf("the pending set was not deployed; got:\n%s", out)
	}
	if strings.Contains(out, "[SUCCESS] skills deployed") {
		t.Fatalf("the pending apply deployed a feature nobody asked for:\n%s", out)
	}
}

// TestIntegrationsRefusesDerivedPreset pins the vocabulary: custom is computed
// from the toggles, so no verb may apply it.
func TestIntegrationsRefusesDerivedPreset(t *testing.T) {
	sandboxIntegrations(t)
	for _, args := range [][]string{{"apply", "custom"}, {"retract", "custom"}, {"preset", "custom"}} {
		code, _, errw := runIntegrations(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want 2", args, code)
		}
		if !strings.Contains(errw, "derived") {
			t.Fatalf("%v refusal does not explain that custom is derived; got:\n%s", args, errw)
		}
	}
}

// TestIntegrationsUsageSurface pins the verb inventory and the unknown-operand
// path (masked operand, usage, exit 2).
func TestIntegrationsUsageSurface(t *testing.T) {
	sandboxIntegrations(t)
	code, _, errw := runIntegrations(t)
	if code != 2 || !strings.Contains(errw, "integrations needs a verb") {
		t.Fatalf("no-verb exit = %d, stderr:\n%s", code, errw)
	}
	code, _, errw = runIntegrations(t, "bogus")
	if code != 2 || !strings.Contains(errw, "unknown integrations verb or operand") {
		t.Fatalf("unknown verb exit = %d, stderr:\n%s", code, errw)
	}
	code, out, _ := runIntegrations(t, "help")
	if code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	for _, want := range []string{"status", "apply", "retract", "preset native|tools", "adopt", "rollback", "repair"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage text missing %q; got:\n%s", want, out)
		}
	}
}

// TestIntegrationsAdoptWithoutAnythingToRecord pins the record-only verb's empty
// case: on a machine with nothing deployed, adopt presents no gate at all (there
// is nothing it could record) and writes nothing.
func TestIntegrationsAdoptWithoutAnythingToRecord(t *testing.T) {
	sandboxIntegrations(t)
	code, out, errw := runIntegrations(t, "adopt")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "Nothing to write") {
		t.Fatalf("expected the no-gate empty state; got:\n%s", out)
	}
	if strings.Contains(out, "Approve Integrations Write") {
		t.Fatalf("adopt opened a gate it cannot describe:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(statePathForTest(t)), "integrations.json")); !os.IsNotExist(err) {
		t.Fatalf("an empty adopt created a manifest (err = %v)", err)
	}
}

// TestIntegrationsPresetVerbReachesBothPostures pins the preset verb end to end:
// tools deploys the whole set and records the row, native retracts it again, and
// both run through the same single-consent gate.
func TestIntegrationsPresetVerbReachesBothPostures(t *testing.T) {
	sandboxIntegrations(t)
	installStatusLineLauncher(t)
	code, out, errw := runIntegrations(t, "preset", "tools", "--yes")
	if code != 0 {
		t.Fatalf("preset tools exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "integration_preset: (unset) -> tools") {
		t.Fatalf("preset tools diff missing the preset row; got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(confConfigDir(t), "skills")); err != nil {
		t.Fatalf("preset tools did not deploy: %v", err)
	}
	code, out, errw = runIntegrations(t, "preset", "native", "--yes")
	if code != 0 {
		t.Fatalf("preset native exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "integration_preset: tools -> native") {
		t.Fatalf("preset native diff missing the preset row; got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(confConfigDir(t), "skills")); !os.IsNotExist(err) {
		t.Fatalf("preset native left the skills tree behind (err = %v)", err)
	}
}

// TestIntegrationsRollbackRestoresASnapshot pins the rollback verb when a chain
// exists: the retract's ledger archive is restored, and the verb names the
// snapshot it used.
func TestIntegrationsRollbackRestoresASnapshot(t *testing.T) {
	sandboxIntegrations(t)
	installStatusLineLauncher(t)
	if code, _, errw := runIntegrations(t, "apply", "statusline", "--yes"); code != 0 {
		t.Fatalf("apply statusline exit = %d, stderr:\n%s", code, errw)
	}
	if code, _, errw := runIntegrations(t, "retract", "statusline", "--yes"); code != 0 {
		t.Fatalf("retract statusline exit = %d, stderr:\n%s", code, errw)
	}
	code, out, errw := runIntegrations(t, "rollback", "--yes")
	if code != 0 {
		t.Fatalf("rollback exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] settings.json restored from") {
		t.Fatalf("missing rollback success line; got:\n%s", out)
	}
	settingsPath, pathErr := integrationSettingsPath()
	if pathErr != nil {
		t.Fatalf("GlobalSettingsPath: %v", pathErr)
	}
	data, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatalf("read restored settings.json: %v", readErr)
	}
	if !bytes.Contains(data, []byte("statusLine")) {
		t.Fatalf("the restored file does not carry the slot the retract removed:\n%s", data)
	}
}

// TestIntegrationsStatusLineLauncherRefusal pins the wrapper-missing refusal on an
// explicit statusline target: the plan refuses, nothing is written, and the
// sentence names the documented shape rather than a resolved path.
func TestIntegrationsStatusLineLauncherRefusal(t *testing.T) {
	sandboxIntegrations(t)
	code, _, errw := runIntegrations(t, "apply", "statusline", "--yes")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errw, "agy-tokens") {
		t.Fatalf("the refusal does not name the documented launcher shape; got:\n%s", errw)
	}
	if data, readErr := os.ReadFile(statePathForTest(t)); readErr == nil && strings.Contains(string(data), "integration_statusline") {
		t.Fatalf("a refused statusline apply wrote intent:\n%s", data)
	}
}

// ---------------------------------------------------------------------------
// rollback of a content feature's deploy tree

// TestIntegrationsRollbackTreeRestoresPreInstallBytes is the wiring pin for
// `rollback <feature>`: a destination the apply overwrote goes back to the bytes it
// held before, a destination the apply created is moved out of the config tree, and
// the verb records no intent (a rollback is not a posture the user stated).
func TestIntegrationsRollbackTreeRestoresPreInstallBytes(t *testing.T) {
	sandboxIntegrations(t)
	preInstall := []byte("# the user's own rules file\n")
	target := filepath.Join(confConfigDir(t), "rules", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir rules dir: %v", err)
	}
	if err := os.WriteFile(target, preInstall, 0o600); err != nil {
		t.Fatalf("plant the pre-install file: %v", err)
	}
	if code, _, errw := runIntegrations(t, "apply", "rules", "--yes"); code != 0 {
		t.Fatalf("apply exit = %d, stderr:\n%s", code, errw)
	}
	if live, err := os.ReadFile(target); err != nil || bytes.Equal(live, preInstall) {
		t.Fatalf("the apply did not overwrite the planted file (err = %v)", err)
	}
	code, out, errw := runIntegrations(t, "rollback", "rules", "--yes")
	if code != 0 {
		t.Fatalf("rollback exit = %d, stderr:\n%s", code, errw)
	}
	for _, want := range []string{"[SUCCESS] rules rolled back", "restored", "removed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rollback output missing %q; got:\n%s", want, out)
		}
	}
	live, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read the restored file: %v", err)
	}
	if !bytes.Equal(live, preInstall) {
		t.Fatalf("the destination holds %q, want the pre-install bytes", live)
	}
	if _, err := os.Stat(filepath.Join(confConfigDir(t), "rules", "GEMINI.md")); !os.IsNotExist(err) {
		t.Fatalf("a file the apply created survived the rollback (err = %v)", err)
	}
	// A rollback states no intent: the row the apply wrote is untouched.
	if got := stateRow(t, func(s *mode.State) string { return s.IntegrationRules }); got != mode.IntegrationToggleOn {
		t.Fatalf("integration_rules = %q; the rollback rewrote an intent row", got)
	}
	// A second run has nothing left to resolve.
	code, out, errw = runIntegrations(t, "rollback", "rules", "--yes")
	if code != 0 {
		t.Fatalf("second rollback exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "Nothing to roll back") {
		t.Fatalf("a second rollback presented a write; got:\n%s", out)
	}
}

// TestIntegrationsRollbackTreeUsesOneConsent pins the consent invariant on the new
// path: exactly ONE banner and ONE [y/N] for a rollback the operator approves, and a
// non-interactive stdin without --yes writes nothing at all.
func TestIntegrationsRollbackTreeUsesOneConsent(t *testing.T) {
	sandboxIntegrations(t)
	target := filepath.Join(confConfigDir(t), "rules", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir rules dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("# pre-install\n"), 0o600); err != nil {
		t.Fatalf("plant the pre-install file: %v", err)
	}
	if code, _, errw := runIntegrations(t, "apply", "rules", "--yes"); code != 0 {
		t.Fatalf("apply exit = %d, stderr:\n%s", code, errw)
	}
	if err := os.WriteFile(target, []byte("hand-edited\n"), 0o600); err != nil {
		t.Fatalf("edit the deployed file: %v", err)
	}
	code, out, errw := runIntegrationsConsole(t, "y\n", "rollback", "rules")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, errw)
	}
	if n := strings.Count(out, "[agy-pro] Approve Integrations Write"); n != 1 {
		t.Fatalf("consent banners = %d, want exactly 1; output:\n%s", n, out)
	}
	if n := strings.Count(out, "[y/N]"); n != 1 {
		t.Fatalf("prompts = %d, want exactly 1; output:\n%s", n, out)
	}
	if strings.Contains(out, "state.json will be written") {
		t.Fatalf("the rollback diff claims an intent write:\n%s", out)
	}

	// Pipe refusal: the diff is printed, --yes is named, and nothing is restored.
	if code, _, errw := runIntegrations(t, "apply", "rules", "--yes"); code != 0 {
		t.Fatalf("second apply exit = %d, stderr:\n%s", code, errw)
	}
	if err := os.WriteFile(target, []byte("hand-edited again\n"), 0o600); err != nil {
		t.Fatalf("edit the deployed file: %v", err)
	}
	code, out, errw = runIntegrations(t, "rollback", "rules")
	if code != 1 {
		t.Fatalf("pipe refusal exit = %d, want 1", code)
	}
	if !strings.Contains(errw, "--yes") {
		t.Fatalf("the refusal does not name --yes; got:\n%s", errw)
	}
	if live, _ := os.ReadFile(target); string(live) != "hand-edited again\n" {
		t.Fatalf("a refused rollback wrote the destination: %q", live)
	}
}

// TestIntegrationsRollbackTreeNamedEmptyState pins the empty state: a feature the
// manifest records nothing for prints the reason and exits 0 without a gate.
func TestIntegrationsRollbackTreeNamedEmptyState(t *testing.T) {
	sandboxIntegrations(t)
	code, out, errw := runIntegrations(t, "rollback", "skills")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, errw)
	}
	if !strings.Contains(out, "Nothing to roll back") || !strings.Contains(out, "manifest records no destination") {
		t.Fatalf("the empty state is not named; got:\n%s", out)
	}
	if strings.Contains(out, "Approve Integrations Write") {
		t.Fatalf("an empty rollback asked for consent:\n%s", out)
	}
}

// TestIntegrationsRollbackOperandGrammar pins the rollback operand grammar: a
// feature rolls back its tree, `statusline` names the settings door instead of
// pretending the content engine owns it, a preset is refused (a posture has no single
// pre-install tree), `custom` stays derived, and two operands are a usage error.
func TestIntegrationsRollbackOperandGrammar(t *testing.T) {
	sandboxIntegrations(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"rollback", "statusline"}, "settings.json door"},
		{[]string{"rollback", "tools"}, "no single pre-install tree"},
		{[]string{"rollback", "native"}, "no single pre-install tree"},
		{[]string{"rollback", "custom"}, "derived"},
		{[]string{"rollback", "bogus"}, "unknown integrations verb or operand"},
		{[]string{"rollback", "rules", "skills"}, "unknown integrations verb or operand"},
	} {
		code, _, errw := runIntegrations(t, tc.args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want 2; stderr:\n%s", tc.args, code, errw)
		}
		if !strings.Contains(errw, tc.want) {
			t.Fatalf("%v refusal missing %q; got:\n%s", tc.args, tc.want, errw)
		}
	}
}

// ---------------------------------------------------------------------------
// fixtures

// integrationRowForTest reads one feature's intent row through the same closed
// table the surface uses.
func integrationRowForTest(st *mode.State, feature integrations.Feature) string {
	switch feature {
	case integrations.FeatureStatusline:
		return st.IntegrationStatusline
	case integrations.FeatureHook:
		return st.IntegrationHook
	case integrations.FeatureRules:
		return st.IntegrationRules
	case integrations.FeaturePlugins:
		return st.IntegrationPlugins
	case integrations.FeatureSkills:
		return st.IntegrationSkills
	}
	return ""
}

// statePathForTest resolves the state.json path inside the sandbox.
func statePathForTest(t *testing.T) string {
	t.Helper()
	path, err := paths.AgyProStateFile()
	if err != nil {
		t.Fatalf("AgyProStateFile: %v", err)
	}
	return path
}

// integrationSettingsPath resolves the global settings.json path inside the
// sandbox (the door's own resolver).
func integrationSettingsPath() (string, error) {
	return agysettings.GlobalSettingsPath()
}
