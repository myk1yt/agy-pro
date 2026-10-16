package fanout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// Plants for the reconcile-sink pins (S1/S4/C2/C7). Each is key-shaped and each
// is distinct, so every field under test is pinned on its own masked rendering
// and a leak of one cannot be hidden by another's mask.
const (
	// s4EnvPlant rides the apiKeyEnv slot (a legacy registry can hold a
	// credential there); the sk- prefix makes it classifier-visible.
	s4EnvPlant = "sk-planted-fanout-env-key-0123456789abcdef"
	// s4URLPlant rides the baseURL slot without a scheme, so it is not
	// location-shaped and stays a value rather than a location.
	s4URLPlant = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	// s4ModelPlant rides a nested models[].id / failover hop slot: hyphen- and
	// slash-decorated, so the contiguous-run rule cannot see it and only the
	// decorated class masks it.
	s4ModelPlant = "1-wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	// s4FamilyPlant rides the explicit model family. Its LOWERCASED fold (what
	// FamilyName() returns) carries no uppercase letter, so the decorated
	// display class cannot see the fold and the folded echo rides raw; the
	// case-preserved value masks as "nativ****" (C2).
	s4FamilyPlant = "native-wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
)

// s4Target builds the Initial/Final target pair the reconcile pins share.
func s4Target() providers.Target {
	prov := providers.Provider{
		Name:      "nw",
		BaseURL:   "https://api.acme.example/v1",
		APIKeyEnv: "NW_API_KEY",
		Models:    []providers.Model{{ID: "glm-5.3-flash", Family: "glm"}},
	}
	return providers.Target{Provider: prov, Model: prov.Models[0]}
}

func s4Seat(t *testing.T, target providers.Target) *SeatResult {
	t.Helper()
	return &SeatResult{
		SeatID:        1,
		InitialTarget: target,
		FinalTarget:   target,
		Status:        "success",
		Content:       "## Findings\n- **[INFO]** ok",
	}
}

func readOutFile(t *testing.T, dir, pattern string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one %s file, got %v (err %v)", pattern, matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	return string(raw)
}

// S1: the Task sink. A task is user input and is normally a whole sentence, and
// whole-string MaskOperand classifies the WHOLE string - a sentence never looks
// secret-shaped, so a bare secret embedded in the task rode raw into
// summary.json, reconciliation.md and the seat-report Task row. MaskSentence
// seeds the redactor with the task plus its per-token forms, so the secret is
// masked wherever the sentence echoes it.
func TestReconcileTaskSinkMasksEmbeddedSecret(t *testing.T) {
	awsPlant := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	task := "audit the config and rotate the key " + awsPlant + " before friday"

	outDir := t.TempDir()
	target := s4Target()
	summary, err := Reconcile(task, t.TempDir(), outDir, 1, []*SeatResult{s4Seat(t, target)})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The in-memory summary (what --json stdout carries).
	if strings.Contains(summary.Task, awsPlant) {
		t.Errorf("summary.Task leaks the planted secret: %q", summary.Task)
	}
	if !strings.Contains(summary.Task, "[REDACTED]") {
		t.Errorf("summary.Task should carry the redaction marker, got %q", summary.Task)
	}

	// summary.json on disk.
	sumRaw := readOutFile(t, outDir, "summary.json")
	if strings.Contains(sumRaw, awsPlant) {
		t.Errorf("summary.json leaks the planted secret:\n%s", sumRaw)
	}

	// The seat-report Task row (the second sink named in S1).
	seatRaw := readOutFile(t, outDir, "seat-*.md")
	if strings.Contains(seatRaw, awsPlant) {
		t.Errorf("seat report leaks the planted secret:\n%s", seatRaw)
	}
	if !strings.Contains(seatRaw, "[REDACTED]") {
		t.Errorf("seat report Task row should carry the redaction marker:\n%s", seatRaw)
	}

	// The narrow-display side of the same rule: a benign multi-word task seeds
	// nothing and must render byte-identical in every sink.
	benign := "review the hybrid swarm model UX initiative and report findings"
	outDir2 := t.TempDir()
	summary2, err := Reconcile(benign, t.TempDir(), outDir2, 1, []*SeatResult{s4Seat(t, target)})
	if err != nil {
		t.Fatalf("Reconcile (benign): %v", err)
	}
	if summary2.Task != benign {
		t.Errorf("benign task was rewritten: %q; want %q", summary2.Task, benign)
	}
	if seatRaw2 := readOutFile(t, outDir2, "seat-*.md"); !strings.Contains(seatRaw2, benign) {
		t.Errorf("benign task must stay readable in the seat report:\n%s", seatRaw2)
	}
}

