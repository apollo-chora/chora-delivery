// exclusion_internal_test.go — in-package unit test for the SQLSTATE 23P01
// (exclusion_violation) detector that maps a room-overlap constraint breach to
// the domain ErrRoomDoubleBooked sentinel (ADR-237 / CHO-2191).
package pg

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsExclusionViolation(t *testing.T) {
	t.Parallel()
	if !isExclusionViolation(&pgconn.PgError{Code: "23P01"}) {
		t.Fatal("23P01 (exclusion_violation) must be detected")
	}
	// wrapped
	if !isExclusionViolation(fmt.Errorf("pg: upsert: %w", &pgconn.PgError{Code: "23P01"})) {
		t.Fatal("wrapped 23P01 must be detected")
	}
	if isExclusionViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("23505 is unique_violation, NOT exclusion")
	}
	if isExclusionViolation(errors.New("plain")) {
		t.Fatal("a plain error is not an exclusion violation")
	}
	if isExclusionViolation(nil) {
		t.Fatal("nil is not an exclusion violation")
	}
}
