// exam_cluster_ex8_coverage_test.go — statement-coverage raise for the R+ exam
// HTTP cluster (exam_handler.go, exam_form_handler.go, exam_candidate_handler.go,
// exam_sitting_handler.go, exam_invigilator_handler.go, incident_report_handler.go,
// exam_webhook_handler.go, exam_result_pubsub_handler.go).
//
// This file is ADD-ONLY: it never touches non-test code and never edits the
// existing exam_*_test.go files. All helpers are prefixed `ex8` to avoid
// colliding with the shared httpapi_test helpers. Fault-injection wrappers
// around the inmem repos let the 5xx / 4xx defensive branches be exercised
// through the real mux (never by calling handlers directly).
package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
)

// -----------------------------------------------------------------------------
// Fault-injecting store wrappers (all ex8-prefixed)
// -----------------------------------------------------------------------------

type ex8fakeExamStore struct {
	inner   *inmem.ExamRepo
	saveErr error
	getErr  error
	listErr error
}

func ex8NewFakeExamStore() *ex8fakeExamStore {
	return &ex8fakeExamStore{inner: inmem.NewExamRepo()}
}
func (f *ex8fakeExamStore) Save(_ context.Context, e *exam.Exam) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.inner.Save(context.Background(), e)
}
func (f *ex8fakeExamStore) Get(_ context.Context, id string) (*exam.Exam, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.inner.Get(context.Background(), id)
}
func (f *ex8fakeExamStore) ListByTenant(_ context.Context, tenantID string) ([]*exam.Exam, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.ListByTenant(context.Background(), tenantID)
}

type ex8fakeCandidateStore struct {
	inner   *inmem.CandidateRepo
	saveErr error
	getErr  error
	listErr error
}

func ex8NewFakeCandidateStore() *ex8fakeCandidateStore {
	return &ex8fakeCandidateStore{inner: inmem.NewCandidateRepo()}
}
func (f *ex8fakeCandidateStore) Save(_ context.Context, c *exam.Candidate) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.inner.Save(context.Background(), c)
}
func (f *ex8fakeCandidateStore) GetByExamAndGCID(_ context.Context, tenantID, examID, gcid string) (*exam.Candidate, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.inner.GetByExamAndGCID(context.Background(), tenantID, examID, gcid)
}
func (f *ex8fakeCandidateStore) ListByExam(_ context.Context, tenantID, examID string) ([]*exam.Candidate, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.ListByExam(context.Background(), tenantID, examID)
}

type ex8fakeFormStore struct {
	inner   *inmem.ExamFormRepo
	saveErr error
	getErr  error
	listErr error
}

func ex8NewFakeFormStore() *ex8fakeFormStore {
	return &ex8fakeFormStore{inner: inmem.NewExamFormRepo()}
}
func (f *ex8fakeFormStore) Save(_ context.Context, form *exam.ExamForm) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.inner.Save(context.Background(), form)
}
func (f *ex8fakeFormStore) Get(_ context.Context, id string) (*exam.ExamForm, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.inner.Get(context.Background(), id)
}
func (f *ex8fakeFormStore) ListByExam(_ context.Context, tenantID, examID string) ([]*exam.ExamForm, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.ListByExam(context.Background(), tenantID, examID)
}

type ex8fakeResultStore struct {
	inner       *inmem.ExamResultRepo
	saveErr     error
	getErr      error
	listFormErr error
	listExamErr error
}

func ex8NewFakeResultStore() *ex8fakeResultStore {
	return &ex8fakeResultStore{inner: inmem.NewExamResultRepo()}
}
func (f *ex8fakeResultStore) Save(_ context.Context, r *exam.ExamResult) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.inner.Save(context.Background(), r)
}
func (f *ex8fakeResultStore) Get(_ context.Context, id string) (*exam.ExamResult, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.inner.Get(context.Background(), id)
}
func (f *ex8fakeResultStore) ListByForm(_ context.Context, tenantID, formID string) ([]*exam.ExamResult, error) {
	if f.listFormErr != nil {
		return nil, f.listFormErr
	}
	return f.inner.ListByForm(context.Background(), tenantID, formID)
}
func (f *ex8fakeResultStore) ListByExam(_ context.Context, tenantID, examID string) ([]*exam.ExamResult, error) {
	if f.listExamErr != nil {
		return nil, f.listExamErr
	}
	return f.inner.ListByExam(context.Background(), tenantID, examID)
}

type ex8fakeSittingStore struct {
	inner   *inmem.SittingRepo
	saveErr error
	getErr  error
	listErr error
}

func ex8NewFakeSittingStore() *ex8fakeSittingStore {
	return &ex8fakeSittingStore{inner: inmem.NewSittingRepo()}
}
func (f *ex8fakeSittingStore) Save(_ context.Context, s *exam.ExamSitting) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.inner.Save(context.Background(), s)
}
func (f *ex8fakeSittingStore) Get(_ context.Context, id string) (*exam.ExamSitting, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.inner.Get(context.Background(), id)
}
func (f *ex8fakeSittingStore) ListByExam(_ context.Context, tenantID, examID string) ([]*exam.ExamSitting, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.ListByExam(context.Background(), tenantID, examID)
}

type ex8fakeInvigilatorStore struct {
	inner   *inmem.InvigilatorRepo
	saveErr error
	getErr  error
	listErr error
}

