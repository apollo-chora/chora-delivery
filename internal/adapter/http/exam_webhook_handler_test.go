// exam_webhook_handler_test.go: TDD for the ADR-193 D1 inbound satellite
// exam-result HMAC webhook receiver (W5 bypass-free slice, CHO-2230).
//
// The adversarial battery mandated by ADR-193 Rollout step 2, exercised
// THROUGH the mux (never by calling the handler func directly, the CHO-2195
// lesson: a constructed-but-unmounted handler is indistinguishable from no
// handler):
//
//   - valid HMAC accepted (201, durable exam_results row, released event teed)
//   - bad signature -> 401 (wrong secret, missing header, malformed header)
//   - stale/future/garbage timestamp -> 401
//   - replayed idempotency_key -> 200 with the ORIGINAL outcome, no 2nd row
//   - exists-but-unprocessed -> re-dispatched, and a durable-result receipt
//     resumes at publish instead of grading twice (no duplicate write)
//   - malformed payload -> 400 naming the exact field (caller fault)
//   - store/publish failure -> 5xx loud (our fault), receipt marked failed
//   - missing secret -> 503 fail-loud (no HMAC trust anchor)
package httpapi_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
)

const (
	whSecret   = "whsec_test_shared_secret_0123456789"
	whTenant   = "019e2f93-d586-71b5-8c3d-e2b0d0d5aa01"
	whExamID   = "019e2f93-d586-71b5-8c3d-e2b0d0d5e001"
	whBankID   = "019e2f93-d586-71b5-8c3d-e2b0d0d5b001"
	whCandRef  = "019e2f93-d586-71b5-8c3d-e2b0d0d5c001"
	whEventID  = "019e2f93-d586-71b5-8c3d-e2b0d0d5f001"
	whEventID2 = "019e2f93-d586-71b5-8c3d-e2b0d0d5f002"
	whIdemKey  = "satellite-a:sitting-9:cand-1"
)

// -----------------------------------------------------------------------------
// Fakes
// -----------------------------------------------------------------------------

// fakeWebhookRepo is an in-memory examwebhook.Repo with injectable faults.
type fakeWebhookRepo struct {
	mu       sync.Mutex
	byKey    map[string]*examwebhook.WebhookEvent
	insertEr error
	getErr   error
	failures []string
}

func newFakeWebhookRepo() *fakeWebhookRepo {
	return &fakeWebhookRepo{byKey: map[string]*examwebhook.WebhookEvent{}}
}

func (f *fakeWebhookRepo) Insert(_ context.Context, ev *examwebhook.WebhookEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertEr != nil {
		return f.insertEr
	}
	if _, dup := f.byKey[ev.IdempotencyKey]; dup {
		return examwebhook.ErrDuplicate
	}
	cp := *ev
	f.byKey[ev.IdempotencyKey] = &cp
	return nil
}

func (f *fakeWebhookRepo) GetByIdempotencyKey(_ context.Context, key string) (*examwebhook.WebhookEvent, bool, error) {
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

func (f *fakeWebhookRepo) MarkResultRecorded(_ context.Context, key, resultID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ev, ok := f.byKey[key]; ok {
		ev.MarkResultRecorded(resultID)
	}
	return nil
}

func (f *fakeWebhookRepo) MarkProcessed(_ context.Context, key, resultID string, when time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ev, ok := f.byKey[key]; ok {
		ev.MarkProcessed(resultID, when)
	}
	return nil
}

func (f *fakeWebhookRepo) MarkFailed(_ context.Context, key, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, key+": "+errMsg)
	if ev, ok := f.byKey[key]; ok {
		ev.MarkFailed(errMsg)
	}
	return nil
}

// capturingResultStore wraps the inmem result repo, capturing the ctx tenant
// on Save (the RLS-from-context contract) + injecting Save faults.
type capturingResultStore struct {
	inner       *inmem.ExamResultRepo
	saveErr     error
	savedTenant string
}

