package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

// redirect aims the package path seam at a fresh temp directory for one test.
func redirect(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := dirOverride
	dirOverride = dir
	t.Cleanup(func() { dirOverride = prev })
	return dir
}

// install copies a testdata fixture into dir under the artifact name the
// loaders resolve, so the test exercises the real path resolution.
func install(t *testing.T, dir, fixture, name string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	writeArtifact(t, dir, name, string(raw))
}

// writeArtifact writes literal artifact bytes into dir.
func writeArtifact(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDirAndArtifactPaths(t *testing.T) {
	home := t.TempDir()
	prevHome, prevOverride := homeDir, dirOverride
	homeDir = func() (string, error) { return home, nil }
	dirOverride = ""
	t.Cleanup(func() { homeDir, dirOverride = prevHome, prevOverride })

	wantDir := filepath.Join(home, ".gemini", "antigravity-dashboard")
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir != wantDir {
		t.Fatalf("Dir = %q, want %q", dir, wantDir)
	}

	for _, tc := range []struct {
		name string
		fn   func() (string, error)
		file string
	}{
		{"data", DataJSONPath, "dashboard-data.json"},
		{"server", ServerJSONPath, "dashboard-server.json"},
		{"html", HTMLPath, "dashboard.html"},
	} {
		got, err := tc.fn()
		if err != nil {
			t.Fatalf("%s path: %v", tc.name, err)
		}
		if want := filepath.Join(wantDir, tc.file); got != want {
			t.Fatalf("%s path = %q, want %q", tc.name, got, want)
		}
	}
}

func TestDirOverrideWinsOverHome(t *testing.T) {
	home := t.TempDir()
	override := t.TempDir()
	prevHome, prevOverride := homeDir, dirOverride
	homeDir = func() (string, error) { return home, nil }
	dirOverride = override
	t.Cleanup(func() { homeDir, dirOverride = prevHome, prevOverride })

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir != override {
		t.Fatalf("Dir = %q, want override %q", dir, override)
	}
}

func TestLoadServerRecordStatuses(t *testing.T) {
	cases := []struct {
		name       string
		content    string // empty means: no file at all
		wantStatus Status
		wantPort   int
	}{
		{"live fixture", readFixture(t, "dashboard-server.json"), StatusOK, 8787},
		{"absent file", "", StatusMissing, 0},
		{"corrupt json", `{"port": 8787,`, StatusMissing, 0},
		{"empty file", "  ", StatusMissing, 0},
		{"json array", `[8787]`, StatusMissing, 0},
		{"json null", `null`, StatusMissing, 0},
		{"spawn intent only", readFixture(t, "spawn-intent.json"), StatusMissing, 0},
		{"port zero", `{"port":0,"pid":1,"payloadVersion":3}`, StatusMissing, 0},
		{"port negative", `{"port":-1,"pid":1,"payloadVersion":3}`, StatusMissing, 0},
		{"port above range", `{"port":65536,"pid":1,"payloadVersion":3}`, StatusMissing, 0},
		{"port one", `{"port":1,"pid":1,"payloadVersion":3}`, StatusOK, 1},
		{"port max", `{"port":65535,"pid":1,"payloadVersion":3}`, StatusOK, 65535},
		{"port as string", `{"port":"8787","payloadVersion":3}`, StatusMissing, 0},
		{"payloadVersion absent is stale", `{"port":8787,"pid":1}`, StatusStale, 8787},
		{"payloadVersion 2 is stale", `{"port":8787,"pid":1,"payloadVersion":2}`, StatusStale, 8787},
		{"payloadVersion 4 is forward", `{"port":8787,"pid":1,"payloadVersion":4}`, StatusOK, 8787},
		{"unknown extra keys tolerated", `{"port":8787,"pid":1,"payloadVersion":3,"futureKey":{"a":[1,2]}}`, StatusOK, 8787},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := redirect(t)
			if tc.content != "" {
				writeArtifact(t, dir, "dashboard-server.json", tc.content)
			}
			rec, status := LoadServerRecord()
			if status != tc.wantStatus {
				t.Fatalf("status = %v, want %v", status, tc.wantStatus)
			}
			if rec.Port != tc.wantPort {
				t.Fatalf("port = %d, want %d", rec.Port, tc.wantPort)
			}
			if status == StatusMissing && (rec.PID != 0 || rec.StartedAt != "" || rec.PayloadVersion != 0) {
				t.Fatalf("missing status leaked a record: %+v", rec)
			}
		})
	}
}

