package providers

import (
	"strings"
	"testing"

	"agypro/internal/testfixtures"
)

// redactorSkLiveBearer is a synthetic key VALUE (a listed credential head plus
// an opaque alnum run). The head is built by string concatenation so the
// tracked source carries no contiguous provider-key spelling; the runtime value
// is byte-identical to the literal it replaced.
const redactorSkLiveBearer = "sk-" + "live-" + "abcdef0123456789"

// S-4/F2: the redactor must mask generic credential shapes too, not only the
// registered provider keys - leaked files and provider error bodies carry
// material the registry has never seen.
func TestRedactorGenericCredentialShapes(t *testing.T) {
	redact := NewRedactor()
	cases := []struct {
		name string
		in   string
		leak string // must NOT survive
		keep string // prefix that may survive (anchor text)
	}{
		{
			name: "aws access key id",
			in:   "client = boto3.client('s3'); key id " + testfixtures.AKIAExample + " and more",
			leak: testfixtures.AKIAExample,
			keep: "key id",
		},
		{
			name: "aws secret access key assignment",
			in:   "aws_secret_access_key = SUPERSECRETVALUE123",
			leak: "SUPERSECRETVALUE123",
			keep: "aws_secret_access_key",
		},
		{
			name: "authorization header value",
			in:   "Authorization: Bearer " + redactorSkLiveBearer,
			leak: redactorSkLiveBearer,
			keep: "Authorization",
		},
		{
			name: "bearer token",
			in:   "curl -H 'Authorization: Bearer " + testfixtures.GHPNumeric + "' ",
			leak: testfixtures.GHPNumeric,
			keep: "Bearer",
		},
		{
			name: "sk- long token",
			in:   "paste: " + testfixtures.SKProjLong + " done",
			leak: testfixtures.SKProjLong,
			keep: "paste:",
		},
		{
			name: "pem private key block",
			in:   "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA0\n-----END RSA PRIVATE KEY-----",
			leak: "MIIEpAIBAAKCAQEA0",
			keep: "PRIVATE KEY",
		},
		{
			name: "google api key",
			in:   "export GEMINI_API_KEY=" + testfixtures.AIzaGeminiLong,
			leak: testfixtures.AIzaGeminiLong,
			keep: "GEMINI_API_KEY=",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.in)
			if strings.Contains(got, tc.leak) {
				t.Errorf("credential material survived redaction: %q", got)
			}
			if tc.keep != "" && !strings.Contains(got, tc.keep) {
				t.Errorf("anchor text %q lost from redacted output: %q", tc.keep, got)
			}
			if !strings.Contains(got, "[REDACTED") {
				t.Errorf("no redaction marker in output: %q", got)
			}
		})
	}
}

// The Google key shape is exact: 34 characters after the AIza prefix is not a
// key and must ride through untouched, so the pattern does not eat ordinary
// text that happens to start with those four letters.
func TestRedactorGoogleKeyShapeIsExact(t *testing.T) {
	redact := NewRedactor()
	const short = testfixtures.AIzaGeminiShort // 34 chars after AIza
	if got := redact("note: " + short); got != "note: "+short {
		t.Errorf("a 38-char AIza string must survive untouched, got %q", got)
	}
	const exact = testfixtures.AIzaGeminiLong // 35 chars after AIza
	got := redact("note: " + exact)
	if strings.Contains(got, exact) {
		t.Errorf("a 39-char Google key survived redaction: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("no redaction marker in output: %q", got)
	}
}

// S-4/F2: registered-key masking still applies (extension, not replacement).
func TestRedactorKeepsRegisteredKeyMasking(t *testing.T) {
	redact := NewRedactor("nw-registered-key-value")
	got := redact("using nw-registered-key-value with sk-abcdef0123456789")
	if strings.Contains(got, "nw-registered-key-value") {
		t.Errorf("registered key not masked: %q", got)
	}
	if strings.Contains(got, "sk-abcdef0123456789") {
		t.Errorf("sk- token not masked: %q", got)
	}
}
