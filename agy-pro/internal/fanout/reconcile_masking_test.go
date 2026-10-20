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

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep, registry-name sinks): every registry-borne
// provider NAME and model ID that reconciles into the shareable artifacts
// renders through providers.MaskSlot, the registry-slot authority. MaskOperand
// is stage one of that authority, so every benign value renders byte-identical;
// only the underscore-head credential class (invisible to the operand masker)
// changes. These pins hold the SERIALIZED surfaces: summary.json fields, the
// reconciliation.md rows and the seat report file name.
// ---------------------------------------------------------------------------

// fix8SlotPlant is a synthetic underscore-head credential-shaped name: the
// class MaskOperand renders RAW and MaskSlot masks to first-5 + "****".
const (
	fix8SlotPlant = "sk_live_0123456789abcdef"
	fix8SlotMask  = "sk_li****"
)

func TestReconcileRegistrySlotsMaskUnderscoreHeadClass(t *testing.T) {
	// The same plant rides the provider NAME and the model ID, plus all four
	// failover-hop slots (provider name and model ID pairs), so a leak of any
	// one slot cannot hide behind another slot's mask.
	prov := providers.Provider{
		Name:      fix8SlotPlant,
		BaseURL:   "https://api.acme.example/v1",
		APIKeyEnv: "NW_API_KEY",
		Models:    []providers.Model{{ID: fix8SlotPlant, Family: "glm"}},
	}
	target := providers.Target{Provider: prov, Model: prov.Models[0]}
	seat := s4Seat(t, target)
	seat.Failovers = []FailoverRecord{{
		Step: 1, FromProv: fix8SlotPlant, FromMod: fix8SlotPlant,
		ToProv: fix8SlotPlant, ToMod: fix8SlotPlant,
		Reason: "upstream 429",
	}}

	outDir := t.TempDir()
	summary, err := Reconcile("review", t.TempDir(), outDir, 1, []*SeatResult{seat})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The summary copy carries the mask on every registry slot.
	got := summary.Seats[0]
	for field, value := range map[string]string{
		"provider name":     got.FinalTarget.Provider.Name,
		"model id":          got.FinalTarget.Model.ID,
		"failover FromProv": got.Failovers[0].FromProv,
		"failover FromMod":  got.Failovers[0].FromMod,
		"failover ToProv":   got.Failovers[0].ToProv,
		"failover ToMod":    got.Failovers[0].ToMod,
	} {
		if value != fix8SlotMask {
			t.Errorf("summary %s = %q; want %q", field, value, fix8SlotMask)
		}
	}
	// The findings matrix attributes each finding to the same slots.
	if len(summary.Issues) == 0 || len(summary.Issues[0].Findings) == 0 {
		t.Fatalf("expected one finding in the matrix, got %+v", summary.Issues)
	}
	if f := summary.Issues[0].Findings[0]; f.Provider != fix8SlotMask || f.Model != fix8SlotMask {
		t.Errorf("finding attribution = %q/%q; want %q on both", f.Provider, f.Model, fix8SlotMask)
	}

	// summary.json on disk never carries the raw value, and carries the mask.
	sumRaw := readOutFile(t, outDir, "summary.json")
	if strings.Contains(sumRaw, fix8SlotPlant) {
		t.Errorf("summary.json leaks the credential-shaped registry slot:\n%s", sumRaw)
	}
	if !strings.Contains(sumRaw, `"name": "`+fix8SlotMask+`"`) {
		t.Errorf("summary.json lost the masked provider name:\n%s", sumRaw)
	}

	// reconciliation.md never carries the raw value; the matrix attributes the
	// finding to the masked registry slots.
	md := readOutFile(t, outDir, "reconciliation.md")
	if strings.Contains(md, fix8SlotPlant) {
		t.Errorf("reconciliation.md leaks the credential-shaped registry slot:\n%s", md)
	}
	if want := "Seat 1 (" + fix8SlotMask + "/" + fix8SlotMask + ")"; !strings.Contains(md, want) {
		t.Errorf("reconciliation.md lost the masked attribution %q:\n%s", want, md)
	}

	// The seat report file name AND body mask the same slots (stars render as
	// dashes in a file name, the I2 rule sanitizeFilename applies).
	seatRaw := readOutFile(t, outDir, "seat-1-*.md")
	if strings.Contains(seatRaw, fix8SlotPlant) {
		t.Errorf("seat report leaks the credential-shaped registry slot:\n%s", seatRaw)
	}
	if !strings.Contains(seatRaw, "| Provider | `"+fix8SlotMask+"` |") {
		t.Errorf("seat report lost the masked provider row:\n%s", seatRaw)
	}
	if !strings.Contains(seatRaw, "Step 1 ("+fix8SlotMask+"/"+fix8SlotMask+" -> ") {
		t.Errorf("seat report lost the masked failover row:\n%s", seatRaw)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, "seat-1-sk_li-----sk_li----.md")); statErr != nil {
		t.Errorf("seat report file name is not the masked pair: %v", statErr)
	}
}