// S4: the nested Target.Provider serialized into summary.json. The per-field
// masking only covered Provider.Name and Model.ID, so apiKeyEnv, baseURL and
// every nested models[].id rode raw. The whole object now routes through the
// shared report helpers.
func TestReconcileSummaryMasksWholeTarget(t *testing.T) {
	prov := providers.Provider{
		Name:      "nw",
		BaseURL:   s4URLPlant,
		APIKeyEnv: s4EnvPlant,
		Models: []providers.Model{
			{ID: "glm-5.3-flash", Family: "glm"},
			{ID: s4ModelPlant},
		},
	}
	target := providers.Target{Provider: prov, Model: prov.Models[0]}

	outDir := t.TempDir()
	summary, err := Reconcile("review", t.TempDir(), outDir, 1, []*SeatResult{s4Seat(t, target)})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	seat := summary.Seats[0]

	// The fields the per-field masking never reached.
	if got := seat.FinalTarget.Provider.APIKeyEnv; got != "sk-pl****" {
		t.Errorf("summary apiKeyEnv = %q; want %q", got, "sk-pl****")
	}
	if got := seat.FinalTarget.Provider.BaseURL; got != "wJalr****" {
		t.Errorf("summary baseURL = %q; want %q", got, "wJalr****")
	}
	if got := seat.FinalTarget.Provider.Models[1].ID; got != "1-wJa****" {
		t.Errorf("summary nested model id = %q; want %q", got, "1-wJa****")
	}
	// The initial target is masked the same way (the sibling sink).
	if got := seat.InitialTarget.Provider.APIKeyEnv; got != "sk-pl****" {
		t.Errorf("summary initial apiKeyEnv = %q; want %q", got, "sk-pl****")
	}

	// Benign registry values stay readable.
	if got := seat.FinalTarget.Provider.Name; got != "nw" {
		t.Errorf("benign provider name = %q; want nw", got)
	}
	if got := seat.FinalTarget.Model.ID; got != "glm-5.3-flash" {
		t.Errorf("benign model id = %q; want glm-5.3-flash", got)
	}
	if got := seat.FinalTarget.Provider.Models[0].ID; got != "glm-5.3-flash" {
		t.Errorf("benign nested model id = %q; want glm-5.3-flash", got)
	}

	// summary.json on disk must not carry any of the plants raw.
	sumRaw := readOutFile(t, outDir, "summary.json")
	for _, plant := range []string{s4EnvPlant, s4URLPlant, s4ModelPlant} {
		if strings.Contains(sumRaw, plant) {
			t.Errorf("summary.json leaks %q:\n%s", plant, sumRaw)
		}
	}
}

// C7: `copySeat := *s` is a shallow copy, so copySeat.Failovers aliased the
// caller's backing array and the per-hop masking wrote into the CALLER's slice.
// Reconcile must not mutate its input.
func TestReconcileDoesNotMutateCallerFailovers(t *testing.T) {
	reason := `upstream HTTP 401: Bearer sk-ab12cd34ef56gh78 rejected`
	fos := []FailoverRecord{{
		Step:     1,
		FromProv: s4URLPlant,
		FromMod:  "glm-5.3-flash",
		ToProv:   "nw",
		ToMod:    s4ModelPlant,
		Reason:   reason,
	}}
	want := append([]FailoverRecord(nil), fos...)

	target := s4Target()
	seat := s4Seat(t, target)
	seat.Failovers = fos

	outDir := t.TempDir()
	summary, err := Reconcile("review", t.TempDir(), outDir, 1, []*SeatResult{seat})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if !reflect.DeepEqual(fos, want) {
		t.Errorf("Reconcile mutated the caller's Failovers slice:\ngot  %+v\nwant %+v", fos, want)
	}
	// The caller's own seat result must be untouched too.
	if !reflect.DeepEqual(seat.Failovers, want) {
		t.Errorf("Reconcile mutated the caller's seat Failovers:\ngot  %+v\nwant %+v", seat.Failovers, want)
	}

	// The masking still happens - on the summary's copy.
	got := summary.Seats[0].Failovers[0]
	if strings.Contains(got.Reason, "sk-ab12cd34ef56gh78") {
		t.Errorf("summary failover reason leaks the credential: %q", got.Reason)
	}
	if got.FromProv != "wJalr****" {
		t.Errorf("summary failover FromProv = %q; want %q", got.FromProv, "wJalr****")
	}
	if got.ToMod != "1-wJa****" {
		t.Errorf("summary failover ToMod = %q; want %q", got.ToMod, "1-wJa****")
	}
}

