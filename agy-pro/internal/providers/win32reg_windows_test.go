//go:build windows

package providers

import (
	"testing"
	"unsafe"
)

// fakeRegQuery builds a regQueryFn over one canned value: a registry type and
// its payload bytes (empty payload = zero-length REG_SZ).
func fakeRegQuery(regType uint32, payload []byte) regQueryFn {
	return func(data *byte, size *uint32) (uint32, uintptr) {
		if data == nil {
			*size = uint32(len(payload))
			return regType, 0
		}
		if len(payload) == 0 {
			*size = 0
			return regType, 0
		}
		copy(unsafe.Slice(data, len(payload)), payload)
		*size = uint32(len(payload))
		return regType, 0
	}
}

// utf16Payload encodes s the way RegQueryValueExW returns REG_SZ data.
func utf16Payload(s string) []byte {
	return utf16BytesFromString(s)
}

// R2-1 regression: a zero-length REG_SZ must come back as ("", present) and
// must never panic - the old code took &buf[0] of a zero-length buffer and
// crashed doctor/KeyValue/repair on one empty value in the hive.
func TestQueryRegValueEmptyStringDoesNotPanic(t *testing.T) {
	for _, regType := range []uint32{regTypeSZ, regTypeExpandSZ} {
		got, ok, err := queryRegValueWith(fakeRegQuery(regType, nil), "EMPTY")
		if err != nil {
			t.Fatalf("type %d: unexpected error: %v", regType, err)
		}
		if !ok {
			t.Errorf("type %d: empty string value must be reported present", regType)
		}
		if got != "" {
			t.Errorf("type %d: got %q; want empty string", regType, got)
		}
	}
}

// R2-1: enumeration keeps going after an empty value - the empty entry is
// collected as "" and later names still resolve.
func TestQueryRegValueEnumerationContinuesAfterEmpty(t *testing.T) {
	hive := map[string]regQueryFn{
		"EMPTY_KEY": fakeRegQuery(regTypeSZ, nil),
		"NEXT_KEY":  fakeRegQuery(regTypeSZ, utf16Payload("still-here")),
	}
	out := map[string]string{}
	for _, name := range []string{"EMPTY_KEY", "NEXT_KEY"} {
		v, ok, err := queryRegValueWith(hive[name], name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !ok {
			t.Fatalf("%s: value unexpectedly absent", name)
		}
		out[name] = v
	}
	if out["EMPTY_KEY"] != "" {
		t.Errorf("EMPTY_KEY = %q; want \"\"", out["EMPTY_KEY"])
	}
	if out["NEXT_KEY"] != "still-here" {
		t.Errorf("NEXT_KEY = %q; want still-here (enumeration must continue)", out["NEXT_KEY"])
	}
}

// Non-string types stay absent; failures stay absent; normal values decode.
func TestQueryRegValueNormalPaths(t *testing.T) {
	if _, ok, err := queryRegValueWith(fakeRegQuery(3 /* REG_BINARY */, []byte{1, 2}), "BIN"); ok || err != nil {
		t.Errorf("binary value = (ok=%v, err=%v); want absent", ok, err)
	}
	failing := func(data *byte, size *uint32) (uint32, uintptr) { return regTypeSZ, 2 }
	if _, ok, err := queryRegValueWith(failing, "ERR"); ok || err != nil {
		t.Errorf("failing query = (ok=%v, err=%v); want absent", ok, err)
	}
	got, ok, err := queryRegValueWith(fakeRegQuery(regTypeSZ, utf16Payload("sk-normal-value")), "OK")
	if err != nil || !ok || got != "sk-normal-value" {
		t.Errorf("normal value = (%q, %v, %v); want (sk-normal-value, true, nil)", got, ok, err)
	}
}