// TestReconcileBenignRegistrySlotsRenderByteIdentically pins the other half of
// the conversion: MaskSlot is a superset of MaskOperand, so a benign registry
// corpus (the sibling sweep's corpus plus the values these pins already use)
// keeps its exact bytes in every serialized surface.
func TestReconcileBenignRegistrySlotsRenderByteIdentically(t *testing.T) {
	for _, name := range []string{"openai", "gemini", "deepseek", "nw", "charm", "acme-corp"} {
		prov := providers.Provider{Name: name, APIKeyEnv: "NW_API_KEY", Models: []providers.Model{{ID: "glm-5.3-flash", Family: "glm"}}}
		target := providers.Target{Provider: prov, Model: prov.Models[0]}
		seat := s4Seat(t, target)
		seat.Failovers = []FailoverRecord{{Step: 1, FromProv: name, FromMod: "glm-5.3-flash", ToProv: "nw", ToMod: "glm-5.3-flash", Reason: "upstream 429"}}

		outDir := t.TempDir()
		summary, err := Reconcile("review", t.TempDir(), outDir, 1, []*SeatResult{seat})
		if err != nil {
			t.Fatalf("%s: Reconcile: %v", name, err)
		}
		got := summary.Seats[0]
		if got.FinalTarget.Provider.Name != name || got.FinalTarget.Model.ID != "glm-5.3-flash" {
			t.Errorf("%s: benign target drifted to %q/%q", name, got.FinalTarget.Provider.Name, got.FinalTarget.Model.ID)
		}
		if got.Failovers[0].FromProv != name || got.Failovers[0].ToProv != "nw" {
			t.Errorf("%s: benign hop provider drifted to %q/%q", name, got.Failovers[0].FromProv, got.Failovers[0].ToProv)
		}
		if got.Failovers[0].FromMod != "glm-5.3-flash" || got.Failovers[0].ToMod != "glm-5.3-flash" {
			t.Errorf("%s: benign hop model drifted to %q/%q", name, got.Failovers[0].FromMod, got.Failovers[0].ToMod)
		}

		seatRaw := readOutFile(t, outDir, "seat-1-*.md")
		for _, want := range []string{
			"| Provider | `" + name + "` |",
			"| Model | `glm-5.3-flash` (family: `glm`) |",
			"Step 1 (" + name + "/glm-5.3-flash -> nw/glm-5.3-flash: upstream 429)",
		} {
			if !strings.Contains(seatRaw, want) {
				t.Errorf("%s: seat report lost the byte-identical row %q:\n%s", name, want, seatRaw)
			}
		}
		if _, statErr := os.Stat(filepath.Join(outDir, "seat-1-"+name+"-glm-5.3-flash.md")); statErr != nil {
			t.Errorf("%s: seat report file name drifted: %v", name, statErr)
		}
	}
}

// TestMergePartitionRegistrySlotsMaskFailoverHops pins the partition twin
// (maskedPartitionSeat): its failover hops land in the shareable summary.json
// of a partition run, so the same registry-slot authority masks them.
func TestMergePartitionRegistrySlotsMaskFailoverHops(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "alpha", "ok.go"), []byte("package alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	units := []Unit{{ID: "unit-alpha", Title: "Alpha work", Scope: []string{"src/alpha"}, Deliverable: "d", Seat: "prov-a/model-a"}}
	prov := providers.Provider{
		Name:      fix8SlotPlant,
		BaseURL:   "https://api.acme.example/v1",
		APIKeyEnv: "NW_API_KEY",
		Models:    []providers.Model{{ID: fix8SlotPlant}},
	}
	target := providers.Target{Provider: prov, Model: prov.Models[0]}
	results := []*SeatResult{{
		SeatID:        1,
		UnitID:        "unit-alpha",
		InitialTarget: target,
		FinalTarget:   target,
		Status:        "success",
		Content:       "WORK-PRODUCT-A: plain analysis prose for the alpha unit",
		Failovers: []FailoverRecord{{
			Step: 1, FromProv: fix8SlotPlant, FromMod: fix8SlotPlant,
			ToProv: fix8SlotPlant, ToMod: fix8SlotPlant,
			Reason: "upstream 429",
		}},
	}}

	outDir := filepath.Join(t.TempDir(), "reports")
	if _, err := MergePartition("review", root, outDir, units, results); err != nil {
		t.Fatalf("MergePartition: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if err != nil {
		t.Fatalf("read summary.json: %v", err)
	}
	if strings.Contains(string(raw), fix8SlotPlant) {
		t.Errorf("partition summary.json leaks the credential-shaped registry slot:\n%s", raw)
	}
	var s SummaryReport
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("summary.json is not parseable: %v\n%s", err, raw)
	}
	if len(s.Seats) != 1 || len(s.Seats[0].Failovers) != 1 {
		t.Fatalf("partition summary seats = %+v; want one seat with one hop", s.Seats)
	}
	hop := s.Seats[0].Failovers[0]
	for field, value := range map[string]string{
		"FromProv": hop.FromProv, "FromMod": hop.FromMod,
		"ToProv": hop.ToProv, "ToMod": hop.ToMod,
	} {
		if value != fix8SlotMask {
			t.Errorf("partition summary hop %s = %q; want %q", field, value, fix8SlotMask)
		}
	}
	// The benign target of the same seat stays readable (the slot authority is
	// identity on every benign value).
	if got := s.Seats[0].UnitID; got != "unit-alpha" {
		t.Errorf("partition summary unit id = %q; want unit-alpha", got)
	}
}
