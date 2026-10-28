package fanout_test

import (
	"bytes"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

func TestSwarmPresetNameSlotsMaskPlantedCredential(t *testing.T) {
	for _, tc := range []struct {
		name   string
		preset string
		want   string
	}{
		{name: "built-in remains byte-identical", preset: fanout.BuiltinPresetName, want: fanout.BuiltinPresetName},
		{name: "planted credential is masked", preset: testfixtures.SKLive, want: "sk_li****"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notice := swarm.SeatLineupNotice(fanout.SeatLineup{
				PresetName: tc.preset,
				Rung:       fanout.RungDefaultPreset,
				Raw:        "native:gemini-a",
			})
			if !strings.Contains(notice, `preset "`+tc.want+`"`) {
				t.Errorf("SeatLineupNotice = %q; want preset %q", notice, tc.want)
			}

			var out bytes.Buffer
			swarm.RenderSummary(&out, fanout.SummaryReport{
				NRequested: 1,
				SeatLineup: &fanout.SeatLineupReport{
					Preset: tc.preset,
					Rung:   fanout.RungDefaultPreset,
					Seats:  1,
				},
			}, false)
			if !strings.Contains(out.String(), `lineup from preset "`+tc.want+`"`) {
				t.Errorf("RenderSummary = %q; want preset %q", out.String(), tc.want)
			}
			if strings.Contains(notice, testfixtures.SKLive) || strings.Contains(out.String(), testfixtures.SKLive) {
				t.Errorf("swarm name slot leaks the planted credential: notice=%q summary=%q", notice, out.String())
			}
		})
	}
}