func TestLoadServerRecordFields(t *testing.T) {
	dir := redirect(t)
	install(t, dir, "dashboard-server.json", "dashboard-server.json")

	rec, status := LoadServerRecord()
	if status != StatusOK {
		t.Fatalf("status = %v, want ok", status)
	}
	if rec.PID != 41204 {
		t.Fatalf("pid = %d, want 41204", rec.PID)
	}
	if rec.StartedAt != "2026-10-21T09:14:52.118Z" {
		t.Fatalf("startedAt = %q", rec.StartedAt)
	}
	if rec.PayloadVersion != DashboardPayloadVersion {
		t.Fatalf("payloadVersion = %d, want %d", rec.PayloadVersion, DashboardPayloadVersion)
	}
}

func TestLoadSpawnIntent(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantStatus Status
		wantPort   int
	}{
		{"spawn intent fixture", readFixture(t, "spawn-intent.json"), StatusOK, 8787},
		{"live server record", readFixture(t, "dashboard-server.json"), StatusMissing, 0},
		{"absent file", "", StatusMissing, 0},
		{"corrupt json", `{"intent":"spawn",`, StatusMissing, 0},
		{"wrong intent value", `{"intent":"start","requestedPort":8787,"at":1}`, StatusMissing, 0},
		{"requestedPort zero", `{"intent":"spawn","requestedPort":0,"at":1}`, StatusMissing, 0},
		{"requestedPort above range", `{"intent":"spawn","requestedPort":70000,"at":1}`, StatusMissing, 0},
		{"unknown extra keys tolerated", `{"intent":"spawn","requestedPort":8788,"at":1,"note":"x"}`, StatusOK, 8788},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := redirect(t)
			if tc.content != "" {
				writeArtifact(t, dir, "dashboard-server.json", tc.content)
			}
			intent, status := LoadSpawnIntent()
			if status != tc.wantStatus {
				t.Fatalf("status = %v, want %v", status, tc.wantStatus)
			}
			if intent.RequestedPort != tc.wantPort {
				t.Fatalf("requestedPort = %d, want %d", intent.RequestedPort, tc.wantPort)
			}
		})
	}
}

func TestLoadSpawnIntentFields(t *testing.T) {
	dir := redirect(t)
	install(t, dir, "spawn-intent.json", "dashboard-server.json")

	intent, status := LoadSpawnIntent()
	if status != StatusOK {
		t.Fatalf("status = %v, want ok", status)
	}
	if intent.Intent != "spawn" {
		t.Fatalf("intent = %q, want spawn", intent.Intent)
	}
	if intent.At != 1761038092118 {
		t.Fatalf("at = %d, want 1761038092118", intent.At)
	}
}

