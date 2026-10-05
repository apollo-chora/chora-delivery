// offering_om5_fakes_test.go — om5-prefixed failing/paging doubles for the
// statement-coverage battery (TestOm5*) that drives the error branches of the
// offering-nested handlers. Real in-mem stores are used wherever a branch can
// be reached with them; a failing double is introduced ONLY where the handler
// must surface a repo error as a 5xx (or a register/cancel failure as 4xx).
//
// Every helper + type here is prefixed `om5` so it can never collide with the
// sibling offering_*_test.go helpers. No production file is touched.
package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	"github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/module"
	"github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
	"github.com/apollo-chora/chora-delivery/internal/domain/offering_attendance"
	"github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
)

// om5ErrOfferingRepo is a failing OfferingPort double. failGet makes Get
// return an error (repo-outage 500); failSave makes Save return an error
// (durable-write 500). When stored is set, Get returns it (the handler
// mutates the aggregate before Save, exactly like the in-mem repo).
type om5ErrOfferingRepo struct {
	failGet  bool
	failSave bool
	stored   *delivery.Offering
}

var _ delivery.OfferingPort = (*om5ErrOfferingRepo)(nil)

func (r *om5ErrOfferingRepo) Save(_ context.Context, _ *delivery.Offering) error {
	if r.failSave {
		return errors.New("om5: offering save boom")
	}
	return nil
}

func (r *om5ErrOfferingRepo) Get(_ context.Context, _ string) (*delivery.Offering, bool, error) {
	if r.failGet {
		return nil, false, errors.New("om5: offering get boom")
	}
	if r.stored == nil {
		return nil, false, nil
	}
	return r.stored, true, nil
}

func (r *om5ErrOfferingRepo) ListByTenant(context.Context, string) ([]*delivery.Offering, error) {
	return nil, nil
}

func (r *om5ErrOfferingRepo) Search(context.Context, delivery.OfferingQuery) (*delivery.OfferingSearchPage, error) {
	return nil, nil
}

// om5ErrSessionStore is a failing offering_session.Store double (failSave /
// failList). The double-book sentinel is deliberately NOT returned — it would
// take the 409 branch, not the 500 one.
type om5ErrSessionStore struct {
	failSave bool
	failList bool
}

var _ offering_session.Store = (*om5ErrSessionStore)(nil)

func (s *om5ErrSessionStore) Save(_ context.Context, _ *offering_session.OfferingSession) error {
	if s.failSave {
		return errors.New("om5: session save boom")
	}
	return nil
}

func (s *om5ErrSessionStore) Get(_ context.Context, _ string) (*offering_session.OfferingSession, bool, error) {
	return nil, false, nil
}

func (s *om5ErrSessionStore) ListByOffering(_ context.Context, _, _ string) ([]*offering_session.OfferingSession, error) {
	if s.failList {
		return nil, errors.New("om5: session list boom")
	}
	return nil, nil
}

// om5AttendanceStore is a failing offering_attendance.Store double (failUpsert
// / failList).
type om5AttendanceStore struct {
	failUpsert bool
	failList   bool
}

var _ offering_attendance.Store = (*om5AttendanceStore)(nil)

func (s *om5AttendanceStore) Upsert(_ context.Context, _ *offering_attendance.Record) error {
	if s.failUpsert {
		return errors.New("om5: attendance upsert boom")
	}
	return nil
}

func (s *om5AttendanceStore) ListBySession(_ context.Context, _, _ string) ([]*offering_attendance.Record, error) {
	if s.failList {
		return nil, errors.New("om5: attendance list boom")
	}
	return nil, nil
}

// om5ErrAssessmentRepo extends the in-mem assessment repo so a single repo can
// both serve happy reads (via the embedded store) and fail exactly one op.
type om5ErrAssessmentRepo struct {
	*delivery.InMemAssessmentRepo
	failList bool
	failSave bool
}

func (r *om5ErrAssessmentRepo) ListByOffering(ctx context.Context, tenantID, offeringID string, pageSize int, pageToken string) ([]*delivery.Assessment, string, error) {
	if r.failList {
		return nil, "", errors.New("om5: assessment list boom")
	}
	return r.InMemAssessmentRepo.ListByOffering(ctx, tenantID, offeringID, pageSize, pageToken)
}

func (r *om5ErrAssessmentRepo) Save(ctx context.Context, a *delivery.Assessment) error {
	if r.failSave {
		return errors.New("om5: assessment save boom")
	}
	return r.InMemAssessmentRepo.Save(ctx, a)
}

