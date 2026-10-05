// certification.go — Postgres adapter for chora_delivery.certifications
// (CHO-2157).
//
// SCHEMA: migrations/0001_initial.sql:199-224.
//
//	certification_id UUID PK · tenant_id · course_id · gcid
//	accomplishments  TEXT[] NOT NULL DEFAULT '{}'
//	score            SMALLINT NULL CHECK (0..100)
//	signature_hash   CHAR(64) NOT NULL      -- deterministic SHA-256
//	issued_at        TIMESTAMPTZ NOT NULL
//	UNIQUE (course_id, gcid)                -- the idempotency guarantee
//	+ enforce_certifications_append_only trigger (no UPDATE, no DELETE)
//
// This table was created in the very first migration and then **never used**:
// until now nothing in the codebase read or wrote it. The only implementation of
// the certification store was CertificationRegistry — a mutex and two maps —
// wired into Deps as a concrete type, so every certificate the platform issued
// lived in one pod's memory and died with it. The learner's TRANSCRIPT row
// survived (issuance emits certification.issued.v1, which chora-consumption
// persists), so learners were shown credentials on their transcript that no
// longer existed anywhere and could not be verified.
//
// Idempotency is the DATABASE's job, not a map's: UNIQUE (course_id, gcid) means
// a duplicate issue is refused even across a restart, a replayed Pub/Sub message
// or a second pod — which is precisely what an auto-issue engine (CHO-2157)
// needs and what an in-memory registry could never give.
package pg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const certSelectCols = `
    certification_id, tenant_id, course_id, gcid,
    accomplishments, score, signature_hash, issued_at
`

// SQLInsertCertification appends a certificate.
//
// ON CONFLICT DO NOTHING (never DO UPDATE — the append-only trigger would reject
// it) so a duplicate issue returns zero rows, which the repo maps to
// ErrCertAlreadyIssued. That is the same answer the in-memory registry gave, but
// it now holds across restarts and across pods.
const SQLInsertCertification = `
INSERT INTO certifications (
    certification_id, tenant_id, course_id, gcid,
    accomplishments, score, signature_hash, issued_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (course_id, gcid) DO NOTHING
RETURNING ` + certSelectCols

// SQLSelectCertificationByID — the public verification read.
const SQLSelectCertificationByID = `
SELECT ` + certSelectCols + `
FROM certifications
WHERE certification_id = $1
  AND tenant_id = $2
`

// SQLSelectCertificationByLearnerCourse — the idempotency + "do they hold one?"
// read.
const SQLSelectCertificationByLearnerCourse = `
SELECT ` + certSelectCols + `
FROM certifications
WHERE tenant_id = $1
  AND gcid = $2
  AND course_id = $3
`

// SQLListCertifications — tenant-scoped, optionally narrowed by learner and/or
// course. Empty string means "any" for each optional filter.
const SQLListCertifications = `
SELECT ` + certSelectCols + `
FROM certifications
WHERE tenant_id = $1
  AND ($2 = '' OR gcid::text = $2)
  AND ($3 = '' OR course_id::text = $3)
ORDER BY issued_at DESC
`

// CertificationRepo is the pg adapter for delivery.CertificationStore.
type CertificationRepo struct {
	tx TxRunner
}

// NewCertificationRepo constructs the repo.
func NewCertificationRepo(tx TxRunner) *CertificationRepo {
	return &CertificationRepo{tx: tx}
}

// Issue mints and PERSISTS a certificate, refusing duplicates at the DB layer.
func (r *CertificationRepo) IssueCtx(ctx context.Context, tenantID, learnerGCID, courseID string, score *int, accomplishments []string) (*domain.Certification, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	cert, err := domain.IssueCertification(tenantID, learnerGCID, courseID, accomplishments)
	if err != nil {
		return nil, err
	}
	// rls.ApplySession reads the tenant from the CONTEXT, not the SQL args.
	ctx = tracing.WithTenantID(ctx, tenantID)

	var out *domain.Certification
	err = r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		acc := cert.Accomplishments
		if acc == nil {
			acc = []string{}
		}
		row := q.QueryRow(ctx, SQLInsertCertification,
			cert.ID, cert.TenantID, cert.CourseID, cert.LearnerID,
			acc, score, cert.Hash, cert.IssuedAt)
		got, scanErr := scanCertification(row)
		if scanErr != nil {
			if errors.Is(scanErr, errNoCertRow) {
				// ON CONFLICT DO NOTHING matched an existing (course_id, gcid).
				return domain.ErrCertAlreadyIssued
			}
			return scanErr
		}
		out = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns the certificate by id.
func (r *CertificationRepo) GetCtx(ctx context.Context, tenantID, id string) (*domain.Certification, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, false, errors.New("pg: tenantID required")
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	var out *domain.Certification
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		got, scanErr := scanCertification(q.QueryRow(ctx, SQLSelectCertificationByID, id, tenantID))
		if scanErr != nil {
			if errors.Is(scanErr, errNoCertRow) {
				return nil // not found — not an error
			}
			return scanErr
		}
		out = got
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, out != nil, nil
}

// GetByLearnerCourse returns the learner's certificate for a course.
func (r *CertificationRepo) GetByLearnerCourseCtx(ctx context.Context, tenantID, learnerGCID, courseID string) (*domain.Certification, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, false, errors.New("pg: tenantID required")
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	var out *domain.Certification
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		got, scanErr := scanCertification(q.QueryRow(ctx, SQLSelectCertificationByLearnerCourse, tenantID, learnerGCID, courseID))
		if scanErr != nil {
			if errors.Is(scanErr, errNoCertRow) {
				return nil
			}
			return scanErr
		}
		out = got
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, out != nil, nil
}

// ListByTenant lists certificates, optionally narrowed by learner and/or course.
func (r *CertificationRepo) ListByTenantCtx(ctx context.Context, tenantID, learnerGCID, courseID string) ([]*domain.Certification, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("pg: tenantID required")
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	out := make([]*domain.Certification, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qerr := q.Query(ctx, SQLListCertifications, tenantID, learnerGCID, courseID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			c, serr := scanCertificationRows(rows)
			if serr != nil {
				return serr
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// errNoCertRow marks a zero-row scan (not-found / ON CONFLICT DO NOTHING).
var errNoCertRow = errors.New("pg: no certification row")

func scanCertification(row Row) (*domain.Certification, error) {
	var (
		id, tenantID, courseID, gcid, hash string
		acc                                []string
		score                              *int16
		issuedAt                           time.Time
	)
	if err := row.Scan(&id, &tenantID, &courseID, &gcid, &acc, &score, &hash, &issuedAt); err != nil {
		if isNoRows(err) {
			return nil, errNoCertRow
		}
		return nil, err
	}
	return &domain.Certification{
		ID:              id,
		TenantID:        tenantID,
		LearnerID:       gcid,
		CourseID:        courseID,
		Accomplishments: acc,
		Hash:            hash,
		IssuedAt:        issuedAt,
	}, nil
}

func scanCertificationRows(rows Rows) (*domain.Certification, error) {
	var (
		id, tenantID, courseID, gcid, hash string
		acc                                []string
		score                              *int16
		issuedAt                           time.Time
	)
	if err := rows.Scan(&id, &tenantID, &courseID, &gcid, &acc, &score, &hash, &issuedAt); err != nil {
		return nil, err
	}
	return &domain.Certification{
		ID:              id,
		TenantID:        tenantID,
		LearnerID:       gcid,
		CourseID:        courseID,
		Accomplishments: acc,
		Hash:            hash,
		IssuedAt:        issuedAt,
	}, nil
}