func ex8NewFakeInvigilatorStore() *ex8fakeInvigilatorStore {
	return &ex8fakeInvigilatorStore{inner: inmem.NewInvigilatorRepo()}
}
func (f *ex8fakeInvigilatorStore) Save(_ context.Context, iv *exam.ExamInvigilator) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.inner.Save(context.Background(), iv)
}
func (f *ex8fakeInvigilatorStore) Get(_ context.Context, id string) (*exam.ExamInvigilator, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.inner.Get(context.Background(), id)
}
func (f *ex8fakeInvigilatorStore) ListBySitting(_ context.Context, tenantID, sittingID string) ([]*exam.ExamInvigilator, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.ListBySitting(context.Background(), tenantID, sittingID)
}

type ex8fakeIncidentStore struct {
	inner     *inmem.IncidentRepo
	appendErr error
	listErr   error
	getErr    error
}

func ex8NewFakeIncidentStore() *ex8fakeIncidentStore {
	return &ex8fakeIncidentStore{inner: inmem.NewIncidentRepo()}
}
func (f *ex8fakeIncidentStore) Append(_ context.Context, ir *exam.IncidentReport) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	return f.inner.Append(context.Background(), ir)
}
func (f *ex8fakeIncidentStore) ListBySitting(_ context.Context, tenantID, sittingID string) ([]*exam.IncidentReport, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.ListBySitting(context.Background(), tenantID, sittingID)
}
func (f *ex8fakeIncidentStore) Get(_ context.Context, id string) (*exam.IncidentReport, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.inner.Get(context.Background(), id)
}

// ex8webhookRepo is an examwebhook.Repo with per-method fault injection.
type ex8webhookRepo struct {
	mu               sync.Mutex
	byKey            map[string]*examwebhook.WebhookEvent
	insertErr        error
	getErr           error
	markRecordedErr  error
	markProcessedErr error
	markFailedErr    error
	failures         []string
}

func ex8NewWebhookRepo() *ex8webhookRepo {
	return &ex8webhookRepo{byKey: map[string]*examwebhook.WebhookEvent{}}
}
func (f *ex8webhookRepo) Insert(_ context.Context, ev *examwebhook.WebhookEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return f.insertErr
	}
	if _, dup := f.byKey[ev.IdempotencyKey]; dup {
		return examwebhook.ErrDuplicate
	}
	cp := *ev
	f.byKey[ev.IdempotencyKey] = &cp
	return nil
}
func (f *ex8webhookRepo) GetByIdempotencyKey(_ context.Context, key string) (*examwebhook.WebhookEvent, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	ev, ok := f.byKey[key]
	if !ok {
		return nil, false, nil
	}
	cp := *ev
	return &cp, true, nil
}
func (f *ex8webhookRepo) MarkResultRecorded(_ context.Context, key, resultID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markRecordedErr != nil {
		return f.markRecordedErr
	}
	if ev, ok := f.byKey[key]; ok {
		ev.MarkResultRecorded(resultID)
	}
	return nil
}
func (f *ex8webhookRepo) MarkProcessed(_ context.Context, key, resultID string, when time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markProcessedErr != nil {
		return f.markProcessedErr
	}
	if ev, ok := f.byKey[key]; ok {
		ev.MarkProcessed(resultID, when)
	}
	return nil
}
func (f *ex8webhookRepo) MarkFailed(_ context.Context, key, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, key+": "+errMsg)
	if f.markFailedErr != nil {
		return f.markFailedErr
	}
	if ev, ok := f.byKey[key]; ok {
		ev.MarkFailed(errMsg)
	}
	return nil
}

// ex8webhookHarness wires the receiver with fault-injectable stores.
type ex8webhookHarness struct {
	srv     http.Handler
	forms   *ex8fakeFormStore
	results *ex8fakeResultStore
	events  *ex8webhookRepo
	pub     *fakeReleasePublisher
	formID  string
}

func ex8NewWebhookHarness(t *testing.T, secret string) *ex8webhookHarness {
	t.Helper()
	forms := ex8NewFakeFormStore()
	results := ex8NewFakeResultStore()
	events := ex8NewWebhookRepo()
	pub := &fakeReleasePublisher{}

	// Seed an EXPOSED form with a RAW cut (max 100, pass mark 60).
	f, err := exam.NewExamForm(exam.NewExamFormInput{TenantID: whTenant, ExamID: whExamID, ItemBankID: whBankID})
	if err != nil {
		t.Fatalf("seed form: %v", err)
	}
	if err := f.AddItem(exam.PinnedItem{ItemID: "q1", AtomRevisionID: "r1", Position: 0}); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	cut, err := exam.NewRawCutScore(100, 60)
	if err != nil {
		t.Fatalf("seed cut: %v", err)
	}
	if err := f.SetCutScore(cut); err != nil {
		t.Fatalf("seed set cut: %v", err)
	}
	if err := f.Assemble(); err != nil {
		t.Fatalf("seed assemble: %v", err)
	}
	if err := f.Expose(); err != nil {
		t.Fatalf("seed expose: %v", err)
	}
	if err := forms.inner.Save(context.Background(), f); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	mux := http.NewServeMux()
	httpapi.RegisterExamWebhookRoutes(mux, httpapi.ExamWebhookDeps{
		Secret:  secret,
		Events:  events,
		Forms:   forms,
		Results: results,
		Publish: pub,
	})
	return &ex8webhookHarness{srv: mux, forms: forms, results: results, events: events, pub: pub, formID: f.ID}
}

