// pg_error_class.go - deciding whether a Postgres failure inside a subscriber
// is worth retrying.
//
// # The asymmetry that drives every choice here
//
// ACK and NACK are not opposites of equal weight. A NACK is REVERSIBLE: the
// broker redelivers, and after max_delivery_attempts the message lands in the
// DLQ where a human can still read it and replay it. An ACK is FINAL: the
// message is gone, and the only trace left is whatever got logged on the way
// out.
//
// So the rule is not "guess the likelier case". It is: ACK only what is PROVEN
// unretryable, and NACK everything else - including anything unrecognised.
// Mis-NACKing a permanent fault costs 5 wasted retries and a DLQ row.
// Mis-ACKing a transient one destroys a credential a candidate earned. Those
// are not comparable, and the classifier is deliberately biased accordingly.
//
// # Why a permanent fault must NOT be NACKed
//
// The live R+ EXAM walk: an exam carried course_id "course-cspo", a candidate
// PASSED, and the cert INSERT hit `certifications.course_id UUID NOT NULL` with
// SQLSTATE 22P02 (invalid input syntax for type uuid). The subscriber returned
// the error, so it NACK-retried 5 times and dead-lettered. Every one of those
// retries re-delivered the identical bytes to the identical column and was
// refused identically. Redelivery cannot repair the ROW being written, so the
// retries bought nothing and the DLQ filled with a message no replay will ever
// drain. That is the case this file exists to ACK - loudly, never silently.
package subscribers

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATE class = the first two characters of the 5-char code (PostgreSQL
// Appendix A). Classifying by CLASS rather than by full code means a fault we
// have never seen before still lands on the correct side: 22P02 is the one the
// live walk produced, but 22001 (string too long) or 22003 (numeric out of
// range) are the same kind of mistake and need the same answer.
const (
	// sqlStateClassDataException (22) - the DATA is wrong: the value being
	// written cannot be represented in the target type. Permanent by
	// construction, because the next delivery carries the same value.
	sqlStateClassDataException = "22"

	// The transient classes below all mean "the data may be fine; the
	// machinery carrying it was not". A redelivery can genuinely land on a
	// healthy connection / an uncontended row / a running database.
	sqlStateClassConnectionException   = "08" // connection lost, reset, failure
	sqlStateClassTransactionRollback   = "40" // serialization failure, deadlock
	sqlStateClassInsufficientResources = "53" // out of memory, too many connections
	sqlStateClassOperatorIntervention  = "57" // statement timeout, admin shutdown
)

// permanentPGDataFault reports whether err is (or wraps) a Postgres error that
// can NEVER succeed on redelivery, returning the underlying *pgconn.PgError so
// the caller can name the SQLSTATE and the server's own message in its log.
//
// errors.As (not a type assertion) is mandatory: repositories wrap their
// failures with context, so the *pgconn.PgError is always somewhere down the
// chain rather than at the top of it.
//
// Anything that is not a *pgconn.PgError at all - a dial timeout, a context
// deadline, Chora's rls.ErrNoTenantContext - carries no SQLSTATE, is not a
// data fault, and must NACK. It falls through the same safe default.
func permanentPGDataFault(err error) (*pgconn.PgError, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil, false
	}
	if len(pgErr.Code) < 2 {
		// A PgError with no usable SQLSTATE tells us nothing. Stay reversible.
		return nil, false
	}
	switch pgErr.Code[:2] {
	case sqlStateClassDataException:
		// Proven unretryable: the row's own bytes are the problem.
		return pgErr, true
	case sqlStateClassConnectionException, sqlStateClassTransactionRollback,
		sqlStateClassInsufficientResources, sqlStateClassOperatorIntervention:
		// Proven retryable: infrastructure, not data.
		return nil, false
	default:
		// Unrecognised - e.g. class 23 (integrity violation) or 42
		// (insufficient privilege / undefined table). Some of these are
		// permanent in principle, but "in principle" is not the bar for
		// discarding a message. NACK: the DLQ keeps it readable, and a
		// missing GRANT deserves an alert rather than a silent drop.
		return nil, false
	}
}
