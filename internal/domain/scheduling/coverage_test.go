// coverage_test.go — closes the remaining statement gap in class.go:
// ParseISOWeek's year-parse failure (a non-numeric 4-char year that still
// passes the fixed-shape check).
package scheduling_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

func TestParseISOWeek_NonNumericYearRejected(t *testing.T) {
	// "abcd-W02": length 8, '-' at 4, 'W' at 5 — the shape passes, then
	// strconv.Atoi("abcd") fails and must map to ErrInvalidISOWeek.
	if _, _, err := scheduling.ParseISOWeek("abcd-W02"); !errors.Is(err, scheduling.ErrInvalidISOWeek) {
		t.Errorf("non-numeric year: got %v want ErrInvalidISOWeek", err)
	}
}
