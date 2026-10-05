//go:build integration

// cohort_authz_integration_test.go — CHO-2153 / ADR-234, against live Cloud SQL.
//
// Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_Cohort \
//	  ./services/chora-delivery/internal/adapter/repo/pg/...
//
// # WHY THIS FILE EXISTS
//
// The cohort rule is written TWICE — once in Go
// (delivery.Assessment.IsLearnerEligible) and once in SQL
// (pg.SQLListAssessmentsVisibleToLearner) — because the list must filter in the
// database and the START gate must decide in the domain. Two encodings of one
// rule is a standing invitation to drift, and drift here is an authz hole.
//
// That is not hypothetical. It is exactly how the bug being fixed stayed
// invisible: the SQL's open-link branch admitted the whole tenant while the Go
// gate returned `true` unconditionally, and every unit test on each side passed,
// forever, because each side was only ever tested against its OWN idea of the
// rule and NOTHING tested them against EACH OTHER.
//
// So TestIntegration_CohortRule_SQLMatchesDomain drives the full cohort × roster
// matrix through BOTH paths and fails the build if they ever disagree. A stub
// cannot catch this class of bug — only real Postgres, with real RLS, can.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// cohortFixture is one row of the matrix: an assessment shape plus the roster
// state of the learner under test.
type cohortFixture struct {
	name        string
	offering    bool // assessment hangs off the offering
	class       bool // assessment is class-bound
	invited     bool // learner is on invited_gcids
	enrolled    bool // learner has a live course_enrollments row on the offering's 2nd course
	booked      bool // learner has a live bookings row on the class
	wantVisible bool
}

// seedCohortWorld builds a complete, self-cleaning delivery world: a tenant, an
// offering spanning TWO courses (the multi-course shape that makes `course_id`
// alone the wrong predicate), a class, and the learner's roster rows.
func seedCohortWorld(t *testing.T, pool *pgxpool.Pool, tenantID string) (offeringID, courseA, courseB, classID string) {
	t.Helper()
	ctx := context.Background()

	oid, _ := uuid.NewV7()
	cA, _ := uuid.NewV7()
	cB, _ := uuid.NewV7()
	cls, _ := uuid.NewV7()
	instructor, _ := uuid.NewV7()
	offeringID, courseA, courseB, classID = oid.String(), cA.String(), cB.String(), cls.String()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("pool.Begin (seed): %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL chora.tenant_id = '"+tenantID+"'"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("SET LOCAL (seed): %v", err)
	}
	for _, cid := range []string{courseA, courseB} {
		if _, err := tx.Exec(ctx, `
INSERT INTO courses (course_id, tenant_id, instructor_gcid, title, atom_ids,
    public, price_sgd_cents, sf_eligible, max_capacity, created_at, updated_at)
VALUES ($1, $2, $3, 'cohort-authz test course', '{}', false, 0, false, 100, now(), now())
ON CONFLICT (course_id) DO NOTHING
`, cid, tenantID, instructor.String()); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("INSERT courses (seed): %v", err)
		}
	}
	// The offering's course_id column carries ONLY courseA, while its
	// data->'CourseIDs' carries BOTH. A predicate that joins on course_id alone
	// therefore passes every assertion about courseA and silently locks out
	// every learner enrolled solely in courseB — which is precisely the live
	// shape of offering 019f3c77 (courses 019eb059 + 019edf1c).
	// NB: courseA/courseB are passed TWICE — once as uuid (the column) and once
	// as text (the JSONB array). Reusing one placeholder for both deduces
	// "inconsistent types for parameter $3" (SQLSTATE 42P08) and the INSERT
	// dies. Separate placeholders keep each parameter's type unambiguous.
	if _, err := tx.Exec(ctx, `
INSERT INTO offerings (id, tenant_id, course_id, delivery_type, state, data, created_at, updated_at)
VALUES ($1, $2, $3, 'graduate', 'RUNNING',
        jsonb_build_object('CourseIDs', jsonb_build_array($4::text, $5::text)),
        now(), now())
ON CONFLICT (id) DO NOTHING
`, offeringID, tenantID, courseA, courseA, courseB); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("INSERT offerings (seed): %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit (seed): %v", err)
	}

	t.Cleanup(func() {
		cctx := context.Background()
		ctx2, cancel := context.WithTimeout(cctx, 20*time.Second)
		defer cancel()
		cleanupTx, err := pool.Begin(ctx2)
		if err != nil {
			return
		}
		defer func() { _ = cleanupTx.Rollback(ctx2) }()
		_, _ = cleanupTx.Exec(ctx2, "SET LOCAL chora.tenant_id = '"+tenantID+"'")
		_, _ = cleanupTx.Exec(ctx2, `DELETE FROM assessments WHERE tenant_id = $1`, tenantID)
		_, _ = cleanupTx.Exec(ctx2, `DELETE FROM bookings WHERE tenant_id = $1`, tenantID)
		_, _ = cleanupTx.Exec(ctx2, `DELETE FROM course_enrollments WHERE course_id = ANY($1)`, []string{courseA, courseB})
		_, _ = cleanupTx.Exec(ctx2, `DELETE FROM offerings WHERE id = $1`, offeringID)
		_, _ = cleanupTx.Exec(ctx2, `DELETE FROM courses WHERE course_id = ANY($1)`, []string{courseA, courseB})
		_ = cleanupTx.Commit(ctx2)
	})
	return offeringID, courseA, courseB, classID
}

