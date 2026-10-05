// assessment_helpers.go — pg scanning + null-coercion helpers for the
// Assessment + Submission adapters. Kept in a separate file so the SQL +
// repo logic in assessment.go / submission.go stays readable.
package pg

import (
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// nullString returns nil for empty strings so Postgres NULLs are inserted.
// (`nullStr` already exists in application.go but takes a different param shape.)
func nullString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// nullBytes returns nil for empty byte slices.
func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// nullTimePtr returns nil for nil pointers, value otherwise.
func nullTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return *t
}

// nullFloatPtr returns nil for nil pointers, the float value otherwise.
func nullFloatPtr(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// sqlNullString is a thin alias for sql.NullString to keep scanning tidy.
type sqlNullString = sql.NullString

// sqlNullTime is a thin alias for sql.NullTime.
type sqlNullTime = sql.NullTime

// sqlNullFloat64 is a thin alias.
type sqlNullFloat64 = sql.NullFloat64

// sqlNullBool is a thin alias.
type sqlNullBool = sql.NullBool

// uuidArray is a Postgres `uuid[]` column scanner. Implements sql.Scanner
// for the {a,b,c} textual array representation pgx returns by default when
// the driver isn't told the OID-typed shape.
type uuidArray []string

// Scan parses `{uuid1,uuid2,...}` or a []any / []string driver-supplied value.
func (a *uuidArray) Scan(src any) error {
	if src == nil {
		*a = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		return a.scanBytes(v)
	case string:
		return a.scanBytes([]byte(v))
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			switch s := x.(type) {
			case string:
				if s != "" {
					out = append(out, s)
				}
			case []byte:
				if len(s) > 0 {
					out = append(out, string(s))
				}
			}
		}
		*a = uuidArray(out)
		return nil
	case []string:
		*a = uuidArray(append([]string{}, v...))
		return nil
	}
	return fmt.Errorf("uuidArray: unsupported type %T", src)
}

// Value implements driver.Valuer so callers can BIND a uuidArray on writes.
func (a uuidArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	return "{" + strings.Join(a, ",") + "}", nil
}

// scanBytes parses both Postgres array wire formats:
//
//   - text mode: `{uuid1,uuid2,...}` — emitted by lib/pq + by pgx when the
//     driver lacks the OID type info (older fallback path).
//   - binary mode: the 20-byte header + per-element length-prefix layout pgx
//     emits by default for typed array columns. UUID elements are 16-byte
//     binary form (`0x00..0x0F` ordering as per RFC 4122).
//
// The binary path covers the LEG2B-B/C failure where pgx routes
// `chora_delivery.assessments.invited_gcids` (`UUID[]`) directly as
// `[]byte` to this Scanner, with the leading bytes carrying the
// ndim / hasnull / OID array header.
func (a *uuidArray) scanBytes(raw []byte) error {
	// Empty / NULL.
	if len(raw) == 0 {
		*a = nil
		return nil
	}
	// Text-mode probe — leading `{` is unambiguous; binary header always
	// starts with ndim ∈ [0,6] which is 0x00 0x00 0x00 0x0N.
	if raw[0] == '{' {
		return a.scanTextBytes(raw)
	}
	return a.scanBinary(raw)
}

