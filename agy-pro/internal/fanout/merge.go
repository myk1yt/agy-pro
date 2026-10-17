package fanout

// Partition merge step (work items B5 + B6): the back half of PARTITION mode.
//
// A partition unit produces a WORK PRODUCT (an analysis/report in
// SeatResult.Content), not a findings list, so the merge is deliberately NOT
// the findings reconciliation path: it concatenates the per-unit deliverables
// into one merged artifact, expands every unit's scope globs into the concrete
// file list the unit claims, and runs the authoritative disjointness scan on
// those claims. None of the findings machinery (the findings parser, the
// thin-seat classifier, the severity clustering) is consulted anywhere in this
// file: a work product must never be classified "thin" or dropped from a
// denominator.
//
// B6 conflict policy: disjointness is an INPUT ASSUMPTION of a partition plan,
// so any overlap (a file claimed by two or more units) or any verification
// failure FAILS THE RUN with a conflict/verification report. The overlap is
// never silently merged into one unit and no winner is silently picked; the
// merged artifact is still written for inspection, but the run status is
// failed. The happy path (disjoint + verification passed) is a success run
// whose single deliverable is the merged artifact.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

// PartitionManifest is the merged manifest of one PARTITION-mode run (B5). It
// rides inside summary.json as an additive omitempty block, so a
// cross-verification summary stays byte-identical to before the field existed.
type PartitionManifest struct {
	// Mode is always "partition" (a self-describing block for machine readers).
	Mode string `json:"mode"`
	// Status is "merged" on the happy path and "failed" when the B6 conflict
	// policy failed the run (conflicts present or verification failed).
	Status string `json:"status"`
	// Artifact is the merged deliverable file name inside the run's output
	// directory (merged.md): the single deliverable of a successful run.
	Artifact string `json:"artifact"`
	// Units lists every plan unit in PLAN ORDER (never reordered, never
	// dropped) with the concrete files its scope claims.
	Units []PartitionUnitEntry `json:"units"`
	// Conflicts lists every file claimed by two or more units. It is an EMPTY
	// ARRAY (not absent) when the plan is disjoint: the happy path is explicit,
	// not implicit. The rows are written through the artifact redactor (E1):
	// the schema and field names stay unchanged, but a secret-shaped unit id or
	// path renders masked, so a key-shaped plan value can never persist raw
	// into summary.json on a failed run.
	Conflicts []PartitionConflict `json:"conflicts"`
	// Verification records the pass/fail verdict of the three merge checks.
	Verification PartitionVerification `json:"verification"`
}

// PartitionUnitEntry is one plan unit's row in the merged manifest.
type PartitionUnitEntry struct {
	UnitID string `json:"unit_id"`
	Title  string `json:"title"`
	// Files is the sorted, forward-slash, root-relative list of concrete files
	// the unit's scope globs claim (see expandClaimedFiles). It is never
	// derived from free-form seat prose: that would be model-dependent and
	// non-deterministic.
	Files []string `json:"files"`
	// Status is the unit's seat status ("success", "failed", ...); "missing"
	// when the seat produced no result at all.
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
}

// PartitionConflict names one file claimed by two or more units. Units is
// sorted; the run fails on any entry (B6).
type PartitionConflict struct {
	Path  string   `json:"path"`
	Units []string `json:"units"`
}

// PartitionVerification is the merge-time self-check verdict. Every check
// must pass for the run to succeed; a false check fails the run (B6) with the
// report naming what broke.
type PartitionVerification struct {
	// UnitsDelivered: every unit contributed a non-empty deliverable.
	UnitsDelivered bool `json:"units_delivered"`
	// EachUnitInArtifactOnce: every unit appears in the merged artifact under
	// its own heading exactly once (nothing silently dropped or duplicated).
	EachUnitInArtifactOnce bool `json:"each_unit_in_artifact_once"`
	// NoSilentDoubleAssignment: the conflict list reports EXACTLY the files
	// claimed by two or more units (an overlapped file is always reported,
	// never silently folded into one unit, and no conflict is invented for a
	// file with a single claimant).
	NoSilentDoubleAssignment bool `json:"no_silent_double_assignment"`
	// Passed is the AND of the three checks.
	Passed bool `json:"passed"`
}

