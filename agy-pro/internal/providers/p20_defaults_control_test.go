package providers

import (
	"strings"
	"testing"
)

func TestP20DefaultsRejectLineAndFormatRunes(t *testing.T) {
	for _, field := range []string{"tools", "excludeFamilies"} {
		for _, class := range []rune{'\u2028', '\u2029', '\u200d'} {
			t.Run(field+"/"+string(class), func(t *testing.T) {
				reg := validControlTestRegistry()
				value := "read" + string(class) + "forged"
				if field == "tools" {
					reg.Defaults.Tools = value
				} else {
					reg.Defaults.ExcludeFamilies = []string{value}
				}
				err := reg.Validate()
				if err == nil {
					t.Fatalf("Validate accepted %s containing %U", field, class)
				}
				if strings.Contains(err.Error(), value) || strings.ContainsRune(err.Error(), class) {
					t.Fatalf("Validate error echoes the operand: %q", err)
				}
			})
		}
	}
}

func TestP20DefaultsBenignValuesStillValidate(t *testing.T) {
	reg := validControlTestRegistry()
	reg.Defaults.Tools = "read,search"
	reg.Defaults.ExcludeFamilies = []string{"embed", "한국어"}
	if err := reg.Validate(); err != nil {
		t.Fatalf("benign defaults rejected: %v", err)
	}
}
