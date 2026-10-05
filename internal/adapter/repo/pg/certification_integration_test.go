//go:build integration

// certification_integration_test.go — CHO-2157, against live Cloud SQL.
//
// The claim under test is DURABILITY, so an in-memory double cannot make it.
// Before this repo existed, `CertificationRegistry` (a mutex and two maps) was
// the only store, and the certifications table — created in migration 0001 —
// was read and written by nothing. Every certificate the platform issued lived
// in one pod's memory and died with it, while the learner's transcript row
// survived via certification.issued.v1. Learners were shown credentials that no
// longer existed anywhere.
//
//	go test -tags integration -run TestIntegration_Certification \
//	  ./services/chora-delivery/internal/adapter/repo/pg/...
package pg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// A certificate must SURVIVE the process. The test proves it by reading it back
// through a SECOND, freshly-constructed repo — the closest a test can get to
// "the pod restarted".
func TestIntegration_CertificationRepo_PersistsAcrossInstances(t *testing.T) {
	pool := liveDB(t)
	txr := &liveTxRunner{pool: pool}

	tenant, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	tenantID, learnerGCID, courseID := tenant.String(), learner.String(), course.String()

	t.Cleanup(func() {
		// certifications is append-only by trigger, so the row cannot be UPDATEd;
		// a DELETE is likewise blocked. Clean up as the owner via a direct
		// statement is not possible either — so the test uses throwaway UUIDs and
		// simply leaves the row. It is inert (a random tenant nobody queries).
	})

	score := 95
	repo := pg.NewCertificationRepo(txr)

	// Bare context — the repo attaches the tenant itself (see
	// reusable_gotcha_integration_test_builds_its_own_rls_context).
	cert, err := repo.IssueCtx(context.Background(), tenantID, learnerGCID, courseID, &score,
		[]string{"Completed the assessment"})
	if err != nil {
		t.Fatalf("IssueCtx: %v", err)
	}
	if cert == nil || cert.ID == "" {
		t.Fatal("IssueCtx returned no certificate")
	}
	if cert.Hash == "" {
		t.Error("the certificate must carry its deterministic signature hash")
	}

	// A DIFFERENT repo instance — nothing shared but the database.
	fresh := pg.NewCertificationRepo(txr)
	got, ok, err := fresh.GetCtx(context.Background(), tenantID, cert.ID)
	if err != nil {
		t.Fatalf("GetCtx: %v", err)
	}
	if !ok {
		t.Fatal("the certificate did not survive — this is the whole bug: " +
			"an in-memory registry loses every certificate on restart")
	}
	if got.LearnerID != learnerGCID || got.CourseID != courseID {
		t.Errorf("round-trip mismatch: learner=%s course=%s", got.LearnerID, got.CourseID)
	}
	if got.Hash != cert.Hash {
		t.Errorf("signature hash must round-trip: want %s, got %s", cert.Hash, got.Hash)
	}
}

// Idempotency is the DATABASE's job. UNIQUE (course_id, gcid) refuses a second
// certificate even across a restart, a replayed Pub/Sub message or a second pod
// — none of which an in-process map can survive. The auto-issue engine depends
// on exactly this: a released grade may be re-delivered at any time.
func TestIntegration_CertificationRepo_DuplicateIsRefusedByTheDatabase(t *testing.T) {
	pool := liveDB(t)
	txr := &liveTxRunner{pool: pool}

	tenant, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	tenantID, learnerGCID, courseID := tenant.String(), learner.String(), course.String()

	repo := pg.NewCertificationRepo(txr)
	if _, err := repo.IssueCtx(context.Background(), tenantID, learnerGCID, courseID, nil, nil); err != nil {
		t.Fatalf("first IssueCtx: %v", err)
	}

	// A brand-new repo — i.e. a restarted pod, with an empty map, replaying the
	// same released grade.
	replay := pg.NewCertificationRepo(txr)
	_, err := replay.IssueCtx(context.Background(), tenantID, learnerGCID, courseID, nil, nil)
	if !errors.Is(err, domain.ErrCertAlreadyIssued) {
		t.Fatalf("a replayed issue must be refused with ErrCertAlreadyIssued, got %v — "+
			"without this an auto-issue engine mints a duplicate credential on every redelivery", err)
	}

	// ...and exactly ONE certificate exists for the pair.
	certs, err := repo.ListByTenantCtx(context.Background(), tenantID, learnerGCID, courseID)
	if err != nil {
		t.Fatalf("ListByTenantCtx: %v", err)
	}
	if len(certs) != 1 {
		t.Errorf("want exactly 1 certificate for (learner, course), got %d", len(certs))
	}
}