func TestLoadPayloadOK(t *testing.T) {
	dir := redirect(t)
	install(t, dir, "dashboard-data.json", "dashboard-data.json")

	payload, status := LoadPayload()
	if status != StatusOK {
		t.Fatalf("status = %v, want ok", status)
	}
	if payload.Version != 3 {
		t.Fatalf("version = %d, want 3", payload.Version)
	}
	if payload.GeneratedAt != "2026-10-21T09:15:00.000Z" {
		t.Fatalf("generatedAt = %q", payload.GeneratedAt)
	}
	if payload.Summaries.Today.TotalTokens != 243000 {
		t.Fatalf("today.totalTokens = %d, want 243000", payload.Summaries.Today.TotalTokens)
	}
	if payload.Summaries.Today.InputTokens != 130000 ||
		payload.Summaries.Today.CachedTokens != 52000 ||
		payload.Summaries.Today.OutputTokens != 61000 {
		t.Fatalf("today token split = %+v", payload.Summaries.Today)
	}
	if payload.Summaries.Last7d.CostUsd != 11.902 {
		t.Fatalf("last7d.costUsd = %v, want 11.902", payload.Summaries.Last7d.CostUsd)
	}
	if payload.Summaries.Last30d.TotalSessions != 41 {
		t.Fatalf("last30d.totalSessions = %d, want 41", payload.Summaries.Last30d.TotalSessions)
	}
	if len(payload.Models) != 2 {
		t.Fatalf("models = %d rows, want 2", len(payload.Models))
	}
	if payload.Models[0].Model != "claude-sonnet-5-5-high" ||
		payload.Models[0].CostUsd != 12.480123 ||
		payload.Models[0].Sessions != 14 {
		t.Fatalf("models[0] = %+v", payload.Models[0])
	}
	if payload.Models[1].Model != "gemini-3.8-flash-high" ||
		payload.Models[1].TotalTokens != 480000 ||
		payload.Models[1].CacheHitRate != 28.5714 {
		t.Fatalf("models[1] = %+v", payload.Models[1])
	}
	if payload.CacheStats == nil {
		t.Fatal("cacheStats absent, want present")
	}
	if payload.CacheStats.TotalSessions != 41 || payload.CacheStats.CachedCount != 38 {
		t.Fatalf("cacheStats = %+v", *payload.CacheStats)
	}
	if payload.GeminiQuota == nil {
		t.Fatal("geminiQuota absent, want present")
	}
	if !payload.GeminiQuota.IsFresh || payload.GeminiQuota.RemainPercent != 63 {
		t.Fatalf("geminiQuota = %+v", *payload.GeminiQuota)
	}
}

func TestLoadPayloadStaleGate(t *testing.T) {
	dir := redirect(t)
	install(t, dir, "stale-version.json", "dashboard-data.json")

	payload, status := LoadPayload()
	if status != StatusStale {
		t.Fatalf("status = %v, want stale", status)
	}
	if !payloadIsZero(payload) {
		t.Fatalf("stale payload must be zero, got %+v", payload)
	}
	if payload.Summaries.Today.TotalTokens != 0 || len(payload.Models) != 0 {
		t.Fatalf("stale gate leaked numbers: %+v", payload)
	}
}

func TestLoadPayloadVersionGates(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantStatus Status
	}{
		{"version 2", `{"version":2,"summaries":{"today":{"totalTokens":9}}}`, StatusStale},
		{"version 1", `{"version":1}`, StatusStale},
		{"version absent", `{"summaries":{"today":{"totalTokens":9}}}`, StatusStale},
		{"version zero", `{"version":0}`, StatusStale},
		{"version negative", `{"version":-3}`, StatusStale},
		{"version 3", `{"version":3}`, StatusOK},
		{"version 4 forward", `{"version":4,"futureKey":"x"}`, StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := redirect(t)
			writeArtifact(t, dir, "dashboard-data.json", tc.content)
			payload, status := LoadPayload()
			if status != tc.wantStatus {
				t.Fatalf("status = %v, want %v", status, tc.wantStatus)
			}
			if status != StatusOK && !payloadIsZero(payload) {
				t.Fatalf("non-ok status returned figures: %+v", payload)
			}
		})
	}
}