// enrolLearner writes a live course_enrollments row.
func enrolLearner(t *testing.T, pool *pgxpool.Pool, tenantID, courseID, gcid string) {
	t.Helper()
	execTenantScoped(t, pool, tenantID, `
INSERT INTO course_enrollments (tenant_id, course_id, gcid, role, enrolled_at, created_at, updated_at)
VALUES ($1, $2, $3, 'learner', now(), now(), now())
ON CONFLICT (course_id, gcid) DO UPDATE SET deleted_at = NULL
`, tenantID, courseID, gcid)
}

// bookLearner writes a live bookings row onto the class.
func bookLearner(t *testing.T, pool *pgxpool.Pool, tenantID, classID, courseID, gcid string) {
	t.Helper()
	id, _ := uuid.NewV7()
	execTenantScoped(t, pool, tenantID, `
INSERT INTO bookings (id, tenant_id, class_id, course_id, learner_gcid, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'confirmed', now(), now())
`, id.String(), tenantID, classID, courseID, gcid)
}

func execTenantScoped(t *testing.T, pool *pgxpool.Pool, tenantID, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("pool.Begin: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL chora.tenant_id = '"+tenantID+"'"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("SET LOCAL: %v", err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("exec: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 1. The SQL predicate and the domain rule must agree — on every cohort shape.
// -----------------------------------------------------------------------------

func TestIntegration_CohortRule_SQLMatchesDomain(t *testing.T) {
	pool := liveDB(t)
	txr := &liveTxRunner{pool: pool}

	cases := []cohortFixture{
		// OPEN_LINK, offering-attached — the live hole.
		{name: "open-link/offering, not enrolled", offering: true, wantVisible: false},
		{name: "open-link/offering, enrolled", offering: true, enrolled: true, wantVisible: true},
		// OPEN_LINK, freestanding — no cohort to scope to; tenant-open.
		{name: "open-link/freestanding", wantVisible: true},
		// CLASS_BOUND — the check ADR-155 promised "upstream" and never made.
		{name: "class-bound, not booked", class: true, wantVisible: false},
		{name: "class-bound, booked", class: true, booked: true, wantVisible: true},
		// EXPLICIT — the invite list stands alone (85 live rows).
		{name: "explicit, invited", invited: true, wantVisible: true},
		{name: "explicit, not invited", wantVisible: true}, // ← not invited ⇒ OPEN_LINK freestanding
		// INTERSECTION — invited AND on the roster.
		{name: "intersection, invited but not booked", class: true, invited: true, wantVisible: false},
		{name: "intersection, invited and booked", class: true, invited: true, booked: true, wantVisible: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tenant, _ := uuid.NewV7()
			learner, _ := uuid.NewV7()
			tenantID, learnerGCID := tenant.String(), learner.String()

			offeringID, courseA, courseB, classID := seedCohortWorld(t, pool, tenantID)

			// Enrol on courseB — the SECOND course, present only in
			// data->'CourseIDs' and NOT in the offering's course_id column. A
			// predicate that joins on course_id alone fails this case.
			if c.enrolled {
				enrolLearner(t, pool, tenantID, courseB, learnerGCID)
			}
			if c.booked {
				bookLearner(t, pool, tenantID, classID, courseA, learnerGCID)
			}

			in := domain.NewAssessmentInput{
				TenantID:         tenantID,
				InstructorGCID:   uuid.Must(uuid.NewV7()).String(),
				TestSetID:        uuid.Must(uuid.NewV7()).String(),
				Title:            "cohort matrix",
				ScheduledOpenAt:  time.Now().Add(-1 * time.Hour),
				ScheduledCloseAt: time.Now().Add(24 * time.Hour),
				MaxAttempts:      1,
				TotalPoints:      10,
				QuestionCount:    1,
			}
			if c.offering {
				in.OfferingID = offeringID
			}
			if c.class {
				in.ClassID = classID
			}
			if c.invited {
				in.InvitedGCIDs = []string{learnerGCID}
			}
			a, err := domain.NewAssessment(in)
			if err != nil {
				t.Fatalf("NewAssessment: %v", err)
			}
			if err := a.Publish(time.Now().Add(-2 * time.Hour)); err != nil {
				t.Fatalf("Publish: %v", err)
			}
			a.AutoFlipToOpen(time.Now())

			// Bare context throughout — exactly what the HTTP layer passes. Every
			// repo here is responsible for attaching the tenant itself.
			ctx := context.Background()
			aRepo := pg.NewAssessmentRepo(txr)
			if err := aRepo.Save(ctx, a); err != nil {
				t.Fatalf("Save assessment: %v", err)
			}

			// --- Path 1: the SQL predicate (what the learner's LIST returns) --
			items, _, err := aRepo.ListVisibleToLearner(ctx, tenantID, learnerGCID, 50, "")
			if err != nil {
				t.Fatalf("ListVisibleToLearner: %v", err)
			}
			sqlSaysVisible := false
			for _, got := range items {
				if got.ID == a.ID {
					sqlSaysVisible = true
				}
			}

			// --- Path 2: the domain rule (what the START gate decides) --------
			// NB: a BARE context, deliberately. In production the HTTP handler
			// hands the roster r.Context(), which carries NO tenant — the repo
			// must attach it itself (tracing.WithTenantID before RunInTx, the
			// idiom every other repo here follows). Handing this test a
			// pre-tenanted ctx would supply what production does not, and the
			// suite would go green over a repo that 500s on every live call.
			// It did exactly that once; hence this line.
			roster := pg.NewCohortRosterRepo(txr)
			facts, err := domain.ResolveCohortFacts(context.Background(), roster, a, tenantID, learnerGCID)
			if err != nil {
				t.Fatalf("ResolveCohortFacts: %v", err)
			}
			domainSaysVisible := a.IsLearnerVisible(learnerGCID, facts)

			// --- They must agree, and both must match the intent -------------
			if sqlSaysVisible != domainSaysVisible {
				t.Fatalf("SQL and domain DISAGREE (this is the drift that hides authz holes): "+
					"SQL visible=%v, domain visible=%v (facts: enrolled=%v booked=%v)",
					sqlSaysVisible, domainSaysVisible, facts.EnrolledInOffering, facts.BookedOnClass)
			}
			if sqlSaysVisible != c.wantVisible {
				t.Errorf("visible: want %v, got %v (facts: enrolled=%v booked=%v)",
					c.wantVisible, sqlSaysVisible, facts.EnrolledInOffering, facts.BookedOnClass)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// 2. The multi-course predicate, isolated.
// -----------------------------------------------------------------------------

// A learner enrolled ONLY in the offering's SECOND course is still in the
// cohort. This is the assertion that a `course_id`-only join would fail while
// looking perfectly correct — the whole reason the roster query reads
// data->'CourseIDs'.
func TestIntegration_CohortRoster_EnrolledInSecondCourseOfOffering(t *testing.T) {
	pool := liveDB(t)
	txr := &liveTxRunner{pool: pool}

	tenant, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	tenantID, learnerGCID := tenant.String(), learner.String()

	offeringID, courseA, courseB, _ := seedCohortWorld(t, pool, tenantID)
	_ = courseA

	// Bare context — the repo must attach the tenant itself (see the note in
	// TestIntegration_CohortRule_SQLMatchesDomain).
	ctx := context.Background()
	roster := pg.NewCohortRosterRepo(txr)

	enrolled, err := roster.EnrolledInOffering(ctx, tenantID, offeringID, learnerGCID)
	if err != nil {
		t.Fatalf("EnrolledInOffering (before): %v", err)
	}
	if enrolled {
		t.Fatal("a learner enrolled in nothing must not be in the offering's cohort")
	}

	enrolLearner(t, pool, tenantID, courseB, learnerGCID) // the SECOND course only

	enrolled, err = roster.EnrolledInOffering(ctx, tenantID, offeringID, learnerGCID)
	if err != nil {
		t.Fatalf("EnrolledInOffering (after): %v", err)
	}
	if !enrolled {
		t.Error("a learner enrolled in the offering's SECOND course is in the cohort — " +
			"data->'CourseIDs' is the linkage, not the course_id column alone")
	}
}

// A soft-deleted (unenrolled) learner leaves the cohort. Soft-delete is the
// only delete this platform has, so the roster query MUST filter on it — an
// unenrolled learner who still passed the cohort gate would be a silent
// re-admission.
func TestIntegration_CohortRoster_SoftDeletedEnrolmentLeavesCohort(t *testing.T) {
	pool := liveDB(t)
	txr := &liveTxRunner{pool: pool}

	tenant, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	tenantID, learnerGCID := tenant.String(), learner.String()

	offeringID, courseA, _, _ := seedCohortWorld(t, pool, tenantID)
	enrolLearner(t, pool, tenantID, courseA, learnerGCID)

	ctx := context.Background() // bare — the repo attaches the tenant
	roster := pg.NewCohortRosterRepo(txr)

	enrolled, err := roster.EnrolledInOffering(ctx, tenantID, offeringID, learnerGCID)
	if err != nil {
		t.Fatalf("EnrolledInOffering: %v", err)
	}
	if !enrolled {
		t.Fatal("precondition: the enrolled learner must be in the cohort")
	}

	execTenantScoped(t, pool, tenantID,
		`UPDATE course_enrollments SET deleted_at = now() WHERE course_id = $1 AND gcid = $2`,
		courseA, learnerGCID)

	enrolled, err = roster.EnrolledInOffering(ctx, tenantID, offeringID, learnerGCID)
	if err != nil {
		t.Fatalf("EnrolledInOffering (post-unenrol): %v", err)
	}
	if enrolled {
		t.Error("a soft-deleted enrolment must not keep the learner in the cohort")
	}
}

// -----------------------------------------------------------------------------
// 3. RLS still holds on the roster reads.
// -----------------------------------------------------------------------------

// Tenant B must never see tenant A's enrolment. The roster query is an authz
// input, so a cross-tenant leak here would be an authz bypass, not just a data
// leak.
func TestIntegration_CohortRoster_RLSIsolation(t *testing.T) {
	pool := liveDB(t)
	txr := &liveTxRunner{pool: pool}

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	learnerGCID := learner.String()

	offeringID, courseA, _, _ := seedCohortWorld(t, pool, tenantA.String())
	enrolLearner(t, pool, tenantA.String(), courseA, learnerGCID)

	roster := pg.NewCohortRosterRepo(txr)

	ctxA := context.Background()
	if ok, err := roster.EnrolledInOffering(ctxA, tenantA.String(), offeringID, learnerGCID); err != nil || !ok {
		t.Fatalf("tenant A must see its own enrolment: ok=%v err=%v", ok, err)
	}

	ctxB := context.Background()
	ok, err := roster.EnrolledInOffering(ctxB, tenantB.String(), offeringID, learnerGCID)
	if err != nil {
		t.Fatalf("EnrolledInOffering under tenant B: %v", err)
	}
	if ok {
		t.Error("RLS breach: tenant B resolved tenant A's enrolment as cohort membership")
	}
}