// scanTextBytes parses `{uuid1,uuid2,...}`.
func (a *uuidArray) scanTextBytes(raw []byte) error {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "{}" || s == "NULL" {
		*a = nil
		return nil
	}
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return fmt.Errorf("uuidArray: malformed array %q", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		*a = nil
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	*a = uuidArray(out)
	return nil
}

// scanBinary parses the Postgres binary array wire format:
//
//	header (20 bytes):
//	  int32 ndim
//	  int32 dataoffset  (non-zero ⇒ NULL bitmap follows)
//	  int32 elemtype OID
//	  per dim {int32 size; int32 lower_bound}   (1 dim here = 8 bytes)
//	per element:
//	  int32 length (-1 ⇒ SQL NULL)
//	  length bytes  (16 for UUID; element layout per pg_type)
//
// Only 1-dimensional UUID[] arrays are supported (matches the schema).
func (a *uuidArray) scanBinary(raw []byte) error {
	// Minimum header for the 0-dim case (empty array): 12 bytes (ndim +
	// dataoffset + elemtype). 1-D arrays add an 8-byte dim record.
	if len(raw) < 12 {
		return fmt.Errorf("uuidArray: binary array too short (%d bytes)", len(raw))
	}
	ndim := int32(binary.BigEndian.Uint32(raw[0:4]))
	dataOffset := int32(binary.BigEndian.Uint32(raw[4:8]))
	elemOID := binary.BigEndian.Uint32(raw[8:12])
	if ndim < 0 || ndim > 1 {
		return fmt.Errorf("uuidArray: ndim=%d unsupported (1-D only)", ndim)
	}
	if elemOID != 2950 {
		return fmt.Errorf("uuidArray: element OID=%d (want 2950 uuid)", elemOID)
	}
	if ndim == 0 {
		*a = nil
		return nil
	}
	// 1-D array — read dim size + lower bound, then each element.
	if len(raw) < 20 {
		return fmt.Errorf("uuidArray: binary array header truncated")
	}
	dimSize := int32(binary.BigEndian.Uint32(raw[12:16]))
	// lower bound at raw[16:20] — unused for our parsing
	pos := 20
	if dataOffset != 0 {
		// NULL bitmap follows the header; one bit per element, rounded up to
		// 4-byte boundary. Skip it — UUID arrays in our schema don't carry
		// NULL elements (column is `NOT NULL DEFAULT '{}'`).
		bitmapLen := int(dataOffset) - 20
		if bitmapLen < 0 || pos+bitmapLen > len(raw) {
			return fmt.Errorf("uuidArray: bad null bitmap offset")
		}
		pos += bitmapLen
	}
	if dimSize <= 0 {
		*a = nil
		return nil
	}
	out := make([]string, 0, dimSize)
	for i := int32(0); i < dimSize; i++ {
		if pos+4 > len(raw) {
			return fmt.Errorf("uuidArray: truncated element-length at #%d", i)
		}
		length := int32(binary.BigEndian.Uint32(raw[pos : pos+4]))
		pos += 4
		if length == -1 {
			continue // SQL NULL
		}
		if length != 16 {
			return fmt.Errorf("uuidArray: element #%d length=%d (want 16 for uuid)", i, length)
		}
		if pos+16 > len(raw) {
			return fmt.Errorf("uuidArray: truncated element bytes at #%d", i)
		}
		out = append(out, formatUUIDBytes(raw[pos:pos+16]))
		pos += 16
	}
	*a = uuidArray(out)
	return nil
}

// formatUUIDBytes renders a 16-byte UUID as the canonical
// `8-4-4-4-12` hex form. RFC 4122 layout — same byte order as the binary
// wire form Postgres emits.
func formatUUIDBytes(b []byte) string {
	const hexset = "0123456789abcdef"
	out := make([]byte, 36)
	dashes := map[int]bool{8: true, 13: true, 18: true, 23: true}
	src := 0
	for i := 0; i < 36; i++ {
		if dashes[i] {
			out[i] = '-'
			continue
		}
		// two hex chars per source byte; flip parity to advance src.
		hi := b[src] >> 4
		lo := b[src] & 0x0f
		out[i] = hexset[hi]
		i++
		out[i] = hexset[lo]
		src++
	}
	return string(out)
}

// invitedGCIDsArray converts a []string to a Postgres-bindable textual form.
func invitedGCIDsArray(in []string) any {
	if len(in) == 0 {
		return "{}"
	}
	return "{" + strings.Join(in, ",") + "}"
}

// textArray is a Postgres `text[]` column scanner — sibling of uuidArray for
// opaque string element types. submission_answers.mcq_choice_ids became
// text[] in migration 0015 (option_ids are positional letters "A"/"B"/...,
// not UUIDs); the uuidArray scanner rejects elemOID != 2950 + enforces
// 16-byte element width, so a text-typed twin is required.
type textArray []string

// Scan accepts `{a,b,c}` text-mode bytes, the binary text[] wire form, or a
// driver-supplied []any / []string. Mirrors uuidArray.Scan minus the UUID
// element-shape assumptions.
func (a *textArray) Scan(src any) error {
	if src == nil {
		*a = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		return a.scanBytes(v)
	case string:
		return a.scanBytes([]byte(v))
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			switch s := x.(type) {
			case string:
				out = append(out, s)
			case []byte:
				out = append(out, string(s))
			}
		}
		*a = textArray(out)
		return nil
	case []string:
		*a = textArray(append([]string{}, v...))
		return nil
	}
	return fmt.Errorf("textArray: unsupported type %T", src)
}