// C2: the family display. The seat report rendered MaskOperand(Model.FamilyName()),
// and FamilyName() folds the explicit family to lowercase - which destroys the
// uppercase letter the decorated class requires, so a key-shaped family pasted
// into the registry rode raw. MaskFamilyName masks the case-preserved value.
func TestReconcileFamilyDisplayMasksKeyShapedFamily(t *testing.T) {
	model := providers.Model{ID: "glm-5.3-flash", Family: s4FamilyPlant}
	target := providers.Target{
		Provider: providers.Provider{Name: "nw"},
		Model:    model,
	}

	outDir := t.TempDir()
	summary, err := Reconcile("review", t.TempDir(), outDir, 1, []*SeatResult{s4Seat(t, target)})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The seat-report family row.
	seatRaw := readOutFile(t, outDir, "seat-*.md")
	if strings.Contains(seatRaw, s4FamilyPlant) {
		t.Errorf("seat report leaks the key-shaped family:\n%s", seatRaw)
	}
	if !strings.Contains(seatRaw, "(family: `nativ****`)") {
		t.Errorf("seat report family row should carry the first-5 mask:\n%s", seatRaw)
	}

	// The machine summary carries the same masked family.
	if got := summary.Seats[0].FinalTarget.Model.Family; got != "nativ****" {
		t.Errorf("summary family = %q; want %q", got, "nativ****")
	}
	sumRaw := readOutFile(t, outDir, "summary.json")
	if strings.Contains(sumRaw, s4FamilyPlant) {
		t.Errorf("summary.json leaks the key-shaped family:\n%s", sumRaw)
	}

	// A benign explicit family stays readable (no over-mask).
	benignModel := providers.Model{ID: "glm-5.3-flash", Family: "GLM"}
	outDir2 := t.TempDir()
	summary2, err := Reconcile("review", t.TempDir(), outDir2, 1,
		[]*SeatResult{s4Seat(t, providers.Target{Provider: providers.Provider{Name: "nw"}, Model: benignModel})})
	if err != nil {
		t.Fatalf("Reconcile (benign family): %v", err)
	}
	if got := summary2.Seats[0].FinalTarget.Model.Family; got != "GLM" {
		t.Errorf("benign family was rewritten: %q; want GLM", got)
	}
	if seatRaw2 := readOutFile(t, outDir2, "seat-*.md"); !strings.Contains(seatRaw2, "(family: `GLM`)") {
		t.Errorf("benign family must stay readable in the seat report:\n%s", seatRaw2)
	}
}

// S1 (sibling): the task string must also survive the JSON encoder untouched
// when it is benign - the sink is a machine-readable artifact, so a task that
// seeds nothing must not gain a fence or a mask through the marshal path.
func TestReconcileBenignTaskSurvivesMarshal(t *testing.T) {
	task := "review the fanout reconcile artifact sinks"
	outDir := t.TempDir()
	summary, err := Reconcile(task, t.TempDir(), outDir, 1, []*SeatResult{s4Seat(t, s4Target())})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	raw := readOutFile(t, outDir, "summary.json")
	var parsed struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("summary.json unmarshal: %v", err)
	}
	if parsed.Task != task {
		t.Errorf("summary.json task = %q; want %q", parsed.Task, task)
	}
	if summary.Task != task {
		t.Errorf("summary.Task = %q; want %q", summary.Task, task)
	}
}

// FIX-13: a nil redactor used to pass upstream error text through untouched, so
// a caller that had no redactor of its own wrote a shareable report with the
// provider's credential echo in it. The fallback is now the generic redactor,
// which masks the credential shapes it knows (here a Google API key, the shape
// agy itself hands to whatever endpoint it talks to).
func TestFormatSeatReportNilRedactorMasksCredentialShapes(t *testing.T) {
	const planted = "AIzaSyDELmE1f2g3h4i5j6k7l8m9n0o1p2q3r4s"
	target := providers.Target{
		Provider: providers.Provider{Name: "prov"},
		Model:    providers.Model{ID: "mod"},
	}
	seat := &SeatResult{
		SeatID:        1,
		InitialTarget: target,
		FinalTarget:   target,
		Status:        "failed",
		Error:         `upstream HTTP 401: rejected credential ` + planted,
		Content:       "## Findings\n- **[INFO]** body",
	}

	md := formatSeatReport("ts", "review", seat, nil)
	if strings.Contains(md, planted) {
		t.Errorf("nil redactor leaked the planted credential:\n%s", md)
	}
	if !strings.Contains(md, "[REDACTED]") {
		t.Errorf("nil redactor produced no mask:\n%s", md)
	}

	// An explicit redactor still wins, so a registry-seeded caller keeps its
	// own masking.
	md2 := formatSeatReport("ts", "review", seat, providers.NewRedactor(planted))
	if strings.Contains(md2, planted) {
		t.Errorf("explicit redactor leaked the planted credential:\n%s", md2)
	}
}
