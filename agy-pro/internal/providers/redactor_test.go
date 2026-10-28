package providers

import (
	"strings"
	"testing"
)

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
			in:   "client = boto3.client('s3'); key id AKIAIOSFODNN7EXAMPLE and more",
			leak: "AKIAIOSFODNN7EXAMPLE",
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
			in:   `Authorization: Bearer sk-live-abcdef0123456789`,
			leak: "sk-live-abcdef0123456789",
			keep: "Authorization",
		},
		{
			name: "bearer token",
			in:   "curl -H 'Authorization: Bearer ghp_0123456789abcdef0123' ",
			leak: "ghp_0123456789abcdef0123",
			keep: "Bearer",
		},
		{
			name: "sk- long token",
			in:   "paste: sk-proj-0123456789abcdef0123456789 done",
			leak: "sk-proj-0123456789abcdef0123456789",
			keep: "paste:",
		},
		{
			name: "pem private key block",
			in:   "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA0\n-----END RSA PRIVATE KEY-----",
			leak: "MIIEpAIBAAKCAQEA0",
			keep: "PRIVATE KEY",
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
