package wireadp

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// readAuditLines polls the audit file until it holds at least want lines.
// The audit write happens after the handler returns, so the HTTP response can
// reach the client before the line lands on disk.
func readAuditLines(t *testing.T, path string, want int) []auditEntry {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			var lines []auditEntry
			ok := true
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if line == "" {
					continue
				}
				var entry auditEntry
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatalf("audit line is not JSON: %v (%s)", err, line)
				}
				lines = append(lines, entry)
			}
			if ok && len(lines) >= want {
				return lines
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit file %s never reached %d lines (last error: %v)", path, want, lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAudit_OneLinePerRequestWithHeaderNamesOnly(t *testing.T) {
	const secret = "SEKRET-KEY-VALUE-1234567890"
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.Redact = providers.NewRedactor(secret)
	srv := startServer(t, cfg)

	url := srv.URL() + "/v1beta/models/probe-gemini:streamGenerateContent"
	resp, _ := post(t, url, map[string]string{
		headerAPIKey:       secret,
		headerInstallNonce: secret,
		"User-Agent":       "google-genai-sdk/1.74.0",
	}, chatBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	// A second request proves the file appends rather than truncates.
	resp, _ = get(t, srv.URL()+"/v1beta/models", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listing status %d", resp.StatusCode)
	}

	lines := readAuditLines(t, cfg.AuditPath, 2)
	if len(lines) != 2 {
		t.Fatalf("audit lines = %d, want exactly 2", len(lines))
	}
	first := lines[0]
	if first.Method != http.MethodPost || first.Route != routeGenerate {
		t.Fatalf("first line = %+v", first)
	}
	if first.Model != "probe-gemini" || first.Verb != "streamGenerateContent" {
		t.Fatalf("first line target = %+v", first)
	}
	if first.Status != http.StatusOK || first.DurationMS < 0 {
		t.Fatalf("first line status/duration = %+v", first)
	}
	if first.TS.IsZero() {
		t.Fatal("the audit line must carry a timestamp")
	}
	// Header NAMES are recorded...
	joined := strings.Join(first.HeaderNames, ",")
	for _, want := range []string{headerAPIKey, headerInstallNonce, "User-Agent"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("header names %v missing %q", first.HeaderNames, want)
		}
	}
	// ...and the VALUES never are, in any line, even before redaction.
	raw, err := os.ReadFile(cfg.AuditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("audit file leaked the planted key: %s", raw)
	}
	if strings.Contains(string(raw), "google-genai-sdk") {
		t.Fatalf("audit file leaked a header value: %s", raw)
	}

	second := lines[1]
	if second.Route != routeListModels || second.Model != "" {
		t.Fatalf("second line = %+v", second)
	}
}

func TestAudit_QueryKeysNeverReachTheFile(t *testing.T) {
	const secret = "SEKRET-QUERY-KEY-987654321"
	cfg := testConfig(t)
	cfg.Redact = providers.NewRedactor(secret)
	cfg.AuthKey = "install-nonce"
	srv := startServer(t, cfg)

	// The key arrives in the query string; only the path may be captured.
	url := srv.URL() + "/v1beta/models/probe-gemini:streamGenerateContent?key=" + secret
	resp, _ := post(t, url, nil, chatBody())
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 (the query key is not the install nonce)", resp.StatusCode)
	}
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if strings.Contains(lines[0].Path, "?") || strings.Contains(lines[0].Path, secret) {
		t.Fatalf("audit path leaked the query: %q", lines[0].Path)
	}
	raw, _ := os.ReadFile(cfg.AuditPath)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("audit file leaked the query key: %s", raw)
	}
}

func TestAudit_WriteFailureNeverAffectsTheResponse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	// A regular file where a directory is expected: every open fails.
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.AuditPath = filepath.Join(blocker, "audit.jsonl")
	srv := startServer(t, cfg)

	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s: an audit failure must not change the response", resp.StatusCode, body)
	}
	resp, body = get(t, srv.URL()+"/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status %d body %s", resp.StatusCode, body)
	}
}

func TestAudit_DefaultPathUnderAgyProHome(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuditPath = ""
	srv := startServer(t, cfg)
	resp, _ := get(t, srv.URL()+"/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal("healthz failed")
	}
	home, err := paths.AgyProHome()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	logs, err := os.ReadDir(filepath.Join(home, "logs"))
	if err != nil {
		t.Fatalf("read logs dir: %v", err)
	}
	found := false
	for _, e := range logs {
		if strings.HasPrefix(e.Name(), "wireadp-") && strings.HasSuffix(e.Name(), ".jsonl") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no default wireadp-*.jsonl audit file under %s", home)
	}
}