func (c *capturingResultStore) Save(ctx context.Context, r *exam.ExamResult) error {
	if c.saveErr != nil {
		return c.saveErr
	}
	c.savedTenant = tracing.TenantIDFromContext(ctx)
	return c.inner.Save(ctx, r)
}
func (c *capturingResultStore) Get(ctx context.Context, id string) (*exam.ExamResult, bool, error) {
	return c.inner.Get(ctx, id)
}
func (c *capturingResultStore) ListByForm(ctx context.Context, tenantID, formID string) ([]*exam.ExamResult, error) {
	return c.inner.ListByForm(ctx, tenantID, formID)
}
func (c *capturingResultStore) ListByExam(ctx context.Context, tenantID, examID string) ([]*exam.ExamResult, error) {
	return c.inner.ListByExam(ctx, tenantID, examID)
}

// fakeReleasePublisher captures ExamResultReleased publishes.
type fakeReleasePublisher struct {
	mu    sync.Mutex
	calls []exam.ExamResultReleased
	err   error
}

func (f *fakeReleasePublisher) PublishExamResultReleased(_ context.Context, _ string, released exam.ExamResultReleased) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, released)
	return nil
}

func (f *fakeReleasePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// -----------------------------------------------------------------------------
// Harness
// -----------------------------------------------------------------------------

type webhookHarness struct {
	srv     http.Handler
	forms   *inmem.ExamFormRepo
	results *capturingResultStore
	events  *fakeWebhookRepo
	pub     *fakeReleasePublisher
	formID  string
}

func newWebhookHarness(t *testing.T, secret string) *webhookHarness {
	t.Helper()
	forms := inmem.NewExamFormRepo()
	results := &capturingResultStore{inner: inmem.NewExamResultRepo()}
	events := newFakeWebhookRepo()
	pub := &fakeReleasePublisher{}

	// Seed an EXPOSED form with a RAW cut (max 100, pass mark 60) so the
	// receiver can grade against a live, exposure-locked form.
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
	if err := forms.Save(context.Background(), f); err != nil {
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
	return &webhookHarness{srv: mux, forms: forms, results: results, events: events, pub: pub, formID: f.ID}
}

func (h *webhookHarness) body(eventID, idemKey string, rawScore int) string {
	return fmt.Sprintf(`{"event_id":%q,"idempotency_key":%q,"tenant_id":%q,"exam_id":%q,"exam_form_id":%q,"candidate_ref":%q,"raw_score":%d}`,
		eventID, idemKey, whTenant, whExamID, h.formID, whCandRef, rawScore)
}

func signature(secret, ts, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, srv http.Handler, ts, sig, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, httpapi.ExamWebhookPath, strings.NewReader(body))
	if ts != "" {
		req.Header.Set("X-Chora-Timestamp", ts)
	}
	if sig != "" {
		req.Header.Set("X-Chora-Signature", sig)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func postSigned(t *testing.T, h *webhookHarness, body string) *httptest.ResponseRecorder {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	return postWebhook(t, h.srv, ts, signature(whSecret, ts, body), body)
}

func (h *webhookHarness) resultRows(t *testing.T) []*exam.ExamResult {
	t.Helper()
	rows, err := h.results.ListByForm(tracing.WithTenantID(context.Background(), whTenant), whTenant, h.formID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	return rows
}

// -----------------------------------------------------------------------------
// Happy path
// -----------------------------------------------------------------------------

func TestExamWebhook_ValidHMAC_201_DurableRow_EventTeed(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	rec := postSigned(t, h, h.body(whEventID, whIdemKey, 72))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["outcome"] != "PASS" {
		t.Errorf("outcome=%v want PASS", got["outcome"])
	}
	if got["replayed"] != false {
		t.Errorf("replayed=%v want false", got["replayed"])
	}
	if got["result_id"] == nil || got["result_id"] == "" {
		t.Error("result_id missing")
	}
	rows := h.resultRows(t)
	if len(rows) != 1 {
		t.Fatalf("result rows=%d want 1", len(rows))
	}
	if rows[0].Outcome != exam.OutcomePass || rows[0].CandidateRef != whCandRef {
		t.Errorf("row=%+v", rows[0])
	}
	if h.results.savedTenant != whTenant {
		t.Errorf("Save ctx tenant=%q want %q (RLS reads the tenant from ctx)", h.results.savedTenant, whTenant)
	}
	if h.pub.count() != 1 {
		t.Errorf("released publishes=%d want 1", h.pub.count())
	}
	ev, ok, _ := h.events.GetByIdempotencyKey(context.Background(), whIdemKey)
	if !ok || !ev.IsProcessed() {
		t.Fatalf("receipt not processed: %+v ok=%v", ev, ok)
	}
	if ev.TenantID != whTenant {
		t.Errorf("receipt tenant=%q want %q", ev.TenantID, whTenant)
	}
}

func TestExamWebhook_FailOutcome_201(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	rec := postSigned(t, h, h.body(whEventID, whIdemKey, 12))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["outcome"] != "FAIL" {
		t.Errorf("outcome=%v want FAIL", got["outcome"])
	}
}

// -----------------------------------------------------------------------------
// Signature + timestamp gauntlet
// -----------------------------------------------------------------------------

func TestExamWebhook_WrongSecret_401_NothingWritten(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	body := h.body(whEventID, whIdemKey, 72)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	rec := postWebhook(t, h.srv, ts, signature("whsec_WRONG", ts, body), body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", rec.Code, rec.Body.String())
	}
	if n := len(h.resultRows(t)); n != 0 {
		t.Errorf("result rows=%d want 0", n)
	}
	if _, ok, _ := h.events.GetByIdempotencyKey(context.Background(), whIdemKey); ok {
		t.Error("receipt must not exist for a rejected signature")
	}
}

func TestExamWebhook_TamperedBody_401(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	body := h.body(whEventID, whIdemKey, 12)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := signature(whSecret, ts, body)
	tampered := strings.Replace(body, `"raw_score":12`, `"raw_score":99`, 1)
	rec := postWebhook(t, h.srv, ts, sig, tampered)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestExamWebhook_MissingOrMalformedSignature_401(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	body := h.body(whEventID, whIdemKey, 72)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	for name, sig := range map[string]string{
		"missing":   "",
		"no prefix": "deadbeef",
		"bad hex":   "sha256=zzzz",
		"empty hex": "sha256=",
	} {
		t.Run(name, func(t *testing.T) {
			rec := postWebhook(t, h.srv, ts, sig, body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d want 401", rec.Code)
			}
		})
	}
}

func TestExamWebhook_TimestampGauntlet_401(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	body := h.body(whEventID, whIdemKey, 72)
	for name, ts := range map[string]string{
		"stale":   strconv.FormatInt(time.Now().Add(-6*time.Minute).Unix(), 10),
		"future":  strconv.FormatInt(time.Now().Add(6*time.Minute).Unix(), 10),
		"garbage": "not-a-unix-second",
		"missing": "",
	} {
		t.Run(name, func(t *testing.T) {
			sig := signature(whSecret, ts, body)
			rec := postWebhook(t, h.srv, ts, sig, body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d want 401 body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Idempotency
// -----------------------------------------------------------------------------

func TestExamWebhook_Replay_200_OriginalOutcome_NoSecondRow(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	first := postSigned(t, h, h.body(whEventID, whIdemKey, 72))
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d body=%s", first.Code, first.Body.String())
	}
	var f map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &f)

	// Same idempotency_key, even under a NEW envelope event_id: still a replay.
	second := postSigned(t, h, h.body(whEventID2, whIdemKey, 72))
	if second.Code != http.StatusOK {
		t.Fatalf("replay: %d want 200 body=%s", second.Code, second.Body.String())
	}
	var s map[string]interface{}
	_ = json.Unmarshal(second.Body.Bytes(), &s)
	if s["replayed"] != true {
		t.Errorf("replayed=%v want true", s["replayed"])
	}
	if s["result_id"] != f["result_id"] {
		t.Errorf("replay result_id=%v want original %v", s["result_id"], f["result_id"])
	}
	if s["outcome"] != f["outcome"] {
		t.Errorf("replay outcome=%v want original %v", s["outcome"], f["outcome"])
	}
	if n := len(h.resultRows(t)); n != 1 {
		t.Fatalf("result rows=%d want exactly 1 (no duplicate write)", n)
	}
	if h.pub.count() != 1 {
		t.Errorf("released publishes=%d want 1 (no re-publish on replay)", h.pub.count())
	}
}

func TestExamWebhook_ExistsUnprocessed_NoResult_IsReDispatched(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	// Seed a stranded receipt: received, never dispatched (pod died mid-flight).
	ev, err := examwebhook.New(whEventID, whIdemKey, whTenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	if err := h.events.Insert(context.Background(), ev); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	rec := postSigned(t, h, h.body(whEventID, whIdemKey, 72))
	if rec.Code != http.StatusOK {
		t.Fatalf("re-dispatch: %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if n := len(h.resultRows(t)); n != 1 {
		t.Fatalf("result rows=%d want 1", n)
	}
	got, ok, _ := h.events.GetByIdempotencyKey(context.Background(), whIdemKey)
	if !ok || !got.IsProcessed() {
		t.Fatalf("receipt must be processed after re-dispatch: %+v", got)
	}
}

func TestExamWebhook_PublishFails_500_ThenRetryCompletesWithoutDuplicate(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	h.pub.err = errors.New("outbox down")

	first := postSigned(t, h, h.body(whEventID, whIdemKey, 72))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first: %d want 500 body=%s", first.Code, first.Body.String())
	}
	if n := len(h.resultRows(t)); n != 1 {
		t.Fatalf("result rows after failed publish=%d want 1 (row is durable)", n)
	}
	ev, ok, _ := h.events.GetByIdempotencyKey(context.Background(), whIdemKey)
	if !ok || ev.IsProcessed() {
		t.Fatalf("receipt must exist unprocessed: %+v ok=%v", ev, ok)
	}
	if ev.ResultID == "" {
		t.Fatal("receipt must remember the durable result id (resume marker)")
	}
	if len(h.events.failures) == 0 {
		t.Fatal("MarkFailed must record the publish failure reason")
	}

	// Satellite retries: the receiver must resume at publish, never grade twice.
	h.pub.err = nil
	second := postSigned(t, h, h.body(whEventID, whIdemKey, 72))
	if second.Code != http.StatusOK {
		t.Fatalf("retry: %d want 200 body=%s", second.Code, second.Body.String())
	}
	if n := len(h.resultRows(t)); n != 1 {
		t.Fatalf("result rows after retry=%d want exactly 1 (no duplicate write)", n)
	}
	if h.pub.count() != 1 {
		t.Errorf("released publishes=%d want 1", h.pub.count())
	}
	got, ok, _ := h.events.GetByIdempotencyKey(context.Background(), whIdemKey)
	if !ok || !got.IsProcessed() {
		t.Fatalf("receipt must be processed after retry: %+v", got)
	}
}

// -----------------------------------------------------------------------------
// Caller-fault 4xx
// -----------------------------------------------------------------------------

func TestExamWebhook_MalformedPayload_400_NamesField(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	base := func(overrideKey, overrideVal string) string {
		m := map[string]interface{}{
			"event_id":        whEventID,
			"idempotency_key": whIdemKey,
			"tenant_id":       whTenant,
			"exam_id":         whExamID,
			"exam_form_id":    h.formID,
			"candidate_ref":   whCandRef,
			"raw_score":       72,
		}
		if overrideVal == "" {
			delete(m, overrideKey)
		} else {
			m[overrideKey] = overrideVal
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	cases := []struct {
		name, field, value string
	}{
		{"missing event_id", "event_id", ""},
		{"missing idempotency_key", "idempotency_key", ""},
		{"missing tenant_id", "tenant_id", ""},
		{"bad tenant uuid", "tenant_id", "tenant-alpha"},
		{"missing exam_id", "exam_id", ""},
		{"bad exam uuid", "exam_id", "nope"},
		{"missing exam_form_id", "exam_form_id", ""},
		{"bad form uuid", "exam_form_id", "nope"},
		{"missing candidate_ref", "candidate_ref", ""},
		{"bad candidate uuid", "candidate_ref", "learner-7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := base(tc.field, tc.value)
			rec := postSigned(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.field) {
				t.Errorf("400 body must name %q: %s", tc.field, rec.Body.String())
			}
		})
	}
}

func TestExamWebhook_NonJSONBody_400(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	rec := postSigned(t, h, "this is not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestExamWebhook_RawScoreOutOfRange_400(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	rec := postSigned(t, h, h.body(whEventID, whIdemKey, 101)) // max is 100
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if n := len(h.resultRows(t)); n != 0 {
		t.Errorf("result rows=%d want 0", n)
	}
}

func TestExamWebhook_UnknownForm_404(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	body := fmt.Sprintf(`{"event_id":%q,"idempotency_key":%q,"tenant_id":%q,"exam_id":%q,"exam_form_id":%q,"candidate_ref":%q,"raw_score":10}`,
		whEventID, whIdemKey, whTenant, whExamID, "019e2f93-d586-71b5-8c3d-e2b0d0d5dead", whCandRef)
	rec := postSigned(t, h, body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestExamWebhook_CrossTenantForm_404(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	otherTenant := "019e2f93-d586-71b5-8c3d-e2b0d0d5bb02"
	body := fmt.Sprintf(`{"event_id":%q,"idempotency_key":%q,"tenant_id":%q,"exam_id":%q,"exam_form_id":%q,"candidate_ref":%q,"raw_score":10}`,
		whEventID, whIdemKey, otherTenant, whExamID, h.formID, whCandRef)
	rec := postSigned(t, h, body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (cross-tenant probe must read as not-found) body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Our-fault 5xx
// -----------------------------------------------------------------------------

func TestExamWebhook_StoreFailure_500_MarkedFailed(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	h.results.saveErr = errors.New("pg down")
	rec := postSigned(t, h, h.body(whEventID, whIdemKey, 72))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
	if len(h.events.failures) == 0 {
		t.Fatal("MarkFailed must record the store failure")
	}
	ev, ok, _ := h.events.GetByIdempotencyKey(context.Background(), whIdemKey)
	if !ok || ev.IsProcessed() {
		t.Fatalf("receipt must remain unprocessed for the retry: %+v ok=%v", ev, ok)
	}
}

func TestExamWebhook_MissingSecret_503(t *testing.T) {
	h := newWebhookHarness(t, "")
	body := h.body(whEventID, whIdemKey, 72)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	rec := postWebhook(t, h.srv, ts, signature(whSecret, ts, body), body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}

func TestExamWebhook_UnwiredDeps_503(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.RegisterExamWebhookRoutes(mux, httpapi.ExamWebhookDeps{Secret: whSecret})
	body := `{}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	rec := postWebhook(t, mux, ts, signature(whSecret, ts, body), body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}

func TestExamWebhook_MethodNotAllowed(t *testing.T) {
	h := newWebhookHarness(t, whSecret)
	req := httptest.NewRequest(http.MethodGet, httpapi.ExamWebhookPath, nil)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}
