package fanout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// S-7: upstream error snippets (up to 500 bytes of the provider's error body)
// can echo a truncated bearer token. Everything written into the shareable
// reports must pass the Redactor first.
func TestFormatSeatReportRedactsErrorSnippets(t *testing.T) {
	target := providers.Target{
		Provider: providers.Provider{Name: "prov"},
		Model:    providers.Model{ID: "mod"},
	}
	seat := &SeatResult{
		SeatID:        1,
		InitialTarget: target,
		FinalTarget:   target,
		Status:        "failed",
		Error:         `upstream HTTP 401: {"error":{"message":"invalid API key sk-ab12cd34ef56gh78"}}`,
		Failovers: []FailoverRecord{{
			Step: 1, FromProv: "prov", FromMod: "mod", ToProv: "prov2", ToMod: "mod2",
			Reason: `Bearer sk-ab12cd34ef56gh78 rejected`,
		}},
		Content: "## Findings\n- **[INFO]** body",
	}

	md := formatSeatReport("ts", "review", seat, providers.NewRedactor())
	if strings.Contains(md, "sk-ab12cd34ef56gh78") {
		t.Fatalf("seat report leaks the credential fragment:\n%s", md)
	}
	if !strings.Contains(md, "[REDACTED]") {
		t.Fatalf("seat report has no redaction marker:\n%s", md)
	}
	// The redaction covers the error row and the failover reason.
	if strings.Count(md, "[REDACTED]") < 2 {
		t.Fatalf("expected both error and failover reason redacted:\n%s", md)
	}
}

// S-7: summary.json copies the seat results; the error fields must be
// redacted there too, not only in the markdown.
func TestReconcileSummaryRedactsErrorSnippets(t *testing.T) {
	outDir := t.TempDir()
	target := providers.Target{
		Provider: providers.Provider{Name: "prov"},
		Model:    providers.Model{ID: "mod"},
	}
	seats := []*SeatResult{{
		SeatID:        1,
		InitialTarget: target,
		FinalTarget:   target,
		Status:        "failed",
		Error:         `network error calling prov (http://127.0.0.1:1/chat/completions): Bearer sk-zz99yy88xx77ww66 rejected`,
		Content:       "## Findings\n- **[INFO]** body",
	}}

	summary, err := Reconcile("review", t.TempDir(), outDir, 1, seats)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(summary.Seats) != 1 {
		t.Fatalf("summary seats = %d; want 1", len(summary.Seats))
	}
	if strings.Contains(summary.Seats[0].Error, "sk-zz99yy88xx77ww66") {
		t.Errorf("summary seat error leaks the credential fragment: %q", summary.Seats[0].Error)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if err != nil {
		t.Fatalf("summary.json: %v", err)
	}
	if strings.Contains(string(raw), "sk-zz99yy88xx77ww66") {
		t.Fatalf("summary.json leaks the credential fragment:\n%s", raw)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("summary.json is not valid JSON: %v", err)
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Errorf("summary.json has no redaction marker:\n%s", raw)
	}
}

// R2-2: the report redactor must cover the registered provider keys of the
// run, not only generic shapes - a raw non-pattern key echoed into seat
// content or errors has to be masked too.
func TestReconcileRedactsRegisteredKeys(t *testing.T) {
	const rawKey = "deadbeefdeadbeefdeadbeefdeadbeef" // hex, no sk-/bearer shape
	t.Setenv("W6_R2_KEY", rawKey)

	outDir := t.TempDir()
	target := providers.Target{
		Provider: providers.Provider{Name: "prov", APIKeyEnv: "W6_R2_KEY"},
		Model:    providers.Model{ID: "mod"},
	}
	seats := []*SeatResult{{
		SeatID: 1, InitialTarget: target, FinalTarget: target,
		Status:  "success",
		Error:   "upstream rejected key " + rawKey,
		Content: "## Findings\n- **[INFO]** credential used: " + rawKey,
	}}

	summary, err := Reconcile("review", t.TempDir(), outDir, 1, seats)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if strings.Contains(summary.Seats[0].Error, rawKey) || strings.Contains(summary.Seats[0].Content, rawKey) {
		t.Errorf("registered key survived in the summary seat copy: %+v", summary.Seats[0])
	}

	for _, name := range []string{"summary.json", "reconciliation.md"} {
		raw, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(raw), rawKey) {
			t.Errorf("%s leaks the registered key:\n%s", name, raw)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "seat-*.md"))
	if len(matches) != 1 {
		t.Fatalf("expected one seat report, got %v", matches)
	}
	seatRaw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(seatRaw), rawKey) {
		t.Errorf("seat report leaks the registered key:\n%s", seatRaw)
	}
	if !strings.Contains(string(seatRaw), "[REDACTED]") {
		t.Errorf("seat report has no redaction marker:\n%s", seatRaw)
	}
}

// S-4/F2: every generated report is requested owner-only (0600). The write
// seam is what can be asserted on Windows, where mode bits do not round-trip
// through Stat.
func TestReconcileWritesReportsOwnerOnly(t *testing.T) {
	type writeCall struct {
		path string
		perm os.FileMode
	}
	var calls []writeCall
	old := writeFile
	writeFile = func(path string, data []byte, perm os.FileMode) error {
		calls = append(calls, writeCall{path, perm})
		return old(path, data, perm)
	}
	t.Cleanup(func() { writeFile = old })

	outDir := t.TempDir()
	target := providers.Target{
		Provider: providers.Provider{Name: "prov"},
		Model:    providers.Model{ID: "mod"},
	}
	seats := []*SeatResult{{
		SeatID: 1, InitialTarget: target, FinalTarget: target,
		Status: "success", Content: "## Findings\n- **[INFO]** ok",
	}}
	if _, err := Reconcile("review", t.TempDir(), outDir, 1, seats); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("expected 3 report writes (seat, summary, reconciliation), got %d: %+v", len(calls), calls)
	}
	for _, c := range calls {
		if c.perm != 0o600 {
			t.Errorf("report %s requested mode %04o; want 0600", filepath.Base(c.path), c.perm)
		}
	}
	// The files must actually exist on disk after the seam writes through.
	for _, c := range calls {
		if _, err := os.Stat(c.path); err != nil {
			t.Errorf("report %s not written: %v", c.path, err)
		}
	}
}
