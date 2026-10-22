package dashboard

import (
	"encoding/json"
	"os"
)

// DashboardPayloadVersion is the payload schema this client understands. It
// mirrors DASHBOARD_PAYLOAD_VERSION on the Node side (src/html-report.js), the
// same constant the REQ-240 staleness gate compares against.
const DashboardPayloadVersion = 3

// Status is the outcome of reading one artifact.
//
// A caller must render numbers ONLY for StatusOK. Both other states are silent
// on purpose: StatusMissing carries no numbers at all, and StatusStale means the
// file was written by a producer older than DashboardPayloadVersion, so its
// figures belong to a schema this client does not know how to read.
type Status int

const (
	// StatusMissing means the artifact is absent, unreadable, not JSON, not an
	// object, or self-contradictory (a server record with no usable port).
	StatusMissing Status = iota

	// StatusStale means the artifact parsed but predates the current payload
	// version, so its numbers must not be shown (REQ-240).
	StatusStale

	// StatusOK means the artifact parsed and matches the current contract.
	StatusOK
)

// String renders the status for diagnostics.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusStale:
		return "stale"
	default:
		return "missing"
	}
}

// ValidPort reports whether p can address a TCP port. Zero, negative and
// out-of-range values are rejected the way the Node reader rejects them.
func ValidPort(p int) bool { return p >= 1 && p <= 65535 }

// isJSONObject reports whether raw opens with a JSON object.
//
// The Node readers reject non-object JSON explicitly (`typeof data !==
// 'object'`), because encoding/json would otherwise accept `null` and leave an
// all-zero record behind, which reads as a valid-but-empty artifact instead of
// an absent one.
func isJSONObject(raw []byte) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// ServerRecord is the live-server shape of dashboard-server.json, written by
// `--serve` once the port is bound (src/dashboard-link.js writePortFile).
type ServerRecord struct {
	Port           int    `json:"port"`
	PID            int    `json:"pid"`
	StartedAt      string `json:"startedAt"`
	PayloadVersion int    `json:"payloadVersion"`
}

// SpawnIntent is the stampede-guard shape of the same file, written while a
// detached server is starting (src/dashboard-link.js writeSpawnIntent). At is
// epoch milliseconds, not an ISO timestamp.
type SpawnIntent struct {
	Intent        string `json:"intent"`
	RequestedPort int    `json:"requestedPort"`
	At            int64  `json:"at"`
}

// LoadServerRecord reads and validates the live-server record. A missing file,
// unreadable bytes, malformed JSON, or a port outside 1..65535 yields
// StatusMissing with a zero record; a record whose payloadVersion is behind
// DashboardPayloadVersion (or absent) yields StatusStale with the record
// attached, because the port is still real even when the payload is not.
func LoadServerRecord() (ServerRecord, Status) {
	path, err := ServerJSONPath()
	if err != nil {
		return ServerRecord{}, StatusMissing
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ServerRecord{}, StatusMissing
	}
	var rec ServerRecord
	if !isJSONObject(raw) || json.Unmarshal(raw, &rec) != nil {
		return ServerRecord{}, StatusMissing
	}
	if !ValidPort(rec.Port) {
		return ServerRecord{}, StatusMissing
	}
	if rec.PayloadVersion < DashboardPayloadVersion {
		return rec, StatusStale
	}
	return rec, StatusOK
}

// LoadSpawnIntent reads the spawn-intent record. Any file that does not carry
// intent "spawn" plus a usable requestedPort is StatusMissing.
func LoadSpawnIntent() (SpawnIntent, Status) {
	path, err := ServerJSONPath()
	if err != nil {
		return SpawnIntent{}, StatusMissing
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return SpawnIntent{}, StatusMissing
	}
	var intent SpawnIntent
	if !isJSONObject(raw) || json.Unmarshal(raw, &intent) != nil {
		return SpawnIntent{}, StatusMissing
	}
	if intent.Intent != "spawn" || !ValidPort(intent.RequestedPort) {
		return SpawnIntent{}, StatusMissing
	}
	return intent, StatusOK
}

// Summary is the consumer subset of one period summary in the v3 payload.
type Summary struct {
	TotalSessions   int     `json:"totalSessions"`
	TotalTurns      int     `json:"totalTurns"`
	InputTokens     int64   `json:"inputTokens"`
	CachedTokens    int64   `json:"cachedTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	TotalTokens     int64   `json:"totalTokens"`
	CacheHitRate    float64 `json:"cacheHitRate"`
	CostUsd         float64 `json:"costUsd"`
	CacheSavingsUsd float64 `json:"cacheSavingsUsd"`
}

// Summaries groups the period summaries the compact view renders.
type Summaries struct {
	Today   Summary `json:"today"`
	Last7d  Summary `json:"last7d"`
	Last30d Summary `json:"last30d"`
}

// ModelRow is the consumer subset of one per-model aggregate in the v3 payload.
// Node emits these sorted by costUsd descending.
type ModelRow struct {
	Model        string  `json:"model"`
	TotalTokens  int64   `json:"totalTokens"`
	CostUsd      float64 `json:"costUsd"`
	CacheHitRate float64 `json:"cacheHitRate"`
	Sessions     int     `json:"sessions"`
}

// CacheStats is the aggregation-cost block of the payload.
type CacheStats struct {
	TotalSessions int     `json:"totalSessions"`
	ParsedCount   int     `json:"parsedCount"`
	CachedCount   int     `json:"cachedCount"`
	ElapsedMs     float64 `json:"elapsedMs"`
}

// GeminiQuota is the consumer subset of the Gemini quota block. It is a pointer
// in the payload because the Node side emits null when no cache exists.
type GeminiQuota struct {
	RemainPercent     float64 `json:"remainPercent"`
	RemainingFraction float64 `json:"remainingFraction"`
	ModelLabel        string  `json:"modelLabel"`
	ResetFormatted    string  `json:"resetFormatted"`
	ResetInSeconds    int64   `json:"resetInSeconds"`
	IsFresh           bool    `json:"isFresh"`
	IsStale           bool    `json:"isStale"`
	AgeMs             float64 `json:"ageMs"`
}

// Payload is the consumer subset of dashboard-data.json (v3). Fields the
// compact view does not need (i18n, daily, dailyModels, rollingUsage, ...) are
// deliberately absent: encoding/json ignores what it is not asked to decode, so
// a payload that grows new keys keeps loading.
type Payload struct {
	Version     int          `json:"version"`
	GeneratedAt string       `json:"generatedAt"`
	Summaries   Summaries    `json:"summaries"`
	Models      []ModelRow   `json:"models"`
	CacheStats  *CacheStats  `json:"cacheStats"`
	GeminiQuota *GeminiQuota `json:"geminiQuota"`
}

// LoadPayload reads and version-gates the payload snapshot.
//
// The gate is the whole point of the function: anything but StatusOK returns a
// ZERO Payload, so a stale or missing snapshot cannot leak figures into a
// render even if a caller forgets to check the status.
func LoadPayload() (Payload, Status) {
	path, err := DataJSONPath()
	if err != nil {
		return Payload{}, StatusMissing
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Payload{}, StatusMissing
	}
	var payload Payload
	if !isJSONObject(raw) || json.Unmarshal(raw, &payload) != nil {
		return Payload{}, StatusMissing
	}
	if payload.Version < DashboardPayloadVersion {
		return Payload{}, StatusStale
	}
	return payload, StatusOK
}