// om5PagingAssessmentRepo returns a real item + a next token on the first
// page and the remainder on the second, so the bounded pagination loops in
// handleOfferingListAssessments + countOfferingAssessments actually iterate.
type om5PagingAssessmentRepo struct {
	*delivery.InMemAssessmentRepo
	page1 []*delivery.Assessment
	page2 []*delivery.Assessment
}

func (r *om5PagingAssessmentRepo) ListByOffering(_ context.Context, _, _ string, _ int, pageToken string) ([]*delivery.Assessment, string, error) {
	if pageToken == "" {
		return r.page1, "om5-next-token", nil
	}
	return r.page2, "", nil
}

// om5ErrProgressPort fails the analytics CourseLearnerProgress read.
type om5ErrProgressPort struct{}

var _ courseprogress.ProgressPort = (*om5ErrProgressPort)(nil)

func (p *om5ErrProgressPort) Advance(context.Context, string, string, string, string, int, int, time.Time) (bool, error) {
	return false, errors.New("om5: progress advance boom")
}

func (p *om5ErrProgressPort) Complete(context.Context, string, string, string, string, time.Time) (bool, error) {
	return false, errors.New("om5: progress complete boom")
}

func (p *om5ErrProgressPort) GetByLearnerCourse(context.Context, string, string, string) (*courseprogress.CourseLearnerProgress, bool, error) {
	return nil, false, errors.New("om5: progress get boom")
}

func (p *om5ErrProgressPort) ListByCourseIDs(context.Context, string, []string) ([]*courseprogress.CourseLearnerProgress, error) {
	return nil, errors.New("om5: progress list boom")
}

// om5ErrCourseStore fails the CJ#2 course Get; every other method delegates to
// the real in-mem CJ#2 store.
type om5ErrCourseStore struct {
	*delivery.InMemCourseCJ2Store
	failGet bool
}

func (s *om5ErrCourseStore) Get(ctx context.Context, tenantID, courseID string) (*delivery.Course, bool, error) {
	if s.failGet {
		return nil, false, errors.New("om5: course get boom")
	}
	return s.InMemCourseCJ2Store.Get(ctx, tenantID, courseID)
}

// om5ErrRosterRepo fails the course-roster materialisation.
type om5ErrRosterRepo struct{}

var _ rostering.CourseRosterRepo = (*om5ErrRosterRepo)(nil)

func (r *om5ErrRosterRepo) ListByCourse(_ context.Context, _, _ string) (*rostering.CourseRoster, error) {
	return nil, errors.New("om5: roster list boom")
}

// om5ErrEnrollStore extends the in-mem enrolment store so each port method can
// fail independently (the exact 500/400 branches of the enrol/remove handlers).
type om5ErrEnrollStore struct {
	*delivery.InMemEnrollmentStore
	failGet      bool
	failCount    bool
	failRegister bool
	failCancel   bool
}

func (s *om5ErrEnrollStore) GetByCourseAndGCID(ctx context.Context, tenantID, courseID, gcid string) (*delivery.Enrollment, bool, error) {
	if s.failGet {
		return nil, false, errors.New("om5: enrolment lookup boom")
	}
	return s.InMemEnrollmentStore.GetByCourseAndGCID(ctx, tenantID, courseID, gcid)
}

func (s *om5ErrEnrollStore) CountByCourse(ctx context.Context, tenantID, courseID string) (int, error) {
	if s.failCount {
		return 0, errors.New("om5: enrolment count boom")
	}
	return s.InMemEnrollmentStore.CountByCourse(ctx, tenantID, courseID)
}

func (s *om5ErrEnrollStore) Register(ctx context.Context, tenantID, courseID, gcid string) (*delivery.Enrollment, error) {
	if s.failRegister {
		return nil, errors.New("om5: enrolment register boom")
	}
	return s.InMemEnrollmentStore.Register(ctx, tenantID, courseID, gcid)
}

func (s *om5ErrEnrollStore) Cancel(ctx context.Context, tenantID, enrollmentID string) error {
	if s.failCancel {
		return errors.New("om5: enrolment cancel boom")
	}
	return s.InMemEnrollmentStore.Cancel(ctx, tenantID, enrollmentID)
}

// om5ErrModuleStore fails the module list.
type om5ErrModuleStore struct {
	*module.InMemModuleStore
	failList bool
}