const (
	// partitionArtifactName is the merged deliverable file inside the run's
	// output directory.
	partitionArtifactName = "merged.md"

	partitionStatusMerged = "merged"
	partitionStatusFailed = "failed"

	// partitionUnitHeading prefixes the per-unit section of the merged
	// artifact. The verification scan anchors on this exact shape.
	partitionUnitHeading = "## Unit "
)

// MergePartition merges one PARTITION-mode run: it concatenates the per-unit
// deliverables into the merged artifact, expands every unit's claimed files,
// scans for overlaps, verifies the merge, and writes summary.json plus the
// human-readable partition report (reconciliation.md) next to the artifact.
//
// units and results are aligned: results[i] is the seat result of units[i].
//
// B6 policy: when the conflict list is non-empty or any verification check
// fails, the run is FAILED. The merged artifact, summary.json and the report
// are still written for inspection, and a non-nil error naming the conflicting
// files/units (or the failed checks) is returned alongside the summary. The
// happy path returns the summary with Partition.Status == "merged" and a nil
// error.
func MergePartition(task, root, outDir string, units []Unit, results []*SeatResult) (*SummaryReport, error) {
	if len(units) == 0 {
		return nil, fmt.Errorf("partition merge: the plan carries no units")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create output directory %s: %w", outDir, err)
	}

	ts := time.Now().UTC().Format(time.RFC3339)

	// S-7 / R2-2: the merged artifact and the summary quote seat output
	// verbatim, so everything that leaves through a report goes through the
	// same registry redactor the cross-verification path uses.
	redact := seatRedactor(results)
	// D3: unit id/title/seat are PLAN-AUTHORED strings that echo verbatim into
	// the merged artifact headings, the report table, summary.json's
	// partition block and the run-failure error. A misdirected key paste in
	// one of those fields must mask exactly the way the task string does, so
	// the fields feed the same seed-eligibility rule and only secret-shaped
	// values seed; benign ids/titles stay readable.
	redact = planFieldRedactor(units, redact)

	// 1. Expand every unit's scope globs into the concrete file list it
	// claims. The walk runs BEFORE any artifact is written, so this run's own
	// outputs can never appear in its claims. The claim test is the read
	// filter's two views (guard.go): the LEXICAL PathInScope on the
	// root-relative path plus the symlink-RESOLVED real path, so the audit's
	// ownership verdict can never disagree with what a seat was allowed to
	// read.
	claims, err := expandClaimedFiles(root, units)
	if err != nil {
		return nil, err
	}

	// 2. The authoritative disjointness scan (the plan parser defers to it):
	// a file claimed by two or more units is an overlap. Possible in principle
	// when a library caller bypassed the plan parser, or the parse-time glob
	// probe's blind spot / budget fail-safe admitted a plan.
	conflicts := scanPartitionConflicts(units, claims)

	// 3. Concatenate the per-unit deliverables in PLAN ORDER, one heading per
	// unit. No silent reordering, no dropping.
	mergedMD := buildMergedArtifact(task, ts, units, results, redact)

	// 4. The verification pass re-derives the merge's invariants from the
	// BUILT artifact (not from the builder's intent), so a builder bug fails
	// the run instead of shipping a silently broken merge.
	verification := verifyPartitionMerge(units, results, mergedMD, claims, conflicts, redact)

	// 5. The merged artifact is written even on a failed run: B6 keeps it for
	// inspection.
	mergedPath := filepath.Join(outDir, partitionArtifactName)
	if err := writeFile(mergedPath, []byte(mergedMD), reportFilePerm); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", mergedPath, err)
	}

	// 6. Assemble the summary and the merged manifest block.
	failed := len(conflicts) > 0 || !verification.Passed
	status := partitionStatusMerged
	if failed {
		status = partitionStatusFailed
	}
	manifest := &PartitionManifest{
		Mode:         "partition",
		Status:       status,
		Artifact:     partitionArtifactName,
		Units:        partitionUnitEntries(units, results, claims, redact),
		Conflicts:    redactPartitionConflicts(conflicts, redact),
		Verification: verification,
	}

	nCompleted := 0
	degraded := false
	var usage usageTotals
	for i, s := range results {
		if s == nil {
			continue
		}
		if i < len(units) && s.Status == "success" && strings.TrimSpace(s.Content) != "" {
			nCompleted++
		}
		if s.Degraded {
			degraded = true
		}
		usage.addSeat(s.Usage)
	}

	summary := &SummaryReport{
		// I4/S1: the task string is user input and usually a whole sentence, so
		// it is masked exactly the way the cross-verification summary masks it.
		Task:          providers.MaskSentence(task, providers.FlagSeedOperands(task)...),
		Timestamp:     ts,
		Root:          root,
		NRequested:    len(units),
		NCompleted:    nCompleted,
		Degraded:      degraded,
		Usage:         usage.runUsage(),
		UntrustedData: untrustedDataNotice,
		Partition:     manifest,
	}
	for _, s := range results {
		if s == nil {
			continue
		}
		summary.Seats = append(summary.Seats, maskedPartitionSeat(s, redact))
	}

	// 7. summary.json: the machine manifest, now carrying the merged block.
	summaryData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return nil, err
	}
	summaryData = append(summaryData, '\n')
	summaryPath := filepath.Join(outDir, "summary.json")
	if err := writeFile(summaryPath, summaryData, reportFilePerm); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", summaryPath, err)
	}

	// 8. The human-readable partition report. It is written at the
	// reconciliation.md path the CLI banner advertises, so a partition run
	// names a real file there; its body is the merge report (artifact
	// reference, per-unit claim counts, verification verdict, and the conflict
	// section on a failed run).
	reportPath := filepath.Join(outDir, "reconciliation.md")
	reportMD := formatPartitionReport(task, ts, units, results, claims, manifest, redact)
	if err := writeFile(reportPath, []byte(reportMD), reportFilePerm); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", reportPath, err)
	}

	// 9. B6: an overlap or a failed verification FAILS THE RUN. The artifacts
	// above are already on disk for inspection.
	if failed {
		return summary, partitionFailureError(units, results, conflicts, verification, redact)
	}
	return summary, nil
}

