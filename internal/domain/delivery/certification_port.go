// certification_port.go — the persistence port for issued Certifications
// (CHO-2157).
//
// # WHY THIS PORT EXISTS
//
// `CertificationRegistry` — the only implementation the platform had — is a
// `sync.Mutex` plus two maps. It was wired straight into Deps as a CONCRETE
// type, so there was no seam a durable store could be swapped into, and nothing
// anywhere in the codebase read or wrote the `certifications` TABLE that
// migration 0001 created.
//
// So every certificate the platform ever issued lived in one pod's memory and
// died with it. The learner's TRANSCRIPT row survived (issuance emits
// certification.issued.v1, which chora-consumption persists), so a learner was
// shown a credential on their transcript that no longer existed anywhere and
// could not be verified. Live proof: the certifications table held ZERO rows
// while a certificate issued the previous day appeared on a learner's
// transcript.
//
// An auto-issue engine (CHO-2157) writing into that map would be a stub passed
// off as a feature — and its "no duplicate certification" guarantee would be
// void, because a restart empties the very map the idempotency check reads.
//
// Hence the port: CertificationRegistry stays for tests + local dev, and
// pg.CertificationRepo persists for real.
package delivery

import "context"

// CertificationStore is the persistence port for the Certification aggregate.
//
// Certifications are APPEND-ONLY: the `certifications` table carries an
// enforce_certifications_append_only trigger and a UNIQUE (course_id, gcid)
// constraint, so a duplicate issue is refused by the DATABASE, not merely by an
// in-process map that a restart would wipe.
type CertificationStore interface {
	// Issue mints and persists a certificate. Returns ErrCertAlreadyIssued when
	// (course_id, gcid) already holds one — the idempotency guarantee, enforced
	// at the DB layer so it survives a restart.
	IssueCtx(ctx context.Context, tenantID, learnerGCID, courseID string, score *int, accomplishments []string) (*Certification, error)

	// Get returns the certificate by id (public verification path).
	GetCtx(ctx context.Context, tenantID, id string) (*Certification, bool, error)

	// GetByLearnerCourse returns the learner's certificate for a course.
	GetByLearnerCourseCtx(ctx context.Context, tenantID, learnerGCID, courseID string) (*Certification, bool, error)

	// ListByTenant lists certificates, optionally narrowed by learner / course.
	ListByTenantCtx(ctx context.Context, tenantID, learnerGCID, courseID string) ([]*Certification, error)
}