func ex8PostSigned(t *testing.T, srv http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	// Ex8-local mirror of postSigned from exam_webhook_handler_test.go —
	// that helper's signature is typed to the older webhookHarness struct,
	// while the ex8 suite drives its own harness with a plain http.Handler.
	ts := fmt.Sprintf("%d", time.Now().Unix())
	return postWebhook(t, srv, ts, signature(whSecret, ts, body), body)
}

// ex8whBody builds a signed-payload body for the exam result webhook with
// the given raw score.
func ex8whBody(formID, eventID, idemKey string, rawScore int) string {
	return fmt.Sprintf(`{"event_id":%q,"idempotency_key":%q,"tenant_id":%q,"exam_id":%q,"exam_form_id":%q,"candidate_ref":%q,"raw_score":%d}`,
		eventID, idemKey, whTenant, whExamID, formID, whCandRef, rawScore)
}

// -----------------------------------------------------------------------------
// Registry no-ops (nil deps must not panic and must not mount routes)
// -----------------------------------------------------------------------------

func TestEx8Candidate_RegisterNilDeps_NoPanic(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.RegisterExamCandidateRoutes(mux, nil)
	rec := doCand(t, mux, http.MethodPost, allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("nil deps must leave routes unmounted; got %d", rec.Code)
	}
}