// planFieldRedactor composes the D3 plan-field seeding over the base seat
// redactor. The seed list is every unit's id, title and seat (plus the parsed
// seat's provider/model tokens); only providers.SeedEligible operands seed
// (the ONE shared seed-eligibility rule the CLI composite-error path uses), so
// a key-shaped field masks everywhere the artifacts/errors echo it while a
// benign "auth-review" id or a plain-English title stays byte-identical. The
// base redactor still runs underneath, so the provider-key masking is
// unchanged.
func planFieldRedactor(units []Unit, base func(string) string) func(string) string {
	seeds := planFieldSeeds(units)
	if len(seeds) == 0 {
		return base
	}
	seeded := providers.NewRedactor(seeds...)
	return func(s string) string {
		return seeded(base(s))
	}
}

// planFieldSeeds renders the D3/E2 seed candidates from the plan-authored unit
// fields: id, title, the seat spec with its provider/model tokens (the
// "/"-split halves FlagSeedOperands does not produce), and every scope entry
// (E2: the parse-time validators, the seat-resolution errors and the runner
// scope refusal quote scope entries verbatim). Each operand plus its trimmed
// form seeds when SeedEligible says the operand is secret-shaped, so benign
// ids/titles and location-shaped scope globs ("src/alpha") never seed and stay
// byte-identical readable.
func planFieldSeeds(units []Unit) []string {
	var operands []string
	for _, u := range units {
		operands = append(operands, u.ID, u.Title, u.Seat)
		operands = append(operands, u.Scope...)
		if u.Seat != "" {
			operands = append(operands, strings.Split(u.Seat, "/")...)
		}
	}
	return providers.SeedList(providers.FlagSeedOperands(operands...))
}

