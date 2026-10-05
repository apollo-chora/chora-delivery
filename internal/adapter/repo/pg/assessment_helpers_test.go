// assessment_helpers_test.go — verifies the uuidArray Scanner handles BOTH
// Postgres array wire formats: textual `{a,b,c}` (lib/pq + pgx fallback) and
// the binary header+length-prefix layout pgx emits by default for typed
// `UUID[]` columns. The binary path was the LEG2B-B/C 500 root cause —
// the scanner was rejecting pgx's default binary mode.
package pg

import (
	"encoding/binary"
	"testing"
)

// TestUUIDArray_ScanBinary_SingleElement covers the canonical Phyllis case
// captured in pod logs at 2026-05-16 07:36:32 — a 1-D UUID[] array with one
// invited learner. The trace bytes round-trip back to
// `00000000-0000-7000-8000-000000001999`.
func TestUUIDArray_ScanBinary_SingleElement(t *testing.T) {
	raw := buildBinaryUUIDArray([]string{"00000000-0000-7000-8000-000000001999"})
	var arr uuidArray
	if err := arr.Scan(raw); err != nil {
		t.Fatalf("Scan(binary 1-elem) err=%v", err)
	}
	if len(arr) != 1 {
		t.Fatalf("len=%d, want 1", len(arr))
	}
	if arr[0] != "00000000-0000-7000-8000-000000001999" {
		t.Fatalf("got %q, want 00000000-0000-7000-8000-000000001999", arr[0])
	}
}