func TestLoadPayloadMissingStatuses(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"absent file", ""},
		{"corrupt json", readFixture(t, "corrupt.json")},
		{"empty file", ""},
		{"json array", `[{"version":3}]`},
		{"json null", `null`},
		{"version as string", `{"version":"3"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := redirect(t)
			if tc.name == "empty file" {
				writeArtifact(t, dir, "dashboard-data.json", "")
			} else if tc.content != "" {
				writeArtifact(t, dir, "dashboard-data.json", tc.content)
			}
			payload, status := LoadPayload()
			if status != StatusMissing {
				t.Fatalf("status = %v, want missing", status)
			}
			if !payloadIsZero(payload) {
				t.Fatalf("missing payload must be zero, got %+v", payload)
			}
		})
	}
}

func TestLoadPayloadForwardCompat(t *testing.T) {
	dir := redirect(t)
	raw := readFixture(t, "dashboard-data.json")
	// A future producer adds keys at every level this client decodes.
	augmented := `{
  "version": 3,
  "generatedAt": "2026-10-21T09:15:00.000Z",
  "futureTop": {"nested": [1, 2, 3], "flag": true},
  "summaries": {
    "today": {"totalTokens": 1000, "costUsd": 0.5, "futureSummaryKey": "x"},
    "last7d": {"totalTokens": 2000},
    "last30d": {"totalTokens": 3000},
    "thisQuarter": {"totalTokens": 4000}
  },
  "models": [
    {"model": "m1", "totalTokens": 1000, "costUsd": 0.5, "sessions": 2, "futureModelKey": 7}
  ],
  "cacheStats": {"totalSessions": 1, "futureCacheKey": null},
  "geminiQuota": null,
  "rollingUsage": {"future": "block"}
}`
	writeArtifact(t, dir, "dashboard-data.json", augmented)

	payload, status := LoadPayload()
	if status != StatusOK {
		t.Fatalf("status = %v, want ok for a forward payload", status)
	}
	if payload.Summaries.Today.TotalTokens != 1000 || payload.Summaries.Today.CostUsd != 0.5 {
		t.Fatalf("known fields lost: %+v", payload.Summaries.Today)
	}
	if payload.Summaries.Last30d.TotalTokens != 3000 {
		t.Fatalf("last30d = %+v", payload.Summaries.Last30d)
	}
	if len(payload.Models) != 1 || payload.Models[0].TotalTokens != 1000 {
		t.Fatalf("models = %+v", payload.Models)
	}
	if payload.CacheStats == nil || payload.CacheStats.TotalSessions != 1 {
		t.Fatalf("cacheStats = %+v", payload.CacheStats)
	}
	if payload.GeminiQuota != nil {
		t.Fatalf("geminiQuota null must decode as nil, got %+v", *payload.GeminiQuota)
	}

	// The trimmed fixture itself must still decode after the seam moved.
	writeArtifact(t, dir, "dashboard-data.json", raw)
	if _, status := LoadPayload(); status != StatusOK {
		t.Fatalf("trimmed fixture status = %v, want ok", status)
	}
}

func TestLoadPayloadOmitEmptyTolerance(t *testing.T) {
	dir := redirect(t)
	writeArtifact(t, dir, "dashboard-data.json", `{"version":3}`)

	payload, status := LoadPayload()
	if status != StatusOK {
		t.Fatalf("status = %v, want ok", status)
	}
	if payload.Summaries.Today.TotalTokens != 0 || payload.Models != nil ||
		payload.CacheStats != nil || payload.GeminiQuota != nil {
		t.Fatalf("minimal v3 payload decoded non-zero: %+v", payload)
	}
}

func TestStatusString(t *testing.T) {
	for _, tc := range []struct {
		status Status
		want   string
	}{
		{StatusMissing, "missing"},
		{StatusStale, "stale"},
		{StatusOK, "ok"},
	} {
		if got := tc.status.String(); got != tc.want {
			t.Fatalf("Status(%d).String() = %q, want %q", int(tc.status), got, tc.want)
		}
	}
}

func TestValidPort(t *testing.T) {
	for _, tc := range []struct {
		port int
		want bool
	}{
		{-1, false}, {0, false}, {1, true}, {8787, true}, {65535, true}, {65536, false},
	} {
		if got := ValidPort(tc.port); got != tc.want {
			t.Fatalf("ValidPort(%d) = %v, want %v", tc.port, got, tc.want)
		}
	}
}

// payloadIsZero reports whether p carries no decoded content. Payload holds a
// slice, so it is not comparable with ==.
func payloadIsZero(p Payload) bool {
	return p.Version == 0 && p.GeneratedAt == "" &&
		p.Summaries == (Summaries{}) && p.Models == nil &&
		p.CacheStats == nil && p.GeminiQuota == nil
}

// readFixture returns a testdata fixture as text.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}