// expandClaimedFiles deterministically enumerates the concrete files every
// unit claims: filepath.WalkDir over the workspace root (lexical visit order),
// each root-relative forward-slash path tested against the unit's scope
// through agenttools.PathInScope. Only regular files count (a directory match
// is a prefix, not a claim). The returned slices align with the plan order of
// units; every list is sorted, so the output is byte-deterministic for a given
// workspace + plan.
//
// D5: the path-reading tools FOLLOW symlinks, so a unit's file ownership must
// be judged in real-path space too. Every walked file is claimed by its
// symlink-RESOLVED real path (agenttools.ResolvedRelPath, the same resolution
// the read filter's resolvedPathInScope test uses): a unit claims the file
// when EITHER the lexical path or the resolved real path is in its scope, and
// the claim is recorded under the real path. An in-scope symlink to another
// unit's real file is therefore claimed by BOTH the lexical owner and the real
// owner under the one real path, so the overlap scan reports it instead of
// hiding it. On the happy path (no symlinks) the resolved path IS the lexical
// path, so the claims are byte-identical to the pre-D5 output; a resolution
// error falls back to the lexical path (the file claims exactly what it would
// have claimed before).
//
// E3: the WALK runs on the symlink-resolved root. When the workspace root
// itself is a symlink/junction, WalkDir sees the root entry as a link (not a
// dir) and visits nothing, so the disjointness audit would be vacuous while
// the run still reported "merged"; EvalSymlinks for the walk closes that (a
// resolution error falls back to the logical root, the pre-E3 behavior). The
// claim paths are still emitted relative to the logical root: the resolved
// walk root denotes the SAME directory, so a walk-root-relative path is the
// logical-root-relative path, and PathInScope keeps speaking in the scope
// entries' root-relative space. On the happy path walkRoot == rootAbs and the
// output is byte-identical to the pre-E3 walk.
func expandClaimedFiles(root string, units []Unit) ([][]string, error) {
	claims := make([][]string, len(units))
	seen := make([]map[string]bool, len(units))
	for i := range claims {
		claims[i] = []string{}
		seen[i] = map[string]bool{}
	}
	// The walk and the resolution must share one absolute root so the resolved
	// real path lands in the same root-relative space as the lexical claims.
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("partition merge: failed to resolve workspace root %s: %w", root, err)
	}
	// E3: resolve the root for the walk itself (see the doc comment).
	walkRoot := rootAbs
	if resolved, rerr := filepath.EvalSymlinks(rootAbs); rerr == nil && resolved != "" {
		walkRoot = resolved
	}
	walkErr := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// The claim stays relative to the logical root: the resolved walk root
		// denotes the same directory, so this IS the logical-root-relative path
		// (and identical to the pre-E3 value when the root was not linked).
		rel, err := filepath.Rel(walkRoot, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		realRel := rel
		if rr, rerr := agenttools.ResolvedRelPath(walkRoot, p); rerr == nil {
			realRel = filepath.ToSlash(rr)
		}
		for i := range units {
			// The read filter's two views (guard.go): the lexical
			// PathInScope on the root-relative path, or the resolved
			// PathInScope on the real path. Either view puts the file's
			// content in the unit's reach, so the unit claims the real path.
			if !agenttools.PathInScope(rel, units[i].Scope) && !agenttools.PathInScope(realRel, units[i].Scope) {
				continue
			}
			// The real-path key can collide with another walked entry (a
			// symlink and its target are two walk entries, one real file);
			// dedupe so a unit never claims the same real path twice.
			if seen[i][realRel] {
				continue
			}
			seen[i][realRel] = true
			claims[i] = append(claims[i], realRel)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("partition merge: failed to walk workspace root %s: %w", root, walkErr)
	}
	for i := range claims {
		sort.Strings(claims[i])
	}
	return claims, nil
}

// scanPartitionConflicts is the authoritative disjointness check: every file
// claimed by two or more units becomes one conflict naming the sorted
// claiming-unit ids. The list is sorted by path; the result is always a
// non-nil EMPTY slice on the happy path, so summary.json shows an explicit
// "conflicts": [] instead of an absent key.
func scanPartitionConflicts(units []Unit, claims [][]string) []PartitionConflict {
	claimants := map[string][]string{}
	for i := range units {
		for _, f := range claims[i] {
			claimants[f] = append(claimants[f], units[i].ID)
		}
	}
	conflicts := []PartitionConflict{}
	for f, ids := range claimants {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		conflicts = append(conflicts, PartitionConflict{Path: f, Units: ids})
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Path < conflicts[j].Path })
	return conflicts
}

// redactPartitionConflicts renders the manifest's conflict rows through the
// artifact redactor before they reach summary.json (E1, the HIGH-1 hole):
// scanPartitionConflicts builds its rows from the RAW unit ids and claim
// paths, and a key-shaped unit id on a CONFLICT run would otherwise persist
// verbatim into partition.conflicts[].units (and a credential-shaped run
// inside the path into partition.conflicts[].path) even though every other
// sink masks it. The manifest schema is unchanged (same field names, same
// row order); only secret-shaped values render masked, and the raw slice is
// untouched so verifyPartitionMerge still compares claim paths in raw space.
// The result stays a non-nil empty slice on the happy path, so summary.json
// keeps the explicit "conflicts": [].
func redactPartitionConflicts(conflicts []PartitionConflict, redact func(string) string) []PartitionConflict {
	out := make([]PartitionConflict, len(conflicts))
	for i, c := range conflicts {
		out[i].Path = redact(c.Path)
		out[i].Units = make([]string, len(c.Units))
		for j, id := range c.Units {
			out[i].Units[j] = redact(id)
		}
	}
	return out
}

