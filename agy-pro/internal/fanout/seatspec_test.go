package fanout

import (
	"strings"
	"testing"
)

func TestParseSeatSpecs_MixedOrderPreserved(t *testing.T) {
	spec := "native:gemini-3.8-flash-high,native:gemini-3.8-pro,nw/glm-5.3-flash,bigmodel/glm-4.6"
	got, err := ParseSeatSpecs(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 seats, got %d: %+v", len(got), got)
	}
	wantKinds := []SeatKind{SeatKindNative, SeatKindNative, SeatKindProvider, SeatKindProvider}
	wantIDs := []string{"gemini-3.8-flash-high", "gemini-3.8-pro", "nw/glm-5.3-flash", "bigmodel/glm-4.6"}
	for i := range got {
		if got[i].Kind != wantKinds[i] {
			t.Errorf("seat %d kind = %q; want %q", i+1, got[i].Kind, wantKinds[i])
		}
		if got[i].ModelID != wantIDs[i] {
			t.Errorf("seat %d ModelID = %q; want %q", i+1, got[i].ModelID, wantIDs[i])
		}
		if got[i].Raw != strings.TrimSpace(strings.Split(spec, ",")[i]) {
			t.Errorf("seat %d Raw = %q; want original token", i+1, got[i].Raw)
		}
	}
}

func TestParseSeatSpecs_NativePrefixCaseInsensitive(t *testing.T) {
	for _, tok := range []string{"native:glm-x", "NATIVE:glm-x", "Native:glm-x", "NaTiVe:glm-x"} {
		got, err := ParseSeatSpecs(tok)
		if err != nil {
			t.Fatalf("ParseSeatSpecs(%q) error: %v", tok, err)
		}
		if got[0].Kind != SeatKindNative || got[0].ModelID != "glm-x" {
			t.Errorf("ParseSeatSpecs(%q) = %+v; want native/glm-x", tok, got[0])
		}
	}
}

func TestParseSeatSpecs_BareIsProvider(t *testing.T) {
	for _, tok := range []string{"glm-4.6", "nw/glm-5.3-flash", "glm-4.6 (bigmodel)"} {
		got, err := ParseSeatSpecs(tok)
		if err != nil {
			t.Fatalf("ParseSeatSpecs(%q) error: %v", tok, err)
		}
		if got[0].Kind != SeatKindProvider {
			t.Errorf("ParseSeatSpecs(%q) kind = %q; want provider", tok, got[0].Kind)
		}
		if got[0].ModelID != tok {
			t.Errorf("ParseSeatSpecs(%q) ModelID = %q; want raw spec %q", tok, got[0].ModelID, tok)
		}
	}
}

func TestParseSeatSpecs_WhitespaceTrimmed(t *testing.T) {
	got, err := ParseSeatSpecs("  native:glm-x ,  nw/glm-5.3-flash  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Raw != "native:glm-x" || got[1].Raw != "nw/glm-5.3-flash" {
		t.Errorf("tokens not trimmed: %+v", got)
	}
}

func TestParseSeatSpecs_DuplicatesAllowed(t *testing.T) {
	got, err := ParseSeatSpecs("native:glm-x,native:glm-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("duplicates must be allowed, got %d seats", len(got))
	}
}

func TestParseSeatSpecs_ErrorStrings(t *testing.T) {
	cases := []struct {
		spec    string
		wantErr string
	}{
		{"native:glm-x,,nw/glm", "seat 2: empty seat token in --seats"},
		{",", "seat 1: empty seat token in --seats"},
		{"native:", `seat 1: "native:": "native:" prefix requires a model id (e.g. native:gemini-3.8-flash-high)`},
		{"native:   ", `seat 1: "native:": "native:" prefix requires a model id (e.g. native:gemini-3.8-flash-high)`},
		{"glm-a,native:", `seat 2: "native:": "native:" prefix requires a model id (e.g. native:gemini-3.8-flash-high)`},
		{"native:nw/glm-5.3-flash", `seat 1: "native:nw/glm-5.3-flash": native model id must not contain "/" (use provider/model for external seats)`},
	}
	for _, c := range cases {
		_, err := ParseSeatSpecs(c.spec)
		if err == nil {
			t.Errorf("ParseSeatSpecs(%q) = nil error; want %q", c.spec, c.wantErr)
			continue
		}
		if err.Error() != c.wantErr {
			t.Errorf("ParseSeatSpecs(%q) error =\n  %q\nwant\n  %q", c.spec, err.Error(), c.wantErr)
		}
	}
}

func TestParseSeatSpecs_NativeIDGrammar(t *testing.T) {
	// valid ids
	for _, id := range []string{"g", "g1", "gemini-3.8-flash-high", "a.b_c-d9", "9lives"} {
		if _, err := ParseSeatSpecs("native:" + id); err != nil {
			t.Errorf("native:%s should parse, got %v", id, err)
		}
	}
	// invalid ids (grammar [A-Za-z0-9][A-Za-z0-9._-]*)
	for _, id := range []string{"-lead", ".lead", "_lead", "has space", "a:b", "a+b"} {
		if _, err := ParseSeatSpecs("native:" + id); err == nil {
			t.Errorf("native:%s should be rejected by the id grammar", id)
		}
	}
}

func TestAllNative(t *testing.T) {
	if allNative(nil) {
		t.Error("nil seats must not be all-native (legacy path)")
	}
	if allNative([]SeatSpec{{Kind: SeatKindNative}}) != true {
		t.Error("single native seat must be all-native")
	}
	if allNative([]SeatSpec{{Kind: SeatKindNative}, {Kind: SeatKindProvider}}) {
		t.Error("mixed seats must not be all-native")
	}
}