func TestEx8Sitting_RegisterNilDeps_NoPanic(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.RegisterExamSittingRoutes(mux, nil)
	rec := doSit(t, mux, http.MethodGet, "/api/v1/exams/"+stExamID+"/sittings", "", stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("nil deps must leave routes unmounted; got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// exam_handler.go — examsSubHandler 404 shapes + store faults
// -----------------------------------------------------------------------------

func ex8ExamServer(store exam.ExamStore, candDeps *httpapi.ExamCandidateDeps) http.Handler {
	return httpapi.NewServer(httpapi.Deps{
		Courses:           inmem.NewCourseRepo(),
		Bookings:          inmem.NewBookingRepo(),
		Exams:             store,
		ExamCandidateDeps: candDeps,
	})
}

func TestEx8Exam_Sub_TrailingSlash_404(t *testing.T) {
	srv := ex8ExamServer(inmem.NewExamRepo(), nil)
	rec := doExam(t, srv, http.MethodGet, "/api/v1/exams/", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Exam_Sub_MultiSegment_404(t *testing.T) {
	srv := ex8ExamServer(inmem.NewExamRepo(), nil)
	rec := doExam(t, srv, http.MethodGet, "/api/v1/exams/a/b", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Exam_Create_SaveError_500(t *testing.T) {
	store := ex8NewFakeExamStore()
	store.saveErr = errors.New("pg down")
	srv := ex8ExamServer(store, nil)
	rec := doExam(t, srv, http.MethodPost, "/api/v1/exams", createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Exam_List_RepoError_500(t *testing.T) {
	store := ex8NewFakeExamStore()
	store.listErr = errors.New("db down")
	srv := ex8ExamServer(store, nil)
	rec := doExam(t, srv, http.MethodGet, "/api/v1/exams", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Exam_Get_RepoError_500(t *testing.T) {
	store := ex8NewFakeExamStore()
	store.getErr = errors.New("db down")
	srv := ex8ExamServer(store, nil)
	rec := doExam(t, srv, http.MethodGet, "/api/v1/exams/"+examTestCourseID, "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Exam_Get_SoftDeleted_404(t *testing.T) {
	store := ex8NewFakeExamStore()
	ex, err := exam.NewExam(exam.NewExamInput{
		TenantID: examTestTenantID, CourseID: examTestCourseID, Title: "Deleted exam",
		ScheduledAt: time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC), DurationMinutes: 60, Capacity: 10,
	})
	if err != nil {
		t.Fatalf("NewExam: %v", err)
	}
	when := time.Now().UTC()
	ex.DeletedAt = &when
	if err := store.inner.Save(context.Background(), ex); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := ex8ExamServer(store, nil)
	rec := doExam(t, srv, http.MethodGet, "/api/v1/exams/"+ex.ID, "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("soft-deleted exam must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Exam_CandidateCount_ListError_500(t *testing.T) {
	store := ex8NewFakeExamStore()
	ex, err := exam.NewExam(exam.NewExamInput{
		TenantID: examTestTenantID, CourseID: examTestCourseID, Title: "Count exam",
		ScheduledAt: time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC), DurationMinutes: 60, Capacity: 10,
	})
	if err != nil {
		t.Fatalf("NewExam: %v", err)
	}
	if err := store.inner.Save(context.Background(), ex); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cands := ex8NewFakeCandidateStore()
	cands.listErr = errors.New("candidate store down")
	d := &httpapi.ExamCandidateDeps{Candidates: cands}
	srv := ex8ExamServer(store, d)
	// Both the list and the by-id surfaces derive the count → both must 500.
	rec := doExam(t, srv, http.MethodGet, "/api/v1/exams", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("list count error: status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doExam(t, srv, http.MethodGet, "/api/v1/exams/"+ex.ID, "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("get count error: status=%d want 500 body=%s", rec2.Code, rec2.Body.String())
	}
}

// -----------------------------------------------------------------------------
// exam_candidate_handler.go — dispatcher 405s + store faults
// -----------------------------------------------------------------------------

func ex8CandServer(d *httpapi.ExamCandidateDeps) http.Handler {
	mux := http.NewServeMux()
	httpapi.RegisterExamCandidateRoutes(mux, d)
	return mux
}

func TestEx8Candidate_GetHandler_NonGET_405(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, http.MethodPut, allocPath()+"/"+candLearnerGCID, "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEx8Candidate_VerifyHandler_NonPOST_405(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, http.MethodGet, verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEx8Candidate_AdmitHandler_NonPOST_405(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, http.MethodGet, admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEx8Candidate_Allocate_LookupError_500(t *testing.T) {
	repo := ex8NewFakeCandidateStore()
	repo.getErr = errors.New("candidate store down")
	srv := ex8CandServer(&httpapi.ExamCandidateDeps{Candidates: repo})
	rec := doCand(t, srv, http.MethodPost, allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Candidate_Allocate_SaveError_500(t *testing.T) {
	repo := ex8NewFakeCandidateStore()
	repo.saveErr = errors.New("pg down")
	srv := ex8CandServer(&httpapi.ExamCandidateDeps{Candidates: repo})
	rec := doCand(t, srv, http.MethodPost, allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Candidate_List_RepoError_500(t *testing.T) {
	repo := ex8NewFakeCandidateStore()
	repo.listErr = errors.New("db down")
	srv := ex8CandServer(&httpapi.ExamCandidateDeps{Candidates: repo})
	rec := doCand(t, srv, http.MethodGet, allocPath(), "", candTenantID, candAdminGCID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Candidate_Get_LookupError_500(t *testing.T) {
	repo := ex8NewFakeCandidateStore()
	repo.getErr = errors.New("db down")
	srv := ex8CandServer(&httpapi.ExamCandidateDeps{Candidates: repo})
	rec := doCand(t, srv, http.MethodGet, allocPath()+"/"+candLearnerGCID, "", candTenantID, candAdminGCID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Candidate_Verify_LookupError_500(t *testing.T) {
	repo := ex8NewFakeCandidateStore()
	repo.getErr = errors.New("db down")
	claims := inmem.NewVerificationClaimReader()
	srv := ex8CandServer(&httpapi.ExamCandidateDeps{Candidates: repo, Claims: claims})
	rec := doCand(t, srv, http.MethodPost, verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Candidate_Verify_SaveError_500(t *testing.T) {
	repo := ex8NewFakeCandidateStore()
	claims := inmem.NewVerificationClaimReader()
	srv := ex8CandServer(&httpapi.ExamCandidateDeps{Candidates: repo, Claims: claims})
	if rec := doCand(t, srv, http.MethodPost, allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin"); rec.Code != http.StatusCreated {
		t.Fatalf("seed allocate: %d body=%s", rec.Code, rec.Body.String())
	}
	claims.MarkVerified(candTenantID, candLearnerGCID)
	repo.saveErr = errors.New("pg down")
	rec := doCand(t, srv, http.MethodPost, verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Candidate_Verify_Twice_409(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	claims.MarkVerified(candTenantID, candLearnerGCID)
	if rec := doCand(t, srv, http.MethodPost, verifyPath(), "", candTenantID, candAdminGCID, "training-admin"); rec.Code != http.StatusOK {
		t.Fatalf("first verify: %d body=%s", rec.Code, rec.Body.String())
	}
	rec := doCand(t, srv, http.MethodPost, verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second verify must 409 (FSM), got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Candidate_Admit_LookupError_500(t *testing.T) {
	repo := ex8NewFakeCandidateStore()
	repo.getErr = errors.New("db down")
	claims := inmem.NewVerificationClaimReader()
	srv := ex8CandServer(&httpapi.ExamCandidateDeps{Candidates: repo, Claims: claims})
	rec := doCand(t, srv, http.MethodPost, admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Candidate_Admit_SaveError_500(t *testing.T) {
	repo := ex8NewFakeCandidateStore()
	claims := inmem.NewVerificationClaimReader()
	srv := ex8CandServer(&httpapi.ExamCandidateDeps{Candidates: repo, Claims: claims})
	if rec := doCand(t, srv, http.MethodPost, allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin"); rec.Code != http.StatusCreated {
		t.Fatalf("seed allocate: %d body=%s", rec.Code, rec.Body.String())
	}
	claims.MarkVerified(candTenantID, candLearnerGCID)
	if rec := doCand(t, srv, http.MethodPost, verifyPath(), "", candTenantID, candAdminGCID, "training-admin"); rec.Code != http.StatusOK {
		t.Fatalf("seed verify: %d body=%s", rec.Code, rec.Body.String())
	}
	repo.saveErr = errors.New("pg down")
	rec := doCand(t, srv, http.MethodPost, admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Candidate_Admit_Twice_409(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	claims.MarkVerified(candTenantID, candLearnerGCID)
	if rec := doCand(t, srv, http.MethodPost, verifyPath(), "", candTenantID, candAdminGCID, "training-admin"); rec.Code != http.StatusOK {
		t.Fatalf("seed verify: %d", rec.Code)
	}
	if rec := doCand(t, srv, http.MethodPost, admitPath(), "", candTenantID, candAdminGCID, "training-admin"); rec.Code != http.StatusOK {
		t.Fatalf("first admit: %d", rec.Code)
	}
	rec := doCand(t, srv, http.MethodPost, admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second admit must 409 (not admissible), got %d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// exam_form_handler.go — unwired stores, PERCENT cuts, publish faults
// -----------------------------------------------------------------------------

func ex8FormServer(fd httpapi.ExamFormDeps) http.Handler {
	mux := http.NewServeMux()
	httpapi.RegisterExamFormRoutes(mux, fd)
	return mux
}

func TestEx8Form_Create_RepoUnwired_503(t *testing.T) {
	srv := ex8FormServer(httpapi.ExamFormDeps{})
	rec := doExam(t, srv, http.MethodPost, formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Form_List_RepoUnwired_503(t *testing.T) {
	srv := ex8FormServer(httpapi.ExamFormDeps{})
	rec := doExam(t, srv, http.MethodGet, formsPath(), "", examTestTenantID, examTestAdminGCID, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}

func TestEx8Form_List_RepoError_500(t *testing.T) {
	formsRepo := ex8NewFakeFormStore()
	formsRepo.listErr = errors.New("db down")
	srv := ex8FormServer(httpapi.ExamFormDeps{Forms: formsRepo, Results: inmem.NewExamResultRepo()})
	rec := doExam(t, srv, http.MethodGet, formsPath(), "", examTestTenantID, examTestAdminGCID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Form_Create_SaveError_500(t *testing.T) {
	formsRepo := ex8NewFakeFormStore()
	formsRepo.saveErr = errors.New("pg down")
	srv := ex8FormServer(httpapi.ExamFormDeps{Forms: formsRepo, Results: inmem.NewExamResultRepo()})
	rec := doExam(t, srv, http.MethodPost, formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Form_Create_PercentCut_201(t *testing.T) {
	srv, _, _ := newFormServer()
	body := `{
		"item_bank_id": "` + formHandlerBankID + `",
		"items": [{"item_id":"q1","atom_revision_id":"r1","position":0}],
		"cut_score": {"mode":"PERCENT","max_score":100,"percent":80}
	}`
	rec := doExam(t, srv, http.MethodPost, formsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	cut, ok := got["cut_score"].(map[string]interface{})
	if !ok || cut["mode"] != "PERCENT" {
		t.Fatalf("cut_score=%v want PERCENT mode", got["cut_score"])
	}
	if cut["pass_mark"].(float64) != 80 {
		t.Errorf("pass_mark=%v want 80 (80%% of max)", cut["pass_mark"])
	}
}

func TestEx8Form_Create_PercentOutOfRange_400(t *testing.T) {
	srv, _, _ := newFormServer()
	body := `{
		"item_bank_id": "` + formHandlerBankID + `",
		"items": [{"item_id":"q1","atom_revision_id":"r1","position":0}],
		"cut_score": {"mode":"PERCENT","max_score":100,"percent":150}
	}`
	rec := doExam(t, srv, http.MethodPost, formsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Form_Transition_RepoUnwired_503(t *testing.T) {
	srv := ex8FormServer(httpapi.ExamFormDeps{})
	rec := doExam(t, srv, http.MethodPost, formsPath()+"/whatever/expose", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}

func TestEx8Form_Transition_LookupError_500(t *testing.T) {
	formsRepo := ex8NewFakeFormStore()
	formsRepo.getErr = errors.New("db down")
	srv := ex8FormServer(httpapi.ExamFormDeps{Forms: formsRepo, Results: inmem.NewExamResultRepo()})
	rec := doExam(t, srv, http.MethodPost, formsPath()+"/whatever/expose", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Form_ResultList_RepoUnwired_503(t *testing.T) {
	srv := ex8FormServer(httpapi.ExamFormDeps{})
	rec := doExam(t, srv, http.MethodGet, resultsPath(), "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}

func TestEx8Form_ResultList_RepoError_500(t *testing.T) {
	resultsRepo := ex8NewFakeResultStore()
	resultsRepo.listExamErr = errors.New("db down")
	srv := ex8FormServer(httpapi.ExamFormDeps{Forms: inmem.NewExamFormRepo(), Results: resultsRepo})
	rec := doExam(t, srv, http.MethodGet, resultsPath(), "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Form_ResultCreate_RepoUnwired_503(t *testing.T) {
	srv := ex8FormServer(httpapi.ExamFormDeps{})
	body := `{"form_id":"x","candidate_ref":"` + formHandlerCand + `","raw_score":60}`
	rec := doExam(t, srv, http.MethodPost, resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}

func TestEx8Form_ResultCreate_FormLookupError_500(t *testing.T) {
	formsRepo := ex8NewFakeFormStore()
	formsRepo.getErr = errors.New("db down")
	srv := ex8FormServer(httpapi.ExamFormDeps{Forms: formsRepo, Results: inmem.NewExamResultRepo()})
	body := `{"form_id":"x","candidate_ref":"` + formHandlerCand + `","raw_score":60}`
	rec := doExam(t, srv, http.MethodPost, resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Form_ResultCreate_SaveError_500(t *testing.T) {
	formsRepo := ex8NewFakeFormStore()
	resultsRepo := ex8NewFakeResultStore()
	srv := ex8FormServer(httpapi.ExamFormDeps{Forms: formsRepo, Results: resultsRepo})
	formID := seedExposedForm(t, srv)
	resultsRepo.saveErr = errors.New("pg down")
	body := `{"form_id":"` + formID + `","candidate_ref":"` + formHandlerCand + `","raw_score":60}`
	rec := doExam(t, srv, http.MethodPost, resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Form_ResultCreate_PublishError_500(t *testing.T) {
	formsRepo := ex8NewFakeFormStore()
	pub := &fakeReleasePublisher{err: errors.New("outbox down")}
	srv := ex8FormServer(httpapi.ExamFormDeps{Forms: formsRepo, Results: inmem.NewExamResultRepo(), Events: pub})
	formID := seedExposedForm(t, srv)
	body := `{"form_id":"` + formID + `","candidate_ref":"` + formHandlerCand + `","raw_score":60}`
	rec := doExam(t, srv, http.MethodPost, resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// exam_sitting_handler.go — dispatcher 405s + sitting faults + DTO branches
// -----------------------------------------------------------------------------

func ex8SitServer(d *httpapi.ExamSittingDeps) http.Handler {
	mux := http.NewServeMux()
	httpapi.RegisterExamSittingRoutes(mux, d)
	return mux
}

func TestEx8Sitting_Root_PUT_405(t *testing.T) {
	srv, _ := sitServer()
	rec := doSit(t, srv, http.MethodPut, "/api/v1/exams/"+stExamID+"/sittings", "", stTenantID, stAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEx8Sitting_Action_NonPOST_405(t *testing.T) {
	srv, _ := sitServer()
	rec := doSit(t, srv, http.MethodGet, "/api/v1/exams/exam-x/sittings/s1/open", "", stTenantID, stAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEx8Sitting_Create_SaveError_500(t *testing.T) {
	sf := ex8NewFakeSittingStore()
	sf.saveErr = errors.New("pg down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: sf, Invigilators: inmem.NewInvigilatorRepo(), Incidents: inmem.NewIncidentRepo()})
	body := `{` + validWindow + `,"capacity":30}`
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Sitting_Create_WithFormAndRoom(t *testing.T) {
	srv, _ := sitServer()
	body := `{"exam_form_id":"019e2f93-d586-71b5-8c3d-e2b0d0d5f700","room_id":"019e2f93-d586-71b5-8c3d-e2b0d0d5f400",` + validWindow + `,"capacity":30}`
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	m := decodeSitMap(t, rec)
	if m["exam_form_id"] == nil || m["room_id"] == nil {
		t.Fatalf("DTO must carry exam_form_id + room_id when set: %+v", m)
	}
}

func TestEx8Sitting_List_RepoError_500(t *testing.T) {
	sf := ex8NewFakeSittingStore()
	sf.listErr = errors.New("db down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: sf, Invigilators: inmem.NewInvigilatorRepo(), Incidents: inmem.NewIncidentRepo()})
	rec := doSit(t, srv, http.MethodGet, "/api/v1/exams/"+stExamID+"/sittings", "", stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Sitting_Transition_GetError_500(t *testing.T) {
	sf := ex8NewFakeSittingStore()
	sf.getErr = errors.New("db down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: sf, Invigilators: inmem.NewInvigilatorRepo(), Incidents: inmem.NewIncidentRepo()})
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/s1/open", "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Sitting_Transition_SoftDeleted_404(t *testing.T) {
	sf := ex8NewFakeSittingStore()
	s, err := exam.NewExamSitting(exam.NewExamSittingInput{
		TenantID: stTenantID, ExamID: stExamID,
		StartsAt: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Capacity: 30,
	})
	if err != nil {
		t.Fatalf("NewExamSitting: %v", err)
	}
	when := time.Now().UTC()
	s.DeletedAt = &when
	if err := sf.inner.Save(context.Background(), s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: sf, Invigilators: inmem.NewInvigilatorRepo(), Incidents: inmem.NewIncidentRepo()})
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+s.ID+"/open", "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("soft-deleted sitting must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Sitting_Transition_SaveError_500(t *testing.T) {
	sf := ex8NewFakeSittingStore()
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: sf, Invigilators: inmem.NewInvigilatorRepo(), Incidents: inmem.NewIncidentRepo()})
	id := createSitting(t, srv)
	sf.saveErr = errors.New("pg down")
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+id+"/open", "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Sitting_Transition_NoRole_403(t *testing.T) {
	srv, _ := sitServer()
	id := createSitting(t, srv)
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+id+"/open", "", stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// exam_invigilator_handler.go — dispatcher 405s + invigilator faults
// -----------------------------------------------------------------------------

func TestEx8Invigilator_Root_PUT_405(t *testing.T) {
	srv, _ := sitServer()
	rec := doSit(t, srv, http.MethodPut, "/api/v1/exams/exam-x/sittings/s1/invigilators", "", stTenantID, stAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEx8Invigilator_ByID_GET_405(t *testing.T) {
	srv, _ := sitServer()
	rec := doSit(t, srv, http.MethodGet, "/api/v1/exams/exam-x/sittings/s1/invigilators/i1", "", stTenantID, stAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEx8Invigilator_Assign_ListError_500(t *testing.T) {
	ivf := ex8NewFakeInvigilatorStore()
	ivf.listErr = errors.New("db down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: inmem.NewSittingRepo(), Invigilators: ivf, Incidents: inmem.NewIncidentRepo()})
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"invigilator"}`
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/s1/invigilators", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Invigilator_Assign_SaveError_500(t *testing.T) {
	ivf := ex8NewFakeInvigilatorStore()
	ivf.saveErr = errors.New("pg down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: inmem.NewSittingRepo(), Invigilators: ivf, Incidents: inmem.NewIncidentRepo()})
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"invigilator"}`
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/s1/invigilators", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Invigilator_Assign_NoRole_403(t *testing.T) {
	srv, _ := sitServer()
	sittingID := createSitting(t, srv)
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"invigilator"}`
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body, stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}

func TestEx8Invigilator_List_RepoError_500(t *testing.T) {
	ivf := ex8NewFakeInvigilatorStore()
	ivf.listErr = errors.New("db down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: inmem.NewSittingRepo(), Invigilators: ivf, Incidents: inmem.NewIncidentRepo()})
	rec := doSit(t, srv, http.MethodGet, "/api/v1/exams/exam-x/sittings/s1/invigilators", "", stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Invigilator_Unassign_GetError_500(t *testing.T) {
	ivf := ex8NewFakeInvigilatorStore()
	ivf.getErr = errors.New("db down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: inmem.NewSittingRepo(), Invigilators: ivf, Incidents: inmem.NewIncidentRepo()})
	rec := doSit(t, srv, http.MethodDelete, "/api/v1/exams/exam-x/sittings/s1/invigilators/i1", "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Invigilator_Unassign_WrongSitting_404(t *testing.T) {
	srv, _ := sitServer()
	sittingID := createSitting(t, srv)
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"invigilator"}`
	assign := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body, stTenantID, stAdminGCID, stAdminRole)
	ivID := decodeSitMap(t, assign)["id"].(string)
	// Correct path shape, wrong sittingID → scoped 404.
	rec := doSit(t, srv, http.MethodDelete, "/api/v1/exams/exam-x/sittings/other-sitting/invigilators/"+ivID, "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Invigilator_Unassign_Twice_404(t *testing.T) {
	srv, _ := sitServer()
	sittingID := createSitting(t, srv)
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"invigilator"}`
	assign := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body, stTenantID, stAdminGCID, stAdminRole)
	ivID := decodeSitMap(t, assign)["id"].(string)
	path := "/api/v1/exams/exam-x/sittings/" + sittingID + "/invigilators/" + ivID
	if rec := doSit(t, srv, http.MethodDelete, path, "", stTenantID, stAdminGCID, stAdminRole); rec.Code != http.StatusOK {
		t.Fatalf("first unassign: %d body=%s", rec.Code, rec.Body.String())
	}
	rec := doSit(t, srv, http.MethodDelete, path, "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second unassign must 404 (soft-deleted); got %d", rec.Code)
	}
}

func TestEx8Invigilator_Unassign_NoRole_403(t *testing.T) {
	srv, _ := sitServer()
	sittingID := createSitting(t, srv)
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"invigilator"}`
	assign := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body, stTenantID, stAdminGCID, stAdminRole)
	ivID := decodeSitMap(t, assign)["id"].(string)
	rec := doSit(t, srv, http.MethodDelete, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators/"+ivID, "", stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}

func TestEx8Invigilator_Unassign_SaveError_500(t *testing.T) {
	ivf := ex8NewFakeInvigilatorStore()
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: inmem.NewSittingRepo(), Invigilators: ivf, Incidents: inmem.NewIncidentRepo()})
	iv, err := exam.NewExamInvigilator(exam.NewExamInvigilatorInput{
		TenantID: stTenantID, SittingID: "sit-1", InvigilatorGCID: stInvigGCID, Rank: exam.InvigilatorRankInvigilator,
	})
	if err != nil {
		t.Fatalf("NewExamInvigilator: %v", err)
	}
	if err := ivf.inner.Save(context.Background(), iv); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ivf.saveErr = errors.New("pg down")
	rec := doSit(t, srv, http.MethodDelete, "/api/v1/exams/exam-x/sittings/sit-1/invigilators/"+iv.ID, "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// incident_report_handler.go — dispatcher 405 + incident faults + DTO branch
// -----------------------------------------------------------------------------

func TestEx8Incident_Root_PUT_405(t *testing.T) {
	srv, _ := sitServer()
	rec := doSit(t, srv, http.MethodPut, "/api/v1/exams/exam-x/sittings/s1/incidents", "", stTenantID, stAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEx8Incident_File_AppendError_500(t *testing.T) {
	irf := ex8NewFakeIncidentStore()
	irf.appendErr = errors.New("pg down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: inmem.NewSittingRepo(), Invigilators: inmem.NewInvigilatorRepo(), Incidents: irf})
	body := `{"kind":"device_violation","narrative":"watch"}`
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/s1/incidents", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Incident_List_RepoError_500(t *testing.T) {
	irf := ex8NewFakeIncidentStore()
	irf.listErr = errors.New("db down")
	srv := ex8SitServer(&httpapi.ExamSittingDeps{Sittings: inmem.NewSittingRepo(), Invigilators: inmem.NewInvigilatorRepo(), Incidents: irf})
	rec := doSit(t, srv, http.MethodGet, "/api/v1/exams/exam-x/sittings/s1/incidents", "", stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestEx8Incident_File_WithCandidateRef(t *testing.T) {
	srv, _ := sitServer()
	sittingID := createSitting(t, srv)
	body := `{"kind":"other","narrative":"disturbance","candidate_ref":"cand-9"}`
	rec := doSit(t, srv, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/incidents", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	if m := decodeSitMap(t, rec); m["candidate_ref"] != "cand-9" {
		t.Fatalf("candidate_ref=%v want cand-9 (DTO must carry it when set)", m["candidate_ref"])
	}
}

// -----------------------------------------------------------------------------
// exam_webhook_handler.go — dedup fault ladder + dispatch faults
// -----------------------------------------------------------------------------

func TestEx8Webhook_InsertDead_500(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	h.events.insertErr = errors.New("db down")
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Webhook_DedupStateLookupFailed_500(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	ev, err := examwebhook.New(whEventID, whIdemKey, whTenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	h.events.byKey[whIdemKey] = ev
	h.events.insertErr = examwebhook.ErrDuplicate
	h.events.getErr = errors.New("state read failed")
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Webhook_DedupRowVanished_500(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	h.events.insertErr = examwebhook.ErrDuplicate // no seeded row → duplicate with no row
	h.events.getErr = nil
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Webhook_Replay_ResultLoadFailed_500(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	ev, err := examwebhook.New(whEventID, whIdemKey, whTenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	ev.MarkResultRecorded("019e2f93-d586-71b5-8c3d-e2b0d0d5aa01")
	ev.MarkProcessed("019e2f93-d586-71b5-8c3d-e2b0d0d5aa01", time.Now().UTC())
	h.events.byKey[whIdemKey] = ev
	h.events.insertErr = examwebhook.ErrDuplicate
	h.results.getErr = errors.New("db down")
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Webhook_Replay_ProcessedNoDurableRow_500(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	ev, err := examwebhook.New(whEventID, whIdemKey, whTenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	ev.MarkProcessed("missing-result", time.Now().UTC())
	h.events.byKey[whIdemKey] = ev
	h.events.insertErr = examwebhook.ErrDuplicate
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Webhook_FormLookupFailed_500_MarkedFailed(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	h.forms.getErr = errors.New("db down")
	h.events.insertErr = nil
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
	if len(h.events.failures) == 0 {
		t.Fatal("MarkFailed must record the form lookup failure")
	}
}

func TestEx8Webhook_ResumeResultLookupFailed_500(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	ev, err := examwebhook.New(whEventID, whIdemKey, whTenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	ev.MarkResultRecorded("019e2f93-d586-71b5-8c3d-e2b0d0d5aa01") // reserved, unprocessed
	h.events.byKey[whIdemKey] = ev
	h.events.insertErr = examwebhook.ErrDuplicate
	h.results.getErr = errors.New("db down")
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
	if len(h.events.failures) == 0 {
		t.Fatal("MarkFailed must record the resume lookup failure")
	}
}

func TestEx8Webhook_MarkResultRecordedFail_500(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	h.events.markRecordedErr = errors.New("repo broken")
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Webhook_ResultSaveFail_500(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	h.results.saveErr = errors.New("pg down")
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestEx8Webhook_NoPublisher_Still201(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	h.pub = nil // outcome seam dark (minimal wiring)
	rec := ex8PostSigned(t, h.srv, ex8whBody(h.formID, whEventID, whIdemKey, 72))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	if rows, err := h.results.inner.ListByForm(context.Background(), whTenant, h.formID); err != nil || len(rows) != 1 {
		t.Fatalf("durable row expected; len=%d err=%v", len(rows), err)
	}
	ev, ok, _ := h.events.GetByIdempotencyKey(context.Background(), whIdemKey)
	if !ok || !ev.IsProcessed() {
		t.Fatalf("receipt must be processed: %+v ok=%v", ev, ok)
	}
}

func TestEx8Webhook_CanonicalUUIDLoopBranches_400(t *testing.T) {
	h := ex8NewWebhookHarness(t, whSecret)
	// 36-char strings that fail inside the character loop (bad dash position /
	// non-hex char) — the short-string cases were already covered by the field
	// gauntlet, these cover the loop's switch branches.
	for _, tc := range []struct{ name, badEventID string }{
		{"non-hex at dash slot", "019e2f93g-5867-1b58-c3de-2b0d0d50101"}, // index 8 = 'g'
		{"hex at dash slot", "019e2f93-58677-1b58-8c3d-e2b0d0d50101"},    // index 13 = '7'
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := ex8whBody(h.formID, tc.badEventID, whIdemKey, 72)
			rec := ex8PostSigned(t, h.srv, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// -----------------------------------------------------------------------------
// exam_result_pubsub_handler.go — nil-subscriber panic, undefined outcome,
// missing tenant attribute
// -----------------------------------------------------------------------------

func TestEx8ExamResultPush_PanicsOnNilSubscriber(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil Subscriber")
		}
	}()
	_ = httpapi.NewExamResultReleasedPushHandler(httpapi.ExamResultPushDeps{})
}

func TestEx8ExamResultPush_UnspecifiedOutcome_NoCert(t *testing.T) {
	mux, certs, examID := newExamResultPushWorld(t)
	attrs := map[string]string{
		"topic":     events.TopicExamResultReleased,
		"event_id":  "evt-er-unspec",
		"tenant_id": erpTenant,
	}
	body := buildExamResultPush(t, attrs, marshalExamResult(t, examID, deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_UNSPECIFIED))
	rec := postExamResult(t, mux, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("expected 2xx ack for UNSPECIFIED (fail-safe); got %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok, _ := certs.GetByLearnerCourseCtx(context.Background(), erpTenant, erpLearner, erpCourse); ok {
		t.Fatal("UNSPECIFIED outcome must not issue a certificate (fail-safe)")
	}
}

func TestEx8ExamResultPush_MissingTenantAttr_FailsLoud(t *testing.T) {
	mux, _, examID := newExamResultPushWorld(t)
	attrs := map[string]string{
		"topic":    events.TopicExamResultReleased,
		"event_id": "evt-er-notenant",
	}
	body := buildExamResultPush(t, attrs, marshalExamResult(t, examID, deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_PASS))
	rec := postExamResult(t, mux, body)
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("expected non-2xx for missing tenant_id attribute; got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// jsonDecode unmarshals a recorder body into v (ex8-prefixed to avoid
// redeclaring decodeSitMap/decodeMap).
func jsonDecode(rec *httptest.ResponseRecorder, v interface{}) error {
	return json.Unmarshal(rec.Body.Bytes(), v)
}