// buildMergedArtifact concatenates the per-unit deliverables into ONE markdown
// artifact: each unit's (redacted) SeatResult.Content under a single
// "## Unit <id>: <title>" heading, emitted in PLAN ORDER. Every deliverable
// body is untrusted model output, so the whole concatenation rides behind the
// A12 fence. The heading's id/title are plan-authored strings (D3), so they
// ride through the same redactor: a key-shaped id or title renders masked in
// the artifact, and the verification anchor (countUnitHeadings) recomputes the
// identical redacted heading.
func buildMergedArtifact(task, ts string, units []Unit, results []*SeatResult, redact func(string) string) string {
	var sb strings.Builder
	sb.WriteString("# Merged Partition Deliverables\n\n")
	sb.WriteString(fmt.Sprintf("**Task:** `%s` | **Date:** `%s` | **Units:** `%d`\n\n",
		providers.MaskSentence(task, providers.FlagSeedOperands(task)...), ts, len(units)))
	sb.WriteString(untrustedFence("every unit deliverable below"))
	for i, u := range units {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		sb.WriteString(partitionUnitHeading + redact(u.ID) + ": " + redact(u.Title) + "\n\n")
		content := ""
		if i < len(results) && results[i] != nil {
			content = results[i].Content
		}
		sb.WriteString(redact(content))
		sb.WriteString("\n")
	}
	return sb.String()
}

// verifyPartitionMerge re-derives the merge invariants from the BUILT artifact
// and the claimed-files map:
//
//   - UnitsDelivered: every unit contributed a non-empty deliverable (a
//     missing/empty result means the unit's work product does not exist).
//   - EachUnitInArtifactOnce: every unit id appears as a merged-artifact
//     heading exactly once (nothing silently dropped or duplicated).
//   - NoSilentDoubleAssignment: the conflict list reports exactly the files
//     claimed by two or more units - an overlapped file is always reported,
//     never silently folded into one unit, and no conflict is invented for a
//     file with a single claimant.
func verifyPartitionMerge(units []Unit, results []*SeatResult, merged string, claims [][]string, conflicts []PartitionConflict, redact func(string) string) PartitionVerification {
	v := PartitionVerification{
		UnitsDelivered:           true,
		EachUnitInArtifactOnce:   true,
		NoSilentDoubleAssignment: true,
	}

	for i := range units {
		if i >= len(results) || results[i] == nil || results[i].Status != "success" || strings.TrimSpace(results[i].Content) == "" {
			v.UnitsDelivered = false
			break
		}
	}
	for i := range units {
		if countUnitHeadings(merged, units[i], redact) != 1 {
			v.EachUnitInArtifactOnce = false
			break
		}
	}

	claimCount := map[string]int{}
	for i := range units {
		for _, f := range claims[i] {
			claimCount[f]++
		}
	}
	reported := map[string]bool{}
	for _, c := range conflicts {
		reported[c.Path] = true
	}
	for f, n := range claimCount {
		if (n >= 2) != reported[f] {
			v.NoSilentDoubleAssignment = false
			break
		}
	}

	v.Passed = v.UnitsDelivered && v.EachUnitInArtifactOnce && v.NoSilentDoubleAssignment
	return v
}

// countUnitHeadings counts the merged-artifact section headings of one unit.
// The anchor is the unit's WHOLE heading line ("## Unit <id>: <title>") after
// the same artifact redaction the builder applied (D3), so a key-shaped id is
// masked in the anchor exactly as in the artifact. Matching the full line
// (D2) also closes the colon-aliasing hole of the old "## Unit <id>:" prefix
// anchor: id "api" used to match the heading "## Unit api:v2: ..." of unit
// "api:v2", so a healthy plan with colon ids failed and a dropped section of
// the aliased unit went uncaught. Deliverable prose that reproduces the full
// heading line fails the check CONSERVATIVELY (into a failed run), never into
// a silently wrong merge.
func countUnitHeadings(merged string, u Unit, redact func(string) string) int {
	anchor := partitionUnitHeading + redact(u.ID) + ": " + redact(u.Title)
	n := 0
	for _, line := range strings.Split(merged, "\n") {
		if line == anchor {
			n++
		}
	}
	return n
}