func (s *om5ErrModuleStore) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*module.Module, error) {
	if s.failList {
		return nil, errors.New("om5: module list boom")
	}
	return s.InMemModuleStore.ListByCourse(ctx, tenantID, courseID)
}

// om5ErrModuleProgressPort fails the per-module progress read.
type om5ErrModuleProgressPort struct{}

var _ moduleprogress.ProgressPort = (*om5ErrModuleProgressPort)(nil)

func (p *om5ErrModuleProgressPort) Advance(context.Context, string, string, string, string, string, *module.Module) (bool, error) {
	return false, errors.New("om5: module progress advance boom")
}

func (p *om5ErrModuleProgressPort) GetByLearnerModule(context.Context, string, string, string) (*moduleprogress.StudentModuleProgress, bool, error) {
	return nil, false, errors.New("om5: module progress get boom")
}

func (p *om5ErrModuleProgressPort) ListByModuleIDs(context.Context, string, []string) ([]*moduleprogress.StudentModuleProgress, error) {
	return nil, errors.New("om5: module progress list boom")
}

// om5ErrCertStore fails the manual-issuance persist.
type om5ErrCertStore struct{}

var _ delivery.CertificationStore = (*om5ErrCertStore)(nil)

func (c *om5ErrCertStore) IssueCtx(_ context.Context, _, _, _ string, _ *int, _ []string) (*delivery.Certification, error) {
	return nil, errors.New("om5: cert issue boom")
}

func (c *om5ErrCertStore) GetCtx(context.Context, string, string) (*delivery.Certification, bool, error) {
	return nil, false, nil
}

func (c *om5ErrCertStore) GetByLearnerCourseCtx(context.Context, string, string, string) (*delivery.Certification, bool, error) {
	return nil, false, nil
}

func (c *om5ErrCertStore) ListByTenantCtx(context.Context, string, string, string) ([]*delivery.Certification, error) {
	return nil, nil
}

// om5FailPublisher is an events.Publisher whose enrolment-created emission
// fails (loud-log branch of handleOfferingEnroll) while every other method
// delegates to the in-memory publisher.
type om5FailPublisher struct {
	*events.InMemoryPublisher
}

func (p *om5FailPublisher) PublishEnrollmentCreated(events.EnrollmentCreated) (events.PublishedEvent, error) {
	return events.PublishedEvent{}, errors.New("om5: enrol publish boom")
}

// om5AnalyticsServer wires the five analytics deps generically; pass nil (or a
// failing double) for the dep whose 503/500 branch the test targets.
func om5AnalyticsServer(oRepo delivery.OfferingPort, courses delivery.CourseCJ2Port, enrollments delivery.EnrollmentListByCoursePort, aRepo delivery.AssessmentRepo, progress courseprogress.ProgressPort, rosters rostering.CourseRosterRepo) http.Handler {
	return httpapi.NewServer(httpapi.Deps{
		Offerings:      oRepo,
		CourseCJ2:      &httpapi.CourseCJ2Deps{Courses: courses},
		Rosters:        rosters,
		AssessmentDeps: &httpapi.AssessmentDeps{Assessments: aRepo},
		CourseProgress: progress,
	})
}

// om5ProgressServer wires the three W7 progress deps (+ the enrolment port for
// the learner path) generically so a nil dep drives its 503 branch.
func om5ProgressServer(oRepo delivery.OfferingPort, mods module.ModulePort, prog moduleprogress.ProgressPort, enroll delivery.EnrollmentPort) http.Handler {
	return httpapi.NewServer(httpapi.Deps{
		Offerings:      oRepo,
		Modules:        mods,
		ModuleProgress: prog,
		Enrollments:    enroll,
	})
}

// om5ErrRoomStore fails the tenant room lookup (the schedule POST room gate's
// "room lookup failed" 500).
type om5ErrRoomStore struct{}

var _ campusops.RoomStore = (*om5ErrRoomStore)(nil)

func (s *om5ErrRoomStore) Save(context.Context, *campusops.Room) error { return nil }

func (s *om5ErrRoomStore) GetForTenant(_ context.Context, _, _ string) (*campusops.Room, bool, error) {
	return nil, false, errors.New("om5: room lookup boom")
}

func (s *om5ErrRoomStore) ListByTenant(context.Context, string) ([]*campusops.Room, error) {
	return nil, nil
}
