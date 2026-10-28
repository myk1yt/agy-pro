package fanout

import (
	"strings"
	"testing"
)

func TestP18ProvenanceAdvisoryFiveDirections(t *testing.T) {
	for _, tc := range []struct {
		name       string
		marker     string
		action     string
		wantMarker string
		wantNotice string
	}{
		{
			name:       "deliberate no-edit seed save clears the missing-marker advisory",
			action:     "save-seed",
			wantMarker: SeatsHash("native:seed"),
		},
		{
			name:       "unrelated save preserves the missing marker and advisory",
			action:     "save-other",
			wantMarker: "",
			wantNotice: "NO provenance marker",
		},
		{
			name:       "edited seed save refreshes the marker and stays silent",
			action:     "edit-seed",
			wantMarker: SeatsHash("native:edited"),
		},
		{
			name:       "unrelated save preserves the stale marker and advisory",
			marker:     "stale-marker",
			action:     "save-other",
			wantMarker: "stale-marker",
			wantNotice: "stored hash mismatch",
		},
		{
			name:       "hand-pasted matching marker is silent by design",
			marker:     SeatsHash("native:seed"),
			action:     "none",
			wantMarker: SeatsHash("native:seed"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, stderr := ladderEnv(t)
			raw := `{"presets":[{"name":"(built-in defaults)","seats":"native:seed"`
			if tc.marker != "" {
				raw += `,"seats_sha256":"` + tc.marker + `"`
			}
			raw += `},{"name":"ordinary","n":2}]}`
			path := writeRawPresetsFile(t, home, raw)
			st := &PresetFileStore{Path: path}
			loaded, err := st.LoadPresets()
			if err != nil {
				t.Fatal(err)
			}

			switch tc.action {
			case "save-seed":
				loaded.Presets[0].SeatsSHA256 = SeatsHash(loaded.Presets[0].Seats)
			case "save-other":
				loaded.Presets[1].N++
			case "edit-seed":
				loaded.Presets[0].Seats = "native:edited"
				loaded.Presets[0].SeatsSHA256 = SeatsHash(loaded.Presets[0].Seats)
			}
			if tc.action != "none" {
				if err := st.SavePresets(loaded); err != nil {
					t.Fatal(err)
				}
			}
			if got := loaded.Presets[0].SeatsSHA256; got != tc.wantMarker {
				t.Errorf("seed marker = %q, want %q", got, tc.wantMarker)
			}

			if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
				t.Fatalf("the advisory must never block the run: %v", err)
			}
			out := stderr.String()
			t.Logf("preset-less stderr=%q", out)
			if tc.wantNotice == "" {
				if strings.Contains(out, "NO provenance marker") || strings.Contains(out, "stored hash mismatch") {
					t.Errorf("unexpected provenance advisory: %q", out)
				}
			} else if !strings.Contains(out, tc.wantNotice) || !strings.Contains(out, "advisory") {
				t.Errorf("stderr = %q, want advisory containing %q", out, tc.wantNotice)
			}
		})
	}
}