// partitionUnitEntries builds the manifest's per-unit rows in PLAN ORDER. The
// id/title are plan-authored strings (D3), so the values written into
// summary.json's partition block ride through the artifact redactor (the
// field names stay; only secret-shaped values render masked). The Files
// entries ride the same redactor: a claim path can embed a credential-shaped
// file name, which must not persist raw into summary.json either (E1). The
// input claims slice is untouched (verification reads it in raw space).
func partitionUnitEntries(units []Unit, results []*SeatResult, claims [][]string, redact func(string) string) []PartitionUnitEntry {
	entries := make([]PartitionUnitEntry, 0, len(units))
	for i, u := range units {
		files := make([]string, len(claims[i]))
		for j, f := range claims[i] {
			files[j] = redact(f)
		}
		e := PartitionUnitEntry{
			UnitID: redact(u.ID),
			Title:  redact(u.Title),
			Files:  files,
			Status: "missing", // no seat result: the unit never ran
		}
		if i < len(results) && results[i] != nil {
			e.Status = results[i].Status
			e.DurationMs = results[i].DurationMs
		}
		entries = append(entries, e)
	}
	return entries
}

// maskedPartitionSeat renders the summary copy of one seat result for a
// partition run. It applies the same masking the cross-verification summary
// uses (S-7 redaction, masked targets and failover hops, copy-on-write
// failovers) but deliberately derives NO thin flag: a partition deliverable is
// a work product and is never classified by the findings machinery.
func maskedPartitionSeat(s *SeatResult, redact func(string) string) SeatResult {
	copySeat := *s
	copySeat.Failovers = append([]FailoverRecord(nil), s.Failovers...)
	copySeat.UnitID = redact(copySeat.UnitID) // plan-authored id (D3): mask key-shaped values
	copySeat.Error = redact(copySeat.Error)
	copySeat.Content = redact(copySeat.Content)
	copySeat.InitialTarget.Provider = providers.MaskProviderForReport(s.InitialTarget.Provider)
	copySeat.InitialTarget.Model = providers.MaskModelForReport(s.InitialTarget.Model)
	copySeat.FinalTarget.Provider = providers.MaskProviderForReport(s.FinalTarget.Provider)
	copySeat.FinalTarget.Model = providers.MaskModelForReport(s.FinalTarget.Model)
	for i := range copySeat.Failovers {
		copySeat.Failovers[i].Reason = redact(copySeat.Failovers[i].Reason)
		copySeat.Failovers[i].FromProv = providers.MaskOperand(copySeat.Failovers[i].FromProv)
		copySeat.Failovers[i].FromMod = providers.MaskOperand(copySeat.Failovers[i].FromMod)
		copySeat.Failovers[i].ToProv = providers.MaskOperand(copySeat.Failovers[i].ToProv)
		copySeat.Failovers[i].ToMod = providers.MaskOperand(copySeat.Failovers[i].ToMod)
	}
	return copySeat
}