// Value implements driver.Valuer for bind on writes.
func (a textArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	return "{" + strings.Join(a, ",") + "}", nil
}

// scanBytes routes to text-mode or binary-mode parser, matching uuidArray.
func (a *textArray) scanBytes(raw []byte) error {
	if len(raw) == 0 {
		*a = nil
		return nil
	}
	if raw[0] == '{' {
		s := strings.TrimSpace(string(raw))
		if s == "" || s == "{}" || s == "NULL" {
			*a = nil
			return nil
		}
		if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
			return fmt.Errorf("textArray: malformed array %q", s)
		}
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			*a = nil
			return nil
		}
		parts := strings.Split(inner, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.Trim(strings.TrimSpace(p), `"`))
		}
		*a = textArray(out)
		return nil
	}
	return a.scanBinary(raw)
}

// scanBinary parses Postgres binary text[] wire format. Layout matches the
// uuidArray scanner's binary path; differences: elemOID==25 (TEXT), and
// element length is per-row (not fixed 16-byte UUID width).
func (a *textArray) scanBinary(raw []byte) error {
	if len(raw) < 12 {
		return fmt.Errorf("textArray: binary array too short (%d bytes)", len(raw))
	}
	ndim := int32(binary.BigEndian.Uint32(raw[0:4]))
	dataOffset := int32(binary.BigEndian.Uint32(raw[4:8]))
	elemOID := binary.BigEndian.Uint32(raw[8:12])
	if ndim < 0 || ndim > 1 {
		return fmt.Errorf("textArray: ndim=%d unsupported (1-D only)", ndim)
	}
	// 25=TEXT, 1043=VARCHAR — both acceptable for our text[] column.
	if elemOID != 25 && elemOID != 1043 {
		return fmt.Errorf("textArray: element OID=%d (want 25 text or 1043 varchar)", elemOID)
	}
	if ndim == 0 {
		*a = nil
		return nil
	}
	if len(raw) < 20 {
		return fmt.Errorf("textArray: binary array header truncated")
	}
	dimSize := int32(binary.BigEndian.Uint32(raw[12:16]))
	pos := 20
	if dataOffset != 0 {
		bitmapLen := int(dataOffset) - 20
		if bitmapLen < 0 || pos+bitmapLen > len(raw) {
			return fmt.Errorf("textArray: bad null bitmap offset")
		}
		pos += bitmapLen
	}
	if dimSize <= 0 {
		*a = nil
		return nil
	}
	out := make([]string, 0, dimSize)
	for i := int32(0); i < dimSize; i++ {
		if pos+4 > len(raw) {
			return fmt.Errorf("textArray: truncated element-length at #%d", i)
		}
		length := int32(binary.BigEndian.Uint32(raw[pos : pos+4]))
		pos += 4
		if length == -1 {
			continue // SQL NULL element
		}
		if length < 0 || pos+int(length) > len(raw) {
			return fmt.Errorf("textArray: truncated element bytes at #%d (length=%d)", i, length)
		}
		out = append(out, string(raw[pos:pos+int(length)]))
		pos += int(length)
	}
	*a = textArray(out)
	return nil
}

// isNoRows reports whether err is sql.ErrNoRows or pgx's no-row sentinel.
// pgx's no-row error message is `no rows in result set`; the helper handles
// both without an import dependency on pgx.
func isNoRows(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no rows in result set")
}
