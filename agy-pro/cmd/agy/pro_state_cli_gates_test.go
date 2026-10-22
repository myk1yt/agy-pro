package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/settings"
)

// P5-1 U2 (F3(a)): every persisted-state CLI write presents its exact diff,
// takes consent, and re-verifies the presented state before the write. This
// file covers the gate contract for the state.json doors (settings set, config
// default-profile/bridge-tools, hybrid on/off) and the profile deployment doors
// (mode switch/restore, including the second settings-mode doors).
//
// Nothing here touches the live ~/.agy-pro or ~/.gemini: every write door runs
// against a temp AGY_PRO_HOME + GEMINI_CONFIG_DIR, and the settings doors run
// against the in-memory core the package's settings tests install.

// sandboxStateCLI points the state and profile paths at temp dirs and clears
// the hybrid env override, so a state/mode door reads and writes only there. The
// user home is redirected with them (F-S9B-03): the settings door both mode verbs
// can reach resolves its protected settings path through HOME/USERPROFILE, which
// GEMINI_CONFIG_DIR does not cover.
func sandboxStateCLI(t *testing.T) (agyHome, configDir string) {
	t.Helper()
	tmp := t.TempDir()
	agyHome = filepath.Join(tmp, "agy-pro")
	configDir = filepath.Join(tmp, "gemini-config")
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv(paths.EnvAgyProHome, agyHome)
	t.Setenv(paths.EnvGeminiConfigDir, configDir)
	return agyHome, configDir
}

// mustStateFile returns the state.json path inside the current sandbox.
func mustStateFile(t *testing.T) string {
	t.Helper()
	file, err := paths.AgyProStateFile()
	if err != nil {
		t.Fatalf("AgyProStateFile: %v", err)
	}
	return file
}

// stubStateReload replaces the write-time state re-read for one test, so an
// external edit can be interleaved exactly where production re-verifies.
func stubStateReload(t *testing.T, fn func() (*mode.State, error)) {
	t.Helper()
	prev := stateReload
	stateReload = fn
	t.Cleanup(func() { stateReload = prev })
}

// TestStateWriteGate_PipeRefusalNamesYes: a non-interactive stdin without
// --yes refuses every state.json door with exit 1, names the flag, prints the
// diff first (never silent), and writes nothing.
func TestStateWriteGate_PipeRefusalNamesYes(t *testing.T) {
	t.Run("settings set", func(t *testing.T) {
		tc := installSettingsCore(t)
		code, out, errw := runSettings(t, []string{"set", "default-profile", "native"}, "")
		if code != 1 {
			t.Fatalf("set exit = %d; want 1 (stderr=%v)", code, errw)
		}
		if !strings.Contains(out, "[agy-pro] Approve State Write") {
			t.Errorf("the diff must print before the refusal:\n%s", out)
		}
		if !strings.Contains(errw, "--yes") || !strings.Contains(errw, "refusal") {
			t.Errorf("the refusal must name --yes:\n%s", errw)
		}
		if tc.state.DefaultProfile != "" {
			t.Errorf("a refused write reached the state: %+v", tc.state)
		}
	})

	t.Run("settings mode switch", func(t *testing.T) {
		tc := installSettingsCore(t)
		code, out, errw := runSettings(t, []string{"mode", "switch", "full"}, "")
		if code != 1 {
			t.Fatalf("mode switch exit = %d; want 1 (stderr=%v)", code, errw)
		}
		if !strings.Contains(out, "[agy-pro] Approve Profile Deployment") {
			t.Errorf("the file plan must print before the refusal:\n%s", out)
		}
		if !strings.Contains(errw, "--yes") {
			t.Errorf("the refusal must name --yes:\n%s", errw)
		}
		if len(tc.switched) != 0 {
			t.Errorf("a refused deployment reached the switch: %v", tc.switched)
		}
	})

	t.Run("settings mode restore", func(t *testing.T) {
		installSettingsCore(t)
		// The plan needs a restore source; without one the door refuses before
		// the gate with mode.Restore's own sentence.
		code, _, errw := runSettings(t, []string{"mode", "restore"}, "")
		if code != 1 {
			t.Fatalf("mode restore exit = %d; want 1 (stderr=%v)", code, errw)
		}
		if !strings.Contains(errw, "no previous backups found to restore") {
			t.Errorf("the refusal must be mode.Restore's own sentence:\n%s", errw)
		}
	})

	t.Run("config default-profile", func(t *testing.T) {
		sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runConfigTo([]string{"default-profile", "native"}, strings.NewReader(""), &out, &errw); code != 1 {
			t.Fatalf("default-profile exit = %d; want 1", code)
		}
		if !strings.Contains(out.String(), "default_profile: (unset) -> native") {
			t.Errorf("the diff must print before the refusal:\n%s", out.String())
		}
		if !strings.Contains(errw.String(), "--yes") {
			t.Errorf("the refusal must name --yes:\n%s", errw.String())
		}
		if _, err := os.Stat(mustStateFile(t)); !os.IsNotExist(err) {
			t.Errorf("a refused write created state.json: %v", err)
		}
	})

	t.Run("config bridge-tools", func(t *testing.T) {
		sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runConfigTo([]string{"bridge-tools", "read,search,write"}, strings.NewReader(""), &out, &errw); code != 1 {
			t.Fatalf("bridge-tools exit = %d; want 1", code)
		}
		if !strings.Contains(errw.String(), "--yes") {
			t.Errorf("the refusal must name --yes:\n%s", errw.String())
		}
		if _, err := os.Stat(mustStateFile(t)); !os.IsNotExist(err) {
			t.Errorf("a refused write created state.json: %v", err)
		}
	})

	t.Run("hybrid on", func(t *testing.T) {
		sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runHybridCmdToIn([]string{"on", "p1"}, strings.NewReader(""), &out, &errw); code != 1 {
			t.Fatalf("hybrid on exit = %d; want 1", code)
		}
		if !strings.Contains(out.String(), "hybrid_profile: (unset) -> p1") {
			t.Errorf("the diff must print before the refusal:\n%s", out.String())
		}
		if _, err := os.Stat(mustStateFile(t)); !os.IsNotExist(err) {
			t.Errorf("a refused write created state.json: %v", err)
		}
	})

	t.Run("mode switch", func(t *testing.T) {
		_, configDir := sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runModeTo([]string{"full"}, strings.NewReader(""), &out, &errw); code != 1 {
			t.Fatalf("mode full exit = %d; want 1", code)
		}
		if !strings.Contains(out.String(), "[agy-pro] Approve Profile Deployment") {
			t.Errorf("the file plan must print before the refusal:\n%s", out.String())
		}
		if _, err := os.Stat(mustStateFile(t)); !os.IsNotExist(err) {
			t.Errorf("a refused deployment created state.json: %v", err)
		}
		if _, err := os.Stat(filepath.Join(configDir, "agents")); !os.IsNotExist(err) {
			t.Errorf("a refused deployment wrote into the config dir: %v", err)
		}
	})
}