func TestUUIDArray_ScanBinary_TwoElements(t *testing.T) {
	in := []string{
		"11111111-1111-7111-8111-111111111111",
		"22222222-2222-7222-8222-222222222222",
	}
	raw := buildBinaryUUIDArray(in)
	var arr uuidArray
	if err := arr.Scan(raw); err != nil {
		t.Fatalf("Scan(binary 2-elem) err=%v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("len=%d, want 2", len(arr))
	}
	for i, want := range in {
		if arr[i] != want {
			t.Fatalf("[%d] got %q, want %q", i, arr[i], want)
		}
	}
}

func TestUUIDArray_ScanBinary_Empty(t *testing.T) {
	raw := buildBinaryUUIDArray(nil)
	var arr uuidArray
	if err := arr.Scan(raw); err != nil {
		t.Fatalf("Scan(binary empty) err=%v", err)
	}
	if len(arr) != 0 {
		t.Fatalf("len=%d, want 0", len(arr))
	}
}

// TestUUIDArray_ScanText still works — backward compat with lib/pq.
func TestUUIDArray_ScanText(t *testing.T) {
	var arr uuidArray
	if err := arr.Scan([]byte("{aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa,bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb}")); err != nil {
		t.Fatalf("Scan(text) err=%v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("len=%d, want 2", len(arr))
	}
	if arr[0] != "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("[0] got %q", arr[0])
	}
}

func TestUUIDArray_ScanNil(t *testing.T) {
	var arr uuidArray
	if err := arr.Scan(nil); err != nil {
		t.Fatalf("Scan(nil) err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

// buildBinaryUUIDArray emits the exact wire format Postgres sends for a 1-D
// `UUID[]` array — same layout pgx routes through `[]byte` into a
// `sql.Scanner`. Mirrors what the assessment.invited_gcids column produced
// on the live cluster (verified against the pod-log hex dump).
func buildBinaryUUIDArray(uuids []string) []byte {
	const headerLen = 20
	const uuidLen = 16
	out := make([]byte, 0, headerLen+len(uuids)*(4+uuidLen))
	ndim := uint32(1)
	if len(uuids) == 0 {
		ndim = 0
	}
	// ndim
	out = appendUint32BE(out, ndim)
	// dataoffset (0 — no null bitmap)
	out = appendUint32BE(out, 0)
	// elemtype = UUID (2950)
	out = appendUint32BE(out, 2950)
	if ndim == 0 {
		return out
	}
	// dim size + lower bound
	out = appendUint32BE(out, uint32(len(uuids)))
	out = appendUint32BE(out, 1)
	// elements
	for _, u := range uuids {
		out = appendUint32BE(out, uuidLen)
		out = append(out, parseUUIDForTest(u)...)
	}
	return out
}

func appendUint32BE(out []byte, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return append(out, b[:]...)
}

// --------------------------------------------------------------------------
// textArray — sibling scanner for `text[]` columns (post migration 0015).
// Covers the variable-element-length case `uuidArray` cannot represent.
// --------------------------------------------------------------------------

func TestTextArray_ScanText_Letters(t *testing.T) {
	var arr textArray
	if err := arr.Scan([]byte("{A,B,C}")); err != nil {
		t.Fatalf("Scan(text letters) err=%v", err)
	}
	if len(arr) != 3 || arr[0] != "A" || arr[1] != "B" || arr[2] != "C" {
		t.Fatalf("got %v, want [A B C]", arr)
	}
}

func TestTextArray_ScanText_Empty(t *testing.T) {
	var arr textArray
	if err := arr.Scan([]byte("{}")); err != nil {
		t.Fatalf("Scan(text empty) err=%v", err)
	}
	if len(arr) != 0 {
		t.Fatalf("len=%d, want 0", len(arr))
	}
}

func TestTextArray_ScanBinary_TwoLetters(t *testing.T) {
	raw := buildBinaryTextArray([]string{"A", "B"})
	var arr textArray
	if err := arr.Scan(raw); err != nil {
		t.Fatalf("Scan(binary 2-elem) err=%v", err)
	}
	if len(arr) != 2 || arr[0] != "A" || arr[1] != "B" {
		t.Fatalf("got %v, want [A B]", arr)
	}
}

func TestTextArray_ScanBinary_Empty(t *testing.T) {
	raw := buildBinaryTextArray(nil)
	var arr textArray
	if err := arr.Scan(raw); err != nil {
		t.Fatalf("Scan(binary empty) err=%v", err)
	}
	if len(arr) != 0 {
		t.Fatalf("len=%d, want 0", len(arr))
	}
}

func TestTextArray_ScanNil(t *testing.T) {
	var arr textArray
	if err := arr.Scan(nil); err != nil {
		t.Fatalf("Scan(nil) err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

// TestTextArray_RejectsUUIDArrayOID ensures the scanner refuses uuid[]
// payloads — symmetric protection against the inverse drift that prompted
// migration 0015 in the first place.
func TestTextArray_RejectsUUIDArrayOID(t *testing.T) {
	uuidRaw := buildBinaryUUIDArray([]string{"00000000-0000-7000-8000-000000000001"})
	var arr textArray
	if err := arr.Scan(uuidRaw); err == nil {
		t.Fatalf("expected error rejecting uuid[] payload, got nil")
	}
}

// buildBinaryTextArray emits the Postgres binary wire format for a 1-D
// `text[]` column (elemOID = 25, variable-length elements).
func buildBinaryTextArray(elems []string) []byte {
	const headerLen = 20
	out := make([]byte, 0, headerLen)
	ndim := uint32(1)
	if len(elems) == 0 {
		ndim = 0
	}
	out = appendUint32BE(out, ndim)
	out = appendUint32BE(out, 0)  // dataoffset
	out = appendUint32BE(out, 25) // elemtype = TEXT
	if ndim == 0 {
		return out
	}
	out = appendUint32BE(out, uint32(len(elems))) // dim size
	out = appendUint32BE(out, 1)                  // lower bound
	for _, s := range elems {
		out = appendUint32BE(out, uint32(len(s)))
		out = append(out, []byte(s)...)
	}
	return out
}

// parseUUIDForTest converts an `8-4-4-4-12` canonical UUID to 16 bytes.
// Test-only — sufficient for the canonical form the test cases use.
func parseUUIDForTest(s string) []byte {
	hexToNibble := func(c byte) byte {
		switch {
		case c >= '0' && c <= '9':
			return c - '0'
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10
		}
		return 0xff
	}
	out := make([]byte, 0, 16)
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			continue
		}
		hi := hexToNibble(s[i])
		i++
		lo := hexToNibble(s[i])
		out = append(out, (hi<<4)|lo)
	}
	return out
}