// formatPartitionReport renders the human-readable partition report written at
// the reconciliation.md path. It names the merged artifact, the per-unit claim
// counts, the verification verdict and - on a failed run - the conflict
// section naming every overlapped file and the units claiming it (B6: the
// operator must see WHO collided, not just that the run failed). Plan-authored
// ids/titles and file paths ride through the artifact redactor (D3), so a
// key-shaped value renders masked here exactly as in merged.md.
func formatPartitionReport(task, ts string, units []Unit, results []*SeatResult, claims [][]string, m *PartitionManifest, redact func(string) string) string {
	var sb strings.Builder
	sb.WriteString("# Partition Run Report\n\n")
	sb.WriteString(fmt.Sprintf("**Task:** `%s` | **Date:** `%s` | **Units:** `%d` | **Status:** `%s`\n\n",
		providers.MaskSentence(task, providers.FlagSeedOperands(task)...), ts, len(units), m.Status))
	sb.WriteString(untrustedFence("every unit deliverable quoted below"))

	sb.WriteString("## Merged Artifact\n\n")
	sb.WriteString(fmt.Sprintf("- `%s` - the single deliverable: every unit's work product concatenated in plan order.\n", m.Artifact))
	sb.WriteString("- `summary.json` - the machine summary (carries the merged `partition` manifest block).\n\n")

	sb.WriteString("## Units\n\n")
	sb.WriteString("| Unit | Title | Claimed Files | Seat Status |\n")
	sb.WriteString("|---|---|---|---|\n")
	for i, u := range units {
		status := "missing"
		if i < len(results) && results[i] != nil {
			status = results[i].Status
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %s | %d | `%s` |\n", redact(u.ID), redact(u.Title), len(claims[i]), status))
	}
	sb.WriteString("\n")

	sb.WriteString("## Verification\n\n")
	sb.WriteString(fmt.Sprintf("- Every unit delivered a non-empty work product: `%v`\n", m.Verification.UnitsDelivered))
	sb.WriteString(fmt.Sprintf("- Every unit appears in the merged artifact exactly once: `%v`\n", m.Verification.EachUnitInArtifactOnce))
	sb.WriteString(fmt.Sprintf("- No silently double-assigned file (every overlap reported): `%v`\n", m.Verification.NoSilentDoubleAssignment))
	sb.WriteString(fmt.Sprintf("- **PASSED:** `%v`\n\n", m.Verification.Passed))

	if len(m.Conflicts) > 0 {
		sb.WriteString("## Conflicts (RUN FAILED)\n\n")
		sb.WriteString("Partition mode requires DISJOINT unit scopes. The following files are claimed by more than one unit; ")
		sb.WriteString("the run is FAILED rather than silently merged or arbitrated:\n\n")
		for _, c := range m.Conflicts {
			unitNames := make([]string, len(c.Units))
			for i, id := range c.Units {
				unitNames[i] = redact(id)
			}
			sb.WriteString(fmt.Sprintf("- `%s` claimed by units: %s\n", redact(c.Path), strings.Join(unitNames, ", ")))
		}
		sb.WriteString("\nResolve the plan (narrow the scopes so every file has exactly one owner, or merge the colliding units) and re-run.\n\n")
	}

	if !m.Verification.Passed {
		sb.WriteString("## Verification Failures (RUN FAILED)\n\n")
		if !m.Verification.UnitsDelivered {
			sb.WriteString("- At least one unit produced NO non-empty deliverable (its seat failed, panicked, or answered empty).\n")
		}
		if !m.Verification.EachUnitInArtifactOnce {
			sb.WriteString("- At least one unit is missing from, or duplicated in, the merged artifact.\n")
		}
		if !m.Verification.NoSilentDoubleAssignment {
			sb.WriteString("- The conflict list disagrees with the claimed-files map (an overlap escaped reporting).\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// partitionFailureError is the B6 run-failure error: it names every overlapped
// file WITH the units claiming it, plus every failed verification check and
// the units that failed it, so the console/CLI error line is actionable on
// its own. The assembled message rides through the artifact redactor (D3), so
// a key-shaped unit id or file path renders masked instead of echoing raw.
func partitionFailureError(units []Unit, results []*SeatResult, conflicts []PartitionConflict, v PartitionVerification, redact func(string) string) error {
	var parts []string
	for _, c := range conflicts {
		parts = append(parts, fmt.Sprintf("file %q claimed by units [%s]", c.Path, strings.Join(c.Units, ", ")))
	}
	if !v.Passed {
		var failed []string
		if !v.UnitsDelivered {
			var missing []string
			for i := range units {
				if i >= len(results) || results[i] == nil || results[i].Status != "success" || strings.TrimSpace(results[i].Content) == "" {
					missing = append(missing, units[i].ID)
				}
			}
			failed = append(failed, "unit(s) produced no non-empty deliverable: ["+strings.Join(missing, ", ")+"]")
		}
		if !v.EachUnitInArtifactOnce {
			failed = append(failed, "a unit is missing from or duplicated in the merged artifact")
		}
		if !v.NoSilentDoubleAssignment {
			failed = append(failed, "an overlapped file was not reported")
		}
		parts = append(parts, "verification failed: "+strings.Join(failed, "; "))
	}
	return fmt.Errorf("partition merge conflict: disjointness assumption broken (%s); the merged artifact and the conflict report were still written for inspection",
		redact(strings.Join(parts, "; ")))
}