// TestStateWriteGate_YesConsentWrites: --yes consents after the diff prints, and
// the write lands.
func TestStateWriteGate_YesConsentWrites(t *testing.T) {
	t.Run("settings set", func(t *testing.T) {
		tc := installSettingsCore(t)
		if code, _, errw := runSettings(t, []string{"set", "default-profile", "native", "--yes"}, ""); code != 0 {
			t.Fatalf("set exit = %d (stderr=%v)", code, errw)
		}
		if tc.state.DefaultProfile != mode.ProfileNative {
			t.Errorf("state = %+v", tc.state)
		}
	})

	t.Run("config default-profile", func(t *testing.T) {
		sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runConfigTo([]string{"default-profile", "gateway", "--yes"}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("default-profile exit = %d (stderr=%v)", code, errw)
		}
		if !strings.Contains(out.String(), "[SUCCESS] Default launch profile set to: gateway") {
			t.Errorf("missing success line:\n%s", out.String())
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.DefaultProfile != mode.ProfileGateway {
			t.Errorf("persisted DefaultProfile = %q; want gateway", st.DefaultProfile)
		}
	})

	t.Run("config bridge-tools", func(t *testing.T) {
		sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runConfigTo([]string{"bridge-tools", "exec,read", "--yes"}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("bridge-tools exit = %d (stderr=%v)", code, errw)
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.BridgeTools != "read,exec" {
			t.Errorf("persisted BridgeTools = %q; want the canonical read,exec", st.BridgeTools)
		}
	})

	t.Run("hybrid on and off", func(t *testing.T) {
		sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runHybridCmdToIn([]string{"on", "p2", "--yes"}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("hybrid on exit = %d (stderr=%v)", code, errw)
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.HybridProfile != "p2" {
			t.Errorf("persisted HybridProfile = %q; want p2", st.HybridProfile)
		}

		out.Reset()
		errw.Reset()
		if code := runHybridCmdToIn([]string{"off", "--yes"}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("hybrid off exit = %d (stderr=%v)", code, errw)
		}
		if !strings.Contains(out.String(), "hybrid_profile: p2 -> (unset)") {
			t.Errorf("the off diff must show the key being cleared:\n%s", out.String())
		}
		st, err = mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.HybridProfile != "" {
			t.Errorf("HybridProfile after off = %q; want empty", st.HybridProfile)
		}
	})

	t.Run("mode switch", func(t *testing.T) {
		_, configDir := sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runModeTo([]string{"full", "--yes"}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("mode full exit = %d (stderr=%v)", code, errw)
		}
		if !strings.Contains(out.String(), "[SUCCESS] Switched mode to: full") {
			t.Errorf("missing switch success line:\n%s", out.String())
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.CurrentMode != mode.ModeFull {
			t.Errorf("CurrentMode = %q; want full", st.CurrentMode)
		}
		// agents/ is a mode-owned destination, so this assertion survives the
		// P5-3 partition and T7; rules/AGENTS.md was the pre-partition form.
		if _, err := os.Stat(filepath.Join(configDir, "agents", "architect", "AGENT.md")); err != nil {
			t.Errorf("the full profile was not deployed: %v", err)
		}
	})

	t.Run("interactive mode with yes", func(t *testing.T) {
		sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runModeTo([]string{"--yes"}, strings.NewReader("1\n"), &out, &errw); code != 0 {
			t.Fatalf("interactive mode exit = %d (stderr=%v)", code, errw)
		}
		if !strings.Contains(out.String(), "Select environment profile mode:") {
			t.Errorf("the interactive prompt must still print:\n%s", out.String())
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.CurrentMode != mode.ModeFull {
			t.Errorf("CurrentMode = %q; want full", st.CurrentMode)
		}
	})

	t.Run("mode restore", func(t *testing.T) {
		_, configDir := sandboxStateCLI(t)
		// Seed one MODE-OWNED entry so the switch has something to back up: the
		// restore then has a real source and its content must come back. The
		// seed used to be rules/custom.md; rules left mode's managed register in
		// the P5-3 partition, so a switch no longer backs it up and the seed
		// moved to the agents tree that mode still owns.
		userAgent := filepath.Join(configDir, "agents", "custom", "AGENT.md")
		if err := os.MkdirAll(filepath.Dir(userAgent), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(userAgent, []byte("user-agent"), 0o600); err != nil {
			t.Fatal(err)
		}

		var out, errw bytes.Buffer
		if code := runModeTo([]string{"full", "--yes"}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("mode full exit = %d (stderr=%v)", code, errw)
		}
		if _, err := os.Stat(userAgent); !os.IsNotExist(err) {
			t.Fatalf("the switch must move the seeded entry to its backup: %v", err)
		}
		out.Reset()
		errw.Reset()
		if code := runModeTo([]string{"restore", "--yes"}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("mode restore exit = %d (stderr=%v)", code, errw)
		}
		if !strings.Contains(out.String(), "Restores the newest profile backup") {
			t.Errorf("the restore plan must print:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "Restore source:") {
			t.Errorf("the plan must name the backup it restores:\n%s", out.String())
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.CurrentMode != "restored" {
			t.Errorf("CurrentMode = %q; want restored", st.CurrentMode)
		}
		restored, err := os.ReadFile(userAgent)
		if err != nil {
			t.Fatalf("the backed-up entry was not restored: %v", err)
		}
		if string(restored) != "user-agent" {
			t.Errorf("restored content = %q; want user-agent", restored)
		}
	})

	// F-S9A-3: the presented restore plan must match the narrowed copy-back. A
	// LEGACY backup (taken before the P5-3 destination partition) still carries
	// rules/ and skills/, and mode.Restore copies back only the entries
	// mode.DefaultManagedItems owns. Before this pin the plan counted all five
	// files and advertised "rules (2 files)" and "skills (1 file)" - a write the
	// verb never performs.
	t.Run("mode restore plan counts only what the copy-back writes", func(t *testing.T) {
		agyHome, configDir := sandboxStateCLI(t)
		backupDir := filepath.Join(agyHome, "backups", "20260101_000000")
		for rel, body := range map[string]string{
			// Register-covered: 2 files the restore writes.
			"agents/architect/AGENT.md": "restored agent",
			"profile.json":              `{"name":"full"}`,
			// Integrations-owned trees the register does not cover: 3 files the
			// restore leaves where they are.
			"rules/AGENTS.md":       "legacy rules",
			"rules/extra.md":        "legacy extra rules",
			"skills/alpha/SKILL.md": "legacy skill",
		} {
			full := filepath.Join(backupDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// A LIVE engine-owned file the restore must not touch.
		liveRule := filepath.Join(configDir, "rules", "AGENTS.md")
		if err := os.MkdirAll(filepath.Dir(liveRule), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(liveRule, []byte("live engine rules"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := mode.LoadState()
		if err != nil || st == nil {
			t.Fatalf("LoadState: %v", err)
		}
		st.LastBackup = backupDir
		if err := mode.SaveState(st); err != nil {
			t.Fatalf("SaveState: %v", err)
		}

		var out, errw bytes.Buffer
		if code := runModeTo([]string{"restore", "--yes"}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("mode restore exit = %d (stderr=%v)", code, errw)
		}
		transcript := out.String()
		if want := "Restore source: " + backupDir + " (2 files)"; !strings.Contains(transcript, want) {
			t.Errorf("the plan does not count exactly the register-covered files (%q):\n%s", want, transcript)
		}
		if want := "Left in place (not a mode-managed item): rules, skills"; !strings.Contains(transcript, want) {
			t.Errorf("the plan does not name the entries the copy-back skips (%q):\n%s", want, transcript)
		}
		// The verb's own count agrees with the plan: two entries copied back, not
		// five, and the skipped names are the same two the plan named.
		if want := "[SUCCESS] Restored 2 items from:"; !strings.Contains(transcript, want) {
			t.Errorf("the restore did not report the planned two entries (%q):\n%s", want, transcript)
		}
		for _, over := range []string{"rules (2 files)", "skills (1 file)"} {
			if strings.Contains(transcript, over) {
				t.Errorf("the plan advertises %q, a write the narrowed copy-back does not perform:\n%s", over, transcript)
			}
		}
		// The write matches the plan: the two register entries arrived, and the
		// engine-owned trees are byte-identical to what was live.
		restoredAgent, err := os.ReadFile(filepath.Join(configDir, "agents", "architect", "AGENT.md"))
		if err != nil {
			t.Fatalf("the register-covered agent was not restored: %v", err)
		}
		if string(restoredAgent) != "restored agent" {
			t.Errorf("restored agent = %q; want the snapshot body", restoredAgent)
		}
		if _, err := os.Stat(filepath.Join(configDir, "profile.json")); err != nil {
			t.Fatalf("the register-covered profile.json was not restored: %v", err)
		}
		live, err := os.ReadFile(liveRule)
		if err != nil {
			t.Fatalf("read the live engine rule: %v", err)
		}
		if string(live) != "live engine rules" {
			t.Errorf("the restore overwrote an integrations-owned file: %q", live)
		}
		if _, err := os.Stat(filepath.Join(configDir, "skills")); !os.IsNotExist(err) {
			t.Errorf("the restore copied the snapshot's skills tree back (stat err = %v)", err)
		}
	})
}

// TestStateWriteGate_ConsoleConsent: console stdin prompts y/N; y writes, n and
// an over-long answer cancel with nothing written, and an immediate EOF takes
// the pipe refusal (exit 1 naming --yes).
func TestStateWriteGate_ConsoleConsent(t *testing.T) {
	stubStdinIsTTY(t, true)

	t.Run("y writes", func(t *testing.T) {
		tc := installSettingsCore(t)
		code, out, errw := runSettings(t, []string{"set", "default-profile", "native"}, "y\n")
		if code != 0 {
			t.Fatalf("console y exit = %d (stderr=%v)", code, errw)
		}
		if !strings.Contains(out, "Apply this write to state.json? [y/N]: ") {
			t.Errorf("the console prompt must name state.json:\n%s", out)
		}
		if tc.state.DefaultProfile != mode.ProfileNative {
			t.Errorf("a consented write did not land: %+v", tc.state)
		}
	})

	t.Run("n cancels", func(t *testing.T) {
		tc := installSettingsCore(t)
		code, out, errw := runSettings(t, []string{"set", "default-profile", "native"}, "n\n")
		if code != 0 {
			t.Fatalf("console n exit = %d (want 0, stderr=%s)", code, errw)
		}
		if !strings.Contains(out, "[CANCELLED] nothing written") {
			t.Errorf("a declined write must say nothing was written:\n%s", out)
		}
		if tc.state.DefaultProfile != "" {
			t.Errorf("a declined write reached the state: %+v", tc.state)
		}
	})

	t.Run("over-long answer cancels", func(t *testing.T) {
		tc := installSettingsCore(t)
		code, _, _ := runSettings(t, []string{"set", "default-profile", "native"}, strings.Repeat("y", 200)+"\n")
		if code != 0 {
			t.Fatalf("over-long answer exit = %d; want 0 (cancel)", code)
		}
		if tc.state.DefaultProfile != "" {
			t.Errorf("an over-long answer wrote the state: %+v", tc.state)
		}
	})

	t.Run("immediate EOF refuses", func(t *testing.T) {
		tc := installSettingsCore(t)
		code, _, errw := runSettings(t, []string{"set", "default-profile", "native"}, "")
		if code != 1 {
			t.Fatalf("EOF exit = %d; want 1", code)
		}
		if !strings.Contains(errw, "--yes") {
			t.Errorf("the EOF refusal must name --yes:\n%s", errw)
		}
		if tc.state.DefaultProfile != "" {
			t.Errorf("an EOF refusal wrote the state: %+v", tc.state)
		}
	})

	t.Run("mode switch console prompt names the configuration", func(t *testing.T) {
		sandboxStateCLI(t)
		var out, errw bytes.Buffer
		if code := runModeTo([]string{"full"}, strings.NewReader("y\n"), &out, &errw); code != 0 {
			t.Fatalf("mode console y exit = %d (stderr=%v)", code, errw)
		}
		if !strings.Contains(out.String(), "Apply this write to the live profile configuration? [y/N]: ") {
			t.Errorf("the console prompt must name the deployment target:\n%s", out.String())
		}
	})
}

// TestStateWriteGate_StaleBaseRefusals covers one drift arm per writer class:
// the presented state moved under the prompt, so the write refuses with a named
// sentence and leaves the external edit in place.
func TestStateWriteGate_StaleBaseRefusals(t *testing.T) {
	t.Run("settings set scalar drift", func(t *testing.T) {
		tc := installSettingsCore(t)
		stubStateReload(t, func() (*mode.State, error) {
			edited := *tc.state
			edited.DefaultProfile = mode.ProfileGateway
			tc.state = &edited
			return tc.state, nil
		})
		code, _, errw := runSettings(t, []string{"set", "default-profile", "native", "--yes"}, "")
		if code != 1 {
			t.Fatalf("drifted set exit = %d; want 1 (stderr=%v)", code, errw)
		}
		if !strings.Contains(errw, "changed on disk") || !strings.Contains(errw, "nothing written") {
			t.Errorf("the refusal must name the drift:\n%s", errw)
		}
		if tc.state.DefaultProfile != mode.ProfileGateway {
			t.Errorf("the external edit was clobbered: %+v", tc.state)
		}
	})

	t.Run("config bridge-tools drift on disk", func(t *testing.T) {
		sandboxStateCLI(t)
		stubStateReload(t, func() (*mode.State, error) {
			st, err := mode.LoadState()
			if err != nil {
				return nil, err
			}
			st.BridgeTools = mode.BridgeToolsNone
			if err := mode.SaveState(st); err != nil {
				return nil, err
			}
			return mode.LoadState()
		})
		var out, errw bytes.Buffer
		code := runConfigTo([]string{"bridge-tools", "read,search,write", "--yes"}, strings.NewReader(""), &out, &errw)
		if code != 1 {
			t.Fatalf("drifted bridge-tools exit = %d; want 1 (stderr=%v)", code, errw)
		}
		if !strings.Contains(errw.String(), "bridge_tools in state.json changed on disk") {
			t.Errorf("the refusal must name the drifted key:\n%s", errw.String())
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.BridgeTools != mode.BridgeToolsNone {
			t.Errorf("the external edit was clobbered: %q", st.BridgeTools)
		}
	})

	t.Run("mode switch managed entry appeared", func(t *testing.T) {
		_, configDir := sandboxStateCLI(t)
		// agents is a mode-owned entry; a rules/ tree appearing under the prompt
		// is no longer managed-set drift (P5-3 partition), so the drift has to
		// be provoked with a destination mode still owns.
		stubStateReload(t, func() (*mode.State, error) {
			if err := os.MkdirAll(filepath.Join(configDir, "agents"), 0o755); err != nil {
				return nil, err
			}
			return mode.LoadState()
		})
		var out, errw bytes.Buffer
		code := runModeTo([]string{"full", "--yes"}, strings.NewReader(""), &out, &errw)
		if code != 1 {
			t.Fatalf("drifted switch exit = %d; want 1 (stderr=%v)", code, errw)
		}
		if !strings.Contains(errw.String(), "managed entry") || !strings.Contains(errw.String(), "nothing written") {
			t.Errorf("the refusal must name the drifted managed set:\n%s", errw.String())
		}
		if _, err := os.Stat(filepath.Join(configDir, "agents", "architect", "AGENT.md")); !os.IsNotExist(err) {
			t.Errorf("a refused deployment wrote the profile: %v", err)
		}
	})

	t.Run("mode switch current mode drift", func(t *testing.T) {
		sandboxStateCLI(t)
		stubStateReload(t, func() (*mode.State, error) {
			st, err := mode.LoadState()
			if err != nil {
				return nil, err
			}
			st.CurrentMode = mode.ModeFull
			if err := mode.SaveState(st); err != nil {
				return nil, err
			}
			return mode.LoadState()
		})
		var out, errw bytes.Buffer
		code := runModeTo([]string{"clean", "--yes"}, strings.NewReader(""), &out, &errw)
		if code != 1 {
			t.Fatalf("drifted current-mode exit = %d; want 1 (stderr=%v)", code, errw)
		}
		if !strings.Contains(errw.String(), "current_mode in state.json changed on disk") {
			t.Errorf("the refusal must name the drifted mode:\n%s", errw.String())
		}
	})

	t.Run("mode restore source drift", func(t *testing.T) {
		agyHome, _ := sandboxStateCLI(t)
		source := filepath.Join(agyHome, "backups", "20260101-000000")
		// The signature covers the register-covered entries only (F-S9A-3), so the
		// drift has to land inside one of those - a late file in the backup's
		// rules/ tree is not this plan's staleness to refuse.
		if err := os.MkdirAll(filepath.Join(source, "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "agents", "AGENT.md"), []byte("v1"), 0o600); err != nil {
			t.Fatal(err)
		}
		st := &mode.State{CurrentMode: mode.ModeFull, ManagedItems: mode.DefaultManagedItems, LastBackup: source}
		if err := mode.SaveState(st); err != nil {
			t.Fatal(err)
		}
		stubStateReload(t, func() (*mode.State, error) {
			if err := os.WriteFile(filepath.Join(source, "agents", "EXTRA.md"), []byte("v2"), 0o600); err != nil {
				return nil, err
			}
			return mode.LoadState()
		})
		var out, errw bytes.Buffer
		code := runModeTo([]string{"restore", "--yes"}, strings.NewReader(""), &out, &errw)
		if code != 1 {
			t.Fatalf("drifted restore exit = %d; want 1 (stderr=%v)", code, errw)
		}
		if !strings.Contains(errw.String(), "restore source") || !strings.Contains(errw.String(), "nothing written") {
			t.Errorf("the refusal must name the drifted restore source:\n%s", errw.String())
		}
	})
}

// TestStateWriteGate_MasksKeyShapedValues: a key-shaped operand in a state.json
// slot never reaches the consent transcript raw, and the key slot itself masks
// on the unknown-key line.
func TestStateWriteGate_MasksKeyShapedValues(t *testing.T) {
	installSettingsCore(t)

	code, out, errw := runSettings(t, []string{"set", "default-profile", maskSweepKey}, "")
	if code != 1 {
		t.Fatalf("key-shaped value exit = %d; want 1 (stderr=%v)", code, errw)
	}
	for _, body := range []string{out, errw} {
		if strings.Contains(body, maskSweepKey) {
			t.Errorf("the transcript printed the planted key raw:\n%s", body)
		}
	}
	if !strings.Contains(out, "sk-pl****") {
		t.Errorf("the diff must render the masked value:\n%s", out)
	}

	code, out, errw = runSettings(t, []string{"set", maskSweepKey, "1"}, "")
	if code != 1 {
		t.Fatalf("key-shaped key exit = %d; want 1 (stderr=%v)", code, errw)
	}
	for _, body := range []string{out, errw} {
		if strings.Contains(body, maskSweepKey) {
			t.Errorf("the unknown-key line printed the planted key raw:\n%s", body)
		}
	}
}

// TestModeChoicePromptParity pins the interactive door's prompt to mode.Switch's
// own argless prompt, byte for byte: the argless switch owns the prompt and the
// write in one call, so the gate re-renders the prompt and must not drift.
func TestModeChoicePromptParity(t *testing.T) {
	sandboxStateCLI(t)

	var wantOut bytes.Buffer
	wantErr := mode.Switch("", strings.NewReader("bogus\n"), &wantOut)
	if wantErr == nil {
		t.Fatal("mode.Switch must reject an invalid interactive selection")
	}

	var gotOut, gotErr bytes.Buffer
	if _, code := promptModeChoice(strings.NewReader("bogus\n"), &gotOut, &gotErr); code != 1 {
		t.Fatalf("promptModeChoice exit = %d; want 1", code)
	}
	if gotOut.String() != wantOut.String() {
		t.Errorf("the mode prompt drifted from mode.Switch:\ngot:\n%q\nwant:\n%q", gotOut.String(), wantOut.String())
	}
	wantSentence := "[agy-pro] Mode switch failed: " + wantErr.Error() + "\n"
	if gotErr.String() != wantSentence {
		t.Errorf("the invalid-selection sentence drifted:\ngot:  %q\nwant: %q", gotErr.String(), wantSentence)
	}
}

// TestNormalizeModeTargetMatchesModeSwitch pins the plan's target aliases to the
// aliases mode.Switch actually accepts: every alias the plan recognizes must
// switch, and must land in the mode the plan named.
func TestNormalizeModeTargetMatchesModeSwitch(t *testing.T) {
	sandboxStateCLI(t)

	for alias, want := range map[string]string{
		"1":          mode.ModeFull,
		"full":       mode.ModeFull,
		"full-stack": mode.ModeFull,
		"2":          mode.ModeClean,
		"clean":      mode.ModeClean,
		"default":    mode.ModeClean,
	} {
		got, recognized := normalizeModeTarget(alias)
		if !recognized || got != want {
			t.Errorf("normalizeModeTarget(%q) = (%q, %v); want (%q, true)", alias, got, recognized, want)
			continue
		}
		var out bytes.Buffer
		if err := mode.Switch(alias, strings.NewReader(""), &out); err != nil {
			t.Errorf("mode.Switch(%q) refused an alias the plan presents: %v", alias, err)
			continue
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.CurrentMode != want {
			t.Errorf("mode.Switch(%q) landed in %q; the plan said %q", alias, st.CurrentMode, want)
		}
	}

	if _, recognized := normalizeModeTarget("bogus"); recognized {
		t.Error("normalizeModeTarget must not claim an alias mode.Switch rejects")
	}
}

// TestAfterStateValueMatchesCoreSet pins the diff projection to the store: for
// every key the CLI writes, the value the diff renders is the value Core.Set
// persists.
func TestAfterStateValueMatchesCoreSet(t *testing.T) {
	sandboxStateCLI(t)

	cases := []struct{ key, value string }{
		{"default-profile", "NATIVE"},
		{"default_profile", "prompt"},
		{"bridge-tools", "exec,read"},
		{"bridge_tools", "none"},
		{"native-seat-concurrency", "4"},
		{"native_seat_concurrency", "8"},
		{"language", "KO"},
		{"launcher_menu", "on"},
		{"launcher-menu", "off"},
		// P5-3 T6: the six integration rows project exactly what the store
		// persists, in both key spellings and through the alias set.
		{"integration-preset", "TOOLS"},
		{"integration_preset", " Custom "},
		{"integration-statusline", "on"},
		{"integration_hook", "OFF"},
		{"integration-rules", "1"},
		{"integration_plugins", "0"},
		{"integration-skills", "true"},
	}
	for _, tc := range cases {
		core := settings.NewCore(settings.Deps{VersionFunc: func() string { return "test" }})
		if err := core.Set(tc.key, tc.value); err != nil {
			t.Fatalf("Core.Set(%q, %q) failed: %v", tc.key, tc.value, err)
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		field, known := stateScalarFieldFor(tc.key)
		if !known {
			t.Fatalf("stateScalarFieldFor(%q) unknown; the CLI writes this key", tc.key)
		}
		if persisted, rendered := field.read(st), afterStateValue(tc.key, tc.value); persisted != rendered {
			t.Errorf("%s: the diff renders %q but the store persisted %q", tc.key, rendered, persisted)
		}
	}
}

// TestStateGateUsageNamesYes pins the --yes affordance on every affected help
// surface, so a scripted caller can discover the consent operand.
func TestStateGateUsageNamesYes(t *testing.T) {
	for _, want := range []string{
		"set <key> <value> [--yes]",
		"switch <full|clean> [--yes]",
		"restore [--yes]",
	} {
		if !strings.Contains(proSettingsUsage, want) {
			t.Errorf("proSettingsUsage missing %q:\n%s", want, proSettingsUsage)
		}
	}
	if !strings.Contains(configUsage, "default-profile <prompt|native|gateway> [--yes]") ||
		!strings.Contains(configUsage, "bridge-tools <read,search,write,exec|none> [--yes]") {
		t.Errorf("configUsage must name --yes on both write verbs:\n%s", configUsage)
	}
	if !strings.Contains(proHybridUsage, "[--yes]") {
		t.Errorf("proHybridUsage must name --yes:\n%s", proHybridUsage)
	}
}

// TestStateWriteGate_IntegrationIntentKeys (P5-3 T6): the six integrations
// intent keys ride the SAME F3-gated `settings set` door as the older state
// scalars. The diff names the state.json row, a pipe without --yes writes
// nothing, --yes writes the canonical scalar, and a value that moved under the
// prompt refuses instead of overwriting the external edit.
func TestStateWriteGate_IntegrationIntentKeys(t *testing.T) {
	t.Run("pipe refusal writes nothing", func(t *testing.T) {
		cases := []struct{ key, value, diff string }{
			{"integration-preset", "tools", "integration_preset: (unset) -> tools"},
			{"integration-rules", "on", "integration_rules: (unset) -> on"},
		}
		for _, tc := range cases {
			core := installSettingsCore(t)
			code, out, errw := runSettings(t, []string{"set", tc.key, tc.value}, "")
			if code != 1 {
				t.Fatalf("%s exit = %d; want 1 (stderr=%s)", tc.key, code, errw)
			}
			if !strings.Contains(out, "[agy-pro] Approve State Write") {
				t.Errorf("%s: the diff must print before the refusal:\n%s", tc.key, out)
			}
			if !strings.Contains(out, tc.diff) {
				t.Errorf("%s: the diff must render the persisted row (%q):\n%s", tc.key, tc.diff, out)
			}
			if !strings.Contains(errw, "--yes") || !strings.Contains(errw, "refusal") {
				t.Errorf("%s: the refusal must name --yes:\n%s", tc.key, errw)
			}
			if core.state.IntegrationPreset != "" || core.state.IntegrationRules != "" {
				t.Errorf("%s: a refused write reached the state: %+v", tc.key, core.state)
			}
		}
	})

	t.Run("yes consent writes the scalar", func(t *testing.T) {
		core := installSettingsCore(t)
		code, out, errw := runSettings(t, []string{"set", "integration-preset", "TOOLS", "--yes"}, "")
		if code != 0 {
			t.Fatalf("preset exit = %d (stderr=%s)", code, errw)
		}
		if !strings.Contains(out, "integration_preset: (unset) -> tools") {
			t.Errorf("the diff must render the canonical preset:\n%s", out)
		}
		if core.state.IntegrationPreset != mode.IntegrationPresetTools {
			t.Errorf("state.IntegrationPreset = %q; want tools", core.state.IntegrationPreset)
		}

		code, out, errw = runSettings(t, []string{"set", "integration-rules", "ON", "--yes"}, "")
		if code != 0 {
			t.Fatalf("rules exit = %d (stderr=%s)", code, errw)
		}
		if !strings.Contains(out, "integration_rules: (unset) -> on") {
			t.Errorf("the diff must render the canonical toggle:\n%s", out)
		}
		if core.state.IntegrationRules != mode.IntegrationToggleOn {
			t.Errorf("state.IntegrationRules = %q; want on", core.state.IntegrationRules)
		}
		// The other five rows are untouched by either write.
		if core.state.IntegrationStatusline != "" || core.state.IntegrationHook != "" ||
			core.state.IntegrationPlugins != "" || core.state.IntegrationSkills != "" {
			t.Errorf("a sibling integration row moved: %+v", core.state)
		}
	})

	t.Run("stale base preset refuses", func(t *testing.T) {
		core := installSettingsCore(t)
		stubStateReload(t, func() (*mode.State, error) {
			edited := *core.state
			edited.IntegrationPreset = mode.IntegrationPresetCustom
			core.state = &edited
			return core.state, nil
		})
		code, _, errw := runSettings(t, []string{"set", "integration-preset", "tools", "--yes"}, "")
		if code != 1 {
			t.Fatalf("drifted preset exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if !strings.Contains(errw, "integration_preset in state.json changed on disk") || !strings.Contains(errw, "nothing written") {
			t.Errorf("the refusal must name the drifted row:\n%s", errw)
		}
		if core.state.IntegrationPreset != mode.IntegrationPresetCustom {
			t.Errorf("the external edit was clobbered: %+v", core.state)
		}
	})

	t.Run("stale base feature refuses", func(t *testing.T) {
		core := installSettingsCore(t)
		// The presented value is the absent row; the external edit sets the row
		// while the prompt waits, so the presented "unset -> on" no longer
		// describes the file.
		stubStateReload(t, func() (*mode.State, error) {
			edited := *core.state
			edited.IntegrationRules = mode.IntegrationToggleOff
			core.state = &edited
			return core.state, nil
		})
		code, _, errw := runSettings(t, []string{"set", "integration-rules", "on", "--yes"}, "")
		if code != 1 {
			t.Fatalf("drifted toggle exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if !strings.Contains(errw, "integration_rules in state.json changed on disk") {
			t.Errorf("the refusal must name the drifted row:\n%s", errw)
		}
		if core.state.IntegrationRules != mode.IntegrationToggleOff {
			t.Errorf("the external edit was clobbered: %+v", core.state)
		}
	})

	// A key-shaped operand in an integration slot must not reach the transcript
	// raw: the diff goes through the registry-slot masker (stateValueDisplay)
	// and the store's refusal masks the operand too.
	t.Run("key-shaped operand stays masked", func(t *testing.T) {
		core := installSettingsCore(t)
		code, out, errw := runSettings(t, []string{"set", "integration-preset", maskSweepKey}, "")
		if code != 1 {
			t.Fatalf("key-shaped preset exit = %d; want 1 (stderr=%s)", code, errw)
		}
		for _, body := range []string{out, errw} {
			if strings.Contains(body, maskSweepKey) {
				t.Errorf("the transcript printed the planted key raw:\n%s", body)
			}
		}
		if !strings.Contains(out, "sk-pl****") {
			t.Errorf("the diff must render the masked operand:\n%s", out)
		}
		if core.state.IntegrationPreset != "" {
			t.Errorf("a refused write reached the state: %+v", core.state)
		}
	})

	t.Run("show keeps not listing them", func(t *testing.T) {
		// P5-3 T8 owns the surfaces; until then `settings show` (text and JSON)
		// must not grow a row for the new keys, so the state-only landing changes
		// no displayed output.
		installSettingsCore(t)
		for _, args := range [][]string{{"show"}, {"show", "--json"}} {
			code, out, errw := runSettings(t, args, "")
			if code != 0 {
				t.Fatalf("%v exit = %d (stderr=%s)", args, code, errw)
			}
			body := strings.ToLower(out)
			for _, row := range []string{"integration_preset", "integration_statusline", "integration_rules", "integration_skills"} {
				if strings.Contains(body, row) {
					t.Errorf("%v must not list the integrations intent row %q yet:\n%s", args, row, out)
				}
			}
		}
	})

	t.Run("bogus value refused after consent", func(t *testing.T) {
		core := installSettingsCore(t)
		code, out, errw := runSettings(t, []string{"set", "integration-preset", "all", "--yes"}, "")
		if code != 1 {
			t.Fatalf("bogus preset exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if !strings.Contains(out, "integration_preset: (unset) -> all") {
			t.Errorf("the plan must render the operand it presented:\n%s", out)
		}
		if !strings.Contains(errw, "use native, tools, or custom") {
			t.Errorf("the store must refuse and name the valid presets:\n%s", errw)
		}
		if core.state.IntegrationPreset != "" {
			t.Errorf("a refused write reached the state: %+v", core.state)
		}

		code, _, errw = runSettings(t, []string{"set", "integration-skills", "enabled", "--yes"}, "")
		if code != 1 {
			t.Fatalf("bogus toggle exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if !strings.Contains(errw, "use on, off, 1, or 0") {
			t.Errorf("the store must refuse and name the valid toggles:\n%s", errw)
		}
		if core.state.IntegrationSkills != "" {
			t.Errorf("a refused write reached the state: %+v", core.state)
		}
	})
}
