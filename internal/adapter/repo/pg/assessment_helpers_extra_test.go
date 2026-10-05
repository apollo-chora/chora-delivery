// assessment_helpers_extra_test.go — in-package (unexported) unit tests for
// the null/array scanning helpers in assessment_helpers.go that
// assessment_helpers_test.go leaves uncovered: uuidArray/textArray driver
// Value(), their []any / []string / string Scan sources, malformed + binary
// error branches, the null-coercion helpers and isNoRows.
package pg

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	_ driver.Valuer = (uuidArray)(nil)
	_ driver.Valuer = (textArray)(nil)
)

// ahxUint32s packs big-endian uint32s into a wire-format prefix.
func ahxUint32s(vals ...uint32) []byte {
	var raw []byte
	for _, v := range vals {
		raw = appendUint32BE(raw, v)
	}
	return raw
}

// --------------------------------------------------------------------------
// uuidArray.Scan — non-[]byte sources + unsupported type
// --------------------------------------------------------------------------

func TestUUIDArray_Scan_StringSource(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	if err := arr.Scan("{aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa,bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb}"); err != nil {
		t.Fatalf("Scan(string) err=%v", err)
	}
	if len(arr) != 2 || arr[0] != "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("got %v", arr)
	}
}

func TestUUIDArray_Scan_SliceOfAny(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.Scan([]any{"aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", []byte("bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb"), ""})
	if err != nil {
		t.Fatalf("Scan([]any) err=%v", err)
	}
	if len(arr) != 2 || arr[0] != "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa" || arr[1] != "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("empty strings must be skipped; got %v", arr)
	}
}

func TestUUIDArray_Scan_SliceOfString(t *testing.T) {
	t.Parallel()
	in := []string{"aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb"}
	var arr uuidArray
	if err := arr.Scan(in); err != nil {
		t.Fatalf("Scan([]string) err=%v", err)
	}
	if len(arr) != 2 || arr[0] != in[0] || arr[1] != in[1] {
		t.Fatalf("got %v, want %v", arr, in)
	}
}

func TestUUIDArray_Scan_UnsupportedType(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.Scan(42)
	if err == nil || !strings.Contains(err.Error(), "uuidArray: unsupported type int") {
		t.Fatalf("expected unsupported-type error; got %v", err)
	}
}

