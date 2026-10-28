package providers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPrintPathsNeverEmitRawKeys is the U8 grep-assert (contract §2.11): no
// format/print line in the wizard, settings, or doctor display paths may
// concatenate a raw key VALUE identifier. Every such line must route the value
// through MaskSecret or the redactor. This is a static companion to the runtime
// leak assertions in secretinput_test.go and settings tests.
func TestPrintPathsNeverEmitRawKeys(t *testing.T) {
	printCall := regexp.MustCompile(`fmt\.(Fprint|Fprintf|Fprintln|Print|Printf|Println)`)
	// Identifiers that hold raw key VALUES (as opposed to env-var NAMES).
	// args[ covers operand echoes at the cmd/agy wrapper dispatch layer.
	rawValueID := regexp.MustCompile(`\b(keyValue|keyInput|newKey|existingVal|envVal|planted|apiKey)\b|args\[`)
	// allowed lists the maskers a print line may route a raw identifier
	// through: the providers maskers plus the cmd/agy sentence maskers.
	allowed := regexp.MustCompile(`MaskSecret|MaskOperand|RedactEchoLine|Redactor|redact|REDACTED|redactProbeDetail|maskOperandSentence|invalidModeArgMessage`)

	roots := []string{
		".",
		filepath.Join("..", "settings"),
		filepath.Join("..", "..", "cmd", "agy"),
	}
	scanned := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", root, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			scanned++
			for i, line := range strings.Split(string(data), "\n") {
				if !printCall.MatchString(line) || !rawValueID.MatchString(line) {
					continue
				}
				// The identifier may appear in a non-print role on the same
				// line (assignment); require it to sit inside the argument
				// list of a print call — approximated by a '(' after the name.
				if allowed.MatchString(line) {
					continue
				}
				if strings.Contains(line, "KeyValue()") || strings.Contains(line, "KeyValueWithOrigin") {
					continue
				}
				// `agy pro settings mode switch` echoes an operand that
				// mode.Switch has already constrained to the {full, clean}
				// enum (mode.go:276-286); the invalid path masks via
				// MaskSecret (mode.go:284). Exempt exactly that SUCCESS line
				// — if the mode operand ever becomes free-form, wrap it in
				// providers.MaskOperand and drop this exemption.
				if strings.Contains(line, "mode switched to") {
					continue
				}
				t.Errorf("%s:%d: print line touches a raw key identifier without MaskSecret/Redactor:\n  %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if scanned == 0 {
		t.Fatal("grep-assert scanned no source files")
	}
}