func TestUUIDArray_Scan_EmptyBytes(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	if err := arr.Scan([]byte{}); err != nil {
		t.Fatalf("Scan(empty []byte) err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

// --------------------------------------------------------------------------
// uuidArray.scanTextBytes — malformed + edge inputs
// --------------------------------------------------------------------------

func TestUUIDArray_ScanTextBytes_EmptyString(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	if err := arr.scanTextBytes([]byte("")); err != nil {
		t.Fatalf("scanTextBytes(\"\") err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

func TestUUIDArray_ScanTextBytes_NULL(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	if err := arr.scanTextBytes([]byte("NULL")); err != nil {
		t.Fatalf("scanTextBytes(\"NULL\") err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

func TestUUIDArray_ScanTextBytes_Malformed(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.scanTextBytes([]byte("not-an-array"))
	if err == nil || !strings.Contains(err.Error(), "malformed array") {
		t.Fatalf("expected malformed-array error; got %v", err)
	}
}

func TestUUIDArray_ScanTextBytes_UnbalancedBraces(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.scanTextBytes([]byte("{aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"))
	if err == nil || !strings.Contains(err.Error(), "malformed array") {
		t.Fatalf("expected malformed-array error; got %v", err)
	}
}

func TestUUIDArray_ScanTextBytes_QuotedParts(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	if err := arr.scanTextBytes([]byte(`{"aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa","bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb"}`)); err != nil {
		t.Fatalf("scanTextBytes(quoted) err=%v", err)
	}
	if len(arr) != 2 || arr[1] != "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("got %v", arr)
	}
}

func TestUUIDArray_ScanTextBytes_WhitespaceInner(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	if err := arr.scanTextBytes([]byte("{   }")); err != nil {
		t.Fatalf("scanTextBytes(\"{   }\") err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

// --------------------------------------------------------------------------
// uuidArray.scanBinary — wire-format error branches
// --------------------------------------------------------------------------

func TestUUIDArray_ScanBinary_TooShort(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.scanBinary([]byte{1, 2, 3})
	if err == nil || !strings.Contains(err.Error(), "binary array too short") {
		t.Fatalf("expected too-short error; got %v", err)
	}
}

func TestUUIDArray_ScanBinary_NDimTooHigh(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.scanBinary(ahxUint32s(2, 0, 2950))
	if err == nil || !strings.Contains(err.Error(), "ndim=2 unsupported") {
		t.Fatalf("expected ndim error; got %v", err)
	}
}

func TestUUIDArray_ScanBinary_WrongOID(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.scanBinary(ahxUint32s(1, 0, 25, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "element OID=25 (want 2950 uuid)") {
		t.Fatalf("expected OID error; got %v", err)
	}
}

func TestUUIDArray_ScanBinary_HeaderTruncated(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.scanBinary(ahxUint32s(1, 0, 2950))
	if err == nil || !strings.Contains(err.Error(), "header truncated") {
		t.Fatalf("expected truncated-header error; got %v", err)
	}
}

func TestUUIDArray_ScanBinary_ZeroDimSize(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	if err := arr.scanBinary(ahxUint32s(1, 0, 2950, 0, 1)); err != nil {
		t.Fatalf("scanBinary(dimSize 0) err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

func TestUUIDArray_ScanBinary_DataOffsetBitmap(t *testing.T) {
	t.Parallel()
	raw := ahxUint32s(1, 24, 2950, 1, 1) // dataoffset 24 ⇒ 4-byte NULL bitmap
	raw = appendUint32BE(raw, 0)         // bitmap (no NULL elements)
	raw = appendUint32BE(raw, 16)
	raw = append(raw, parseUUIDForTest("00000000-0000-7000-8000-000000001999")...)
	var arr uuidArray
	if err := arr.scanBinary(raw); err != nil {
		t.Fatalf("scanBinary(bitmap) err=%v", err)
	}
	if len(arr) != 1 || arr[0] != "00000000-0000-7000-8000-000000001999" {
		t.Fatalf("got %v", arr)
	}
}

func TestUUIDArray_ScanBinary_BadBitmapOffset(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.scanBinary(ahxUint32s(1, 500, 2950, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "bad null bitmap offset") {
		t.Fatalf("expected bitmap-offset error; got %v", err)
	}
}

func TestUUIDArray_ScanBinary_TruncatedElemLength(t *testing.T) {
	t.Parallel()
	var arr uuidArray
	err := arr.scanBinary(ahxUint32s(1, 0, 2950, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "truncated element-length at #0") {
		t.Fatalf("expected truncated-length error; got %v", err)
	}
}

func TestUUIDArray_ScanBinary_NullElementSkipped(t *testing.T) {
	t.Parallel()
	raw := ahxUint32s(1, 0, 2950, 2, 1)
	raw = appendUint32BE(raw, 0xFFFFFFFF) // SQL NULL element — skipped
	raw = appendUint32BE(raw, 16)
	raw = append(raw, parseUUIDForTest("22222222-2222-7222-8222-222222222222")...)
	var arr uuidArray
	if err := arr.scanBinary(raw); err != nil {
		t.Fatalf("scanBinary(null elem) err=%v", err)
	}
	if len(arr) != 1 || arr[0] != "22222222-2222-7222-8222-222222222222" {
		t.Fatalf("NULL element must be skipped; got %v", arr)
	}
}

func TestUUIDArray_ScanBinary_BadElemLength(t *testing.T) {
	t.Parallel()
	raw := ahxUint32s(1, 0, 2950, 1, 1)
	raw = appendUint32BE(raw, 8) // uuid elements must be 16 bytes
	raw = append(raw, make([]byte, 8)...)
	var arr uuidArray
	err := arr.scanBinary(raw)
	if err == nil || !strings.Contains(err.Error(), "element #0 length=8 (want 16 for uuid)") {
		t.Fatalf("expected element-length error; got %v", err)
	}
}

func TestUUIDArray_ScanBinary_TruncatedElemBytes(t *testing.T) {
	t.Parallel()
	raw := ahxUint32s(1, 0, 2950, 1, 1)
	raw = appendUint32BE(raw, 16) // claims 16 bytes…
	raw = append(raw, make([]byte, 8)...)
	var arr uuidArray
	err := arr.scanBinary(raw)
	if err == nil || !strings.Contains(err.Error(), "truncated element bytes at #0") {
		t.Fatalf("expected truncated-bytes error; got %v", err)
	}
}

// --------------------------------------------------------------------------
// uuidArray.Value
// --------------------------------------------------------------------------

func TestUUIDArray_Value_Empty(t *testing.T) {
	t.Parallel()
	v, err := (uuidArray(nil)).Value()
	if err != nil {
		t.Fatalf("Value() err=%v", err)
	}
	if v != "{}" {
		t.Fatalf("empty array must bind as %q; got %v", "{}", v)
	}
}

func TestUUIDArray_Value_NonEmpty(t *testing.T) {
	t.Parallel()
	v, err := (uuidArray{"aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb"}).Value()
	if err != nil {
		t.Fatalf("Value() err=%v", err)
	}
	want := "{aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa,bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb}"
	if v != want {
		t.Fatalf("got %v, want %q", v, want)
	}
}

// --------------------------------------------------------------------------
// textArray.Scan — non-[]byte sources + unsupported type
// --------------------------------------------------------------------------

func TestTextArray_Scan_StringSource(t *testing.T) {
	t.Parallel()
	var arr textArray
	if err := arr.Scan("{A,B}"); err != nil {
		t.Fatalf("Scan(string) err=%v", err)
	}
	if len(arr) != 2 || arr[0] != "A" || arr[1] != "B" {
		t.Fatalf("got %v, want [A B]", arr)
	}
}

func TestTextArray_Scan_SliceOfAny(t *testing.T) {
	t.Parallel()
	var arr textArray
	if err := arr.Scan([]any{"A", []byte("B")}); err != nil {
		t.Fatalf("Scan([]any) err=%v", err)
	}
	if len(arr) != 2 || arr[0] != "A" || arr[1] != "B" {
		t.Fatalf("got %v, want [A B]", arr)
	}
}

func TestTextArray_Scan_SliceOfString(t *testing.T) {
	t.Parallel()
	in := []string{"A", "B"}
	var arr textArray
	if err := arr.Scan(in); err != nil {
		t.Fatalf("Scan([]string) err=%v", err)
	}
	if len(arr) != 2 || arr[0] != in[0] || arr[1] != in[1] {
		t.Fatalf("got %v, want %v", arr, in)
	}
}

func TestTextArray_Scan_UnsupportedType(t *testing.T) {
	t.Parallel()
	var arr textArray
	err := arr.Scan(42)
	if err == nil || !strings.Contains(err.Error(), "textArray: unsupported type int") {
		t.Fatalf("expected unsupported-type error; got %v", err)
	}
}

func TestTextArray_Scan_EmptyBytes(t *testing.T) {
	t.Parallel()
	var arr textArray
	if err := arr.Scan([]byte{}); err != nil {
		t.Fatalf("Scan(empty []byte) err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

func TestTextArray_ScanBytes_Malformed(t *testing.T) {
	t.Parallel()
	var arr textArray
	err := arr.Scan([]byte("{A,B"))
	if err == nil || !strings.Contains(err.Error(), "textArray: malformed array") {
		t.Fatalf("expected malformed-array error; got %v", err)
	}
}

func TestTextArray_ScanBytes_WhitespaceInner(t *testing.T) {
	t.Parallel()
	var arr textArray
	if err := arr.Scan([]byte("{   }")); err != nil {
		t.Fatalf("Scan(\"{   }\") err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

// --------------------------------------------------------------------------
// textArray.scanBinary — wire-format error branches
// --------------------------------------------------------------------------

func TestTextArray_ScanBinary_TooShort(t *testing.T) {
	t.Parallel()
	var arr textArray
	err := arr.scanBinary([]byte{1, 2, 3})
	if err == nil || !strings.Contains(err.Error(), "binary array too short") {
		t.Fatalf("expected too-short error; got %v", err)
	}
}

func TestTextArray_ScanBinary_NDimTooHigh(t *testing.T) {
	t.Parallel()
	var arr textArray
	err := arr.scanBinary(ahxUint32s(2, 0, 25))
	if err == nil || !strings.Contains(err.Error(), "ndim=2 unsupported") {
		t.Fatalf("expected ndim error; got %v", err)
	}
}

func TestTextArray_ScanBinary_WrongOID(t *testing.T) {
	t.Parallel()
	var arr textArray
	err := arr.scanBinary(ahxUint32s(1, 0, 2950, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "element OID=2950 (want 25 text or 1043 varchar)") {
		t.Fatalf("expected OID error; got %v", err)
	}
}

func TestTextArray_ScanBinary_HeaderTruncated(t *testing.T) {
	t.Parallel()
	var arr textArray
	err := arr.scanBinary(ahxUint32s(1, 0, 25))
	if err == nil || !strings.Contains(err.Error(), "header truncated") {
		t.Fatalf("expected truncated-header error; got %v", err)
	}
}

func TestTextArray_ScanBinary_ZeroDimSize(t *testing.T) {
	t.Parallel()
	var arr textArray
	if err := arr.scanBinary(ahxUint32s(1, 0, 25, 0, 1)); err != nil {
		t.Fatalf("scanBinary(dimSize 0) err=%v", err)
	}
	if arr != nil {
		t.Fatalf("want nil, got %v", arr)
	}
}

func TestTextArray_ScanBinary_DataOffsetBitmap(t *testing.T) {
	t.Parallel()
	raw := ahxUint32s(1, 20, 25, 1, 1) // dataoffset 20 ⇒ 0-byte NULL bitmap
	raw = appendUint32BE(raw, 1)
	raw = append(raw, 'A')
	var arr textArray
	if err := arr.scanBinary(raw); err != nil {
		t.Fatalf("scanBinary(bitmap) err=%v", err)
	}
	if len(arr) != 1 || arr[0] != "A" {
		t.Fatalf("got %v, want [A]", arr)
	}
}

func TestTextArray_ScanBinary_BadBitmapOffset(t *testing.T) {
	t.Parallel()
	var arr textArray
	err := arr.scanBinary(ahxUint32s(1, 500, 25, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "bad null bitmap offset") {
		t.Fatalf("expected bitmap-offset error; got %v", err)
	}
}

func TestTextArray_ScanBinary_TruncatedElemLength(t *testing.T) {
	t.Parallel()
	var arr textArray
	err := arr.scanBinary(ahxUint32s(1, 0, 25, 1, 1))
	if err == nil || !strings.Contains(err.Error(), "truncated element-length at #0") {
		t.Fatalf("expected truncated-length error; got %v", err)
	}
}

func TestTextArray_ScanBinary_NullElementSkipped(t *testing.T) {
	t.Parallel()
	raw := ahxUint32s(1, 0, 25, 2, 1)
	raw = appendUint32BE(raw, 0xFFFFFFFF) // SQL NULL element — skipped
	raw = appendUint32BE(raw, 1)
	raw = append(raw, 'B')
	var arr textArray
	if err := arr.scanBinary(raw); err != nil {
		t.Fatalf("scanBinary(null elem) err=%v", err)
	}
	if len(arr) != 1 || arr[0] != "B" {
		t.Fatalf("NULL element must be skipped; got %v", arr)
	}
}

func TestTextArray_ScanBinary_BadElemLength(t *testing.T) {
	t.Parallel()
	raw := ahxUint32s(1, 0, 25, 1, 1)
	raw = appendUint32BE(raw, 0xFFFFFFFE) // negative non-NULL length
	var arr textArray
	err := arr.scanBinary(raw)
	if err == nil || !strings.Contains(err.Error(), "truncated element bytes at #0") {
		t.Fatalf("expected truncated-bytes error; got %v", err)
	}
}

func TestTextArray_ScanBinary_TruncatedElemBytes(t *testing.T) {
	t.Parallel()
	raw := ahxUint32s(1, 0, 25, 1, 1)
	raw = appendUint32BE(raw, 5) // claims 5 bytes…
	raw = append(raw, 'A')       // …1 present
	var arr textArray
	err := arr.scanBinary(raw)
	if err == nil || !strings.Contains(err.Error(), "truncated element bytes at #0") {
		t.Fatalf("expected truncated-bytes error; got %v", err)
	}
}

// --------------------------------------------------------------------------
// textArray.Value
// --------------------------------------------------------------------------

func TestTextArray_Value_Empty(t *testing.T) {
	t.Parallel()
	v, err := (textArray(nil)).Value()
	if err != nil {
		t.Fatalf("Value() err=%v", err)
	}
	if v != "{}" {
		t.Fatalf("empty array must bind as %q; got %v", "{}", v)
	}
}

func TestTextArray_Value_NonEmpty(t *testing.T) {
	t.Parallel()
	v, err := (textArray{"A", "B"}).Value()
	if err != nil {
		t.Fatalf("Value() err=%v", err)
	}
	if v != "{A,B}" {
		t.Fatalf("got %v, want {A,B}", v)
	}
}

// --------------------------------------------------------------------------
// Null-coercion helpers
// --------------------------------------------------------------------------

func TestNullBytes_EmptyVsValue(t *testing.T) {
	t.Parallel()
	if v := nullBytes(nil); v != nil {
		t.Fatalf("nullBytes(nil)=%v, want nil", v)
	}
	if v := nullBytes([]byte{}); v != nil {
		t.Fatalf("nullBytes([]byte{})=%v, want nil", v)
	}
	if v := nullBytes([]byte("x")); v == nil || string(v.([]byte)) != "x" {
		t.Fatalf("nullBytes([]byte(\"x\"))=%v, want x", v)
	}
}

func TestNullTimePtr_NilZeroValue(t *testing.T) {
	t.Parallel()
	if v := nullTimePtr(nil); v != nil {
		t.Fatalf("nullTimePtr(nil)=%v, want nil", v)
	}
	zero := time.Time{}
	if v := nullTimePtr(&zero); v != nil {
		t.Fatalf("nullTimePtr(zero)=%v, want nil", v)
	}
	ts := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	if v := nullTimePtr(&ts); v == nil || v.(time.Time) != ts {
		t.Fatalf("nullTimePtr(&ts)=%v, want %v", v, ts)
	}
}

func TestNullFloatPtr_NilVsValue(t *testing.T) {
	t.Parallel()
	if v := nullFloatPtr(nil); v != nil {
		t.Fatalf("nullFloatPtr(nil)=%v, want nil", v)
	}
	f := 88.5
	if v := nullFloatPtr(&f); v == nil || v.(float64) != 88.5 {
		t.Fatalf("nullFloatPtr(&f)=%v, want 88.5", v)
	}
}

func TestInvitedGCIDsArray_EmptyVsValue(t *testing.T) {
	t.Parallel()
	if v := invitedGCIDsArray(nil); v != "{}" {
		t.Fatalf("invitedGCIDsArray(nil)=%v, want {}", v)
	}
	if v := invitedGCIDsArray([]string{"a", "b"}); v != "{a,b}" {
		t.Fatalf("invitedGCIDsArray(...)=%v, want {a,b}", v)
	}
}

// --------------------------------------------------------------------------
// isNoRows
// --------------------------------------------------------------------------

func TestIsNoRows_Branches(t *testing.T) {
	t.Parallel()
	if isNoRows(nil) {
		t.Fatal("nil must not be treated as no-rows")
	}
	if !isNoRows(sql.ErrNoRows) {
		t.Fatal("sql.ErrNoRows must be recognised")
	}
	if !isNoRows(errors.New("no rows in result set")) {
		t.Fatal("pgx-style no-row message must be recognised")
	}
	if isNoRows(errors.New("some other failure")) {
		t.Fatal("unrelated error must not be treated as no-rows")
	}
}
