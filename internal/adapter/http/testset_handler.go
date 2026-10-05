// testset_handler.go — A+ X.2 test-set authoring API (Lane A scope).
//
// Endpoints (per chora-contracts/openapi/delivery-test-sets.yaml):
//
//	POST   /api/v1/test-sets                                       createTestSet
//	GET    /api/v1/test-sets/{test_set_id}                         getTestSet
//	POST   /api/v1/test-sets/{test_set_id}/questions               addQuestionToTestSet
//	PUT    /api/v1/test-sets/{test_set_id}/questions/{qId}         updateTestSetQuestion
//	DELETE /api/v1/test-sets/{test_set_id}/questions/{qId}         removeTestSetQuestion
//	POST   /api/v1/test-sets/{test_set_id}/publish                 publishTestSet
//
// Authorization (verified inside the handler, NOT at middleware):
//
//   - tenantRequired enforces X-Tenant-Id presence (400 if missing).
//   - gcid header is required (401 if missing) — Bucket 4 servicemesh
//     propagation guarantees this when the caller's JWT is valid.
//   - Caller is authorised when ANY of the following hold:
//     a. caller_gcid == test_set.author_gcid (the author themselves), OR
//     b. caller carries the `instructor` or `admin` typed role in
//     `x-mesh-user-roles` (Bucket 4 servicemesh header).
//
// RLS: every persistence call runs through the TestSetRepo, which applies
// rls.ApplySession with the X-Tenant-Id header before any user query. The
// tenant policy on `test_sets` enforces cross-tenant isolation; the policy
// on `test_set_questions` chains through the FK to the parent.
//
// Events: every state-mutating operation emits a chora.delivery.test_set.*
// event via the outbox-backed publisher. Topic taxonomy per ADR-152 +
// chora-contracts/proto/events/delivery/test_set.proto.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// TestSetPort is the persistence contract the HTTP handlers depend on. Two
// adapters satisfy it:
//   - pg.TestSetRepo — production, Postgres-backed via chora_delivery.
//   - InMemTestSetStore — local dev + tests (this file).
//
// All methods take a context.Context so the pg adapter can apply RLS.
type TestSetPort interface {
	Save(ctx context.Context, ts *domain.TestSet) error
	SaveQuestionRemoval(ctx context.Context, ts *domain.TestSet, questionID string) error
	Get(ctx context.Context, tenantID, id string) (*domain.TestSet, bool, error)
	// GetBySourceJobID returns the (at most one — UNIQUE partial index,
	// migration 0028) test set assembled from the supplied chora-creation
	// batch job. ok=false when absent. Lane 1c (CHO-1703 / ADR-180 D10):
	// the question_batch.accepted.v1 subscriber's idempotency check.
	GetBySourceJobID(ctx context.Context, tenantID, sourceJobID string) (*domain.TestSet, bool, error)
	// List returns tenant-scoped test-set headers (no question children) ordered
	// by `created_at DESC, test_set_id DESC`. Filter semantics per
	// chora-contracts/openapi/delivery-test-sets.yaml#listTestSets.
	//
	//   - states: empty ⇒ default {DRAFT, PUBLISHED} (i.e. exclude ARCHIVED).
	//     Non-empty ⇒ OR-filter on the supplied state values.
	//   - authorGCIDs: empty ⇒ no author filter; non-empty ⇒ OR-filter.
	//   - titleQuery: empty ⇒ no needle; non-empty ⇒ case-insensitive substring.
	//   - sourceJobID: empty ⇒ no filter; non-empty ⇒ exact match (Lane 1c).
	//   - pageSize: capped to {10,20,50,100}; default 20 when 0.
	//   - pageToken: opaque; empty ⇒ first page.
	//
	// Returns (items, nextPageToken, error). nextPageToken == "" when no
	// more pages.
	List(ctx context.Context, tenantID string, filter domain.TestSetListFilter) ([]*domain.TestSet, string, error)
}

// InMemTestSetStore is the in-memory adapter satisfying TestSetPort. Used
// by local-dev wiring + unit tests when no Postgres pool is configured.
// Production wires pg.TestSetRepo instead.
type InMemTestSetStore struct {
	rows map[string]*domain.TestSet // tenantID|id → aggregate
}

// NewInMemTestSetStore returns an empty in-memory store.
func NewInMemTestSetStore() *InMemTestSetStore {
	return &InMemTestSetStore{rows: map[string]*domain.TestSet{}}
}

// inMemKey scopes by tenant so concurrent local-dev shards do not collide.
func inMemKey(tenantID, id string) string { return tenantID + "|" + id }

// Save persists the aggregate verbatim (no FK enforcement).
func (s *InMemTestSetStore) Save(_ context.Context, ts *domain.TestSet) error {
	if ts == nil {
		return errors.New("nil test_set")
	}
	s.rows[inMemKey(ts.TenantID, ts.ID)] = ts
	return nil
}

// SaveQuestionRemoval is a no-op for the in-memory store — the aggregate's
// RemoveQuestion() already mutated the in-memory state; the next Save()
// call will overwrite the row with the updated child slice.
func (s *InMemTestSetStore) SaveQuestionRemoval(_ context.Context, ts *domain.TestSet, _ string) error {
	if ts == nil {
		return errors.New("nil test_set")
	}
	s.rows[inMemKey(ts.TenantID, ts.ID)] = ts
	return nil
}

// Get returns the test_set by (tenant, id). ok=false when absent.
func (s *InMemTestSetStore) Get(_ context.Context, tenantID, id string) (*domain.TestSet, bool, error) {
	ts, ok := s.rows[inMemKey(tenantID, id)]
	return ts, ok, nil
}

// GetBySourceJobID linear-scans the tenant's rows for the batch-job linkage.
// Mirrors pg.TestSetRepo.GetBySourceJobID (header semantics; the in-memory
// aggregate carries its children anyway).
func (s *InMemTestSetStore) GetBySourceJobID(_ context.Context, tenantID, sourceJobID string) (*domain.TestSet, bool, error) {
	if strings.TrimSpace(sourceJobID) == "" {
		return nil, false, nil
	}
	for _, ts := range s.rows {
		if ts == nil || ts.TenantID != tenantID || ts.DeletedAt != nil {
			continue
		}
		if ts.SourceJobID != nil && *ts.SourceJobID == sourceJobID {
			return ts, true, nil
		}
	}
	return nil, false, nil
}

// List returns tenant-scoped test-set headers, applying the OR-within-family
// AND-across-family filter semantics per the OpenAPI contract.
//
// For the in-memory adapter, pagination is "all or one page" — page_token is
// accepted but not honoured (returns nextPageToken="" always). Production
// pagination lives in pg.TestSetRepo.
func (s *InMemTestSetStore) List(_ context.Context, tenantID string, filter domain.TestSetListFilter) ([]*domain.TestSet, string, error) {
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	stateSet := map[domain.TestSetState]bool{}
	if len(filter.States) == 0 {
		stateSet[domain.TestSetStateDraft] = true
		stateSet[domain.TestSetStatePublished] = true
	} else {
		for _, st := range filter.States {
			stateSet[st] = true
		}
	}
	authorSet := map[string]bool{}
	for _, g := range filter.AuthorGCIDs {
		authorSet[g] = true
	}
	needle := strings.ToLower(strings.TrimSpace(filter.TitleQuery))
	sourceJob := strings.TrimSpace(filter.SourceJobID)

	out := make([]*domain.TestSet, 0, len(s.rows))
	for _, ts := range s.rows {
		if ts == nil || ts.TenantID != tenantID {
			continue
		}
		if ts.DeletedAt != nil {
			continue
		}
		if !stateSet[ts.State] {
			continue
		}
		if len(authorSet) > 0 && !authorSet[ts.AuthorGCID] {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(ts.Title), needle) {
			continue
		}
		// Lane 1c — exact-match on the batch-job linkage when supplied.
		if sourceJob != "" && (ts.SourceJobID == nil || *ts.SourceJobID != sourceJob) {
			continue
		}
		out = append(out, ts)
	}
	// Stable ordering — created_at DESC, then ID DESC tie-break.
	sortTestSetsCreatedAtDesc(out)
	if len(out) > pageSize {
		out = out[:pageSize]
	}
	return out, "", nil
}

// -----------------------------------------------------------------------------
// Routing
// -----------------------------------------------------------------------------

// testSetsRootHandler dispatches GET (list) + POST (create) on /api/v1/test-sets.
func testSetsRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListTestSets(deps, w, r)
		case http.MethodPost:
			handleCreateTestSet(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// testSetsSubHandler dispatches:
//
//	GET    /api/v1/test-sets/{id}
//	POST   /api/v1/test-sets/{id}/publish
//	POST   /api/v1/test-sets/{id}/questions
//	PUT    /api/v1/test-sets/{id}/questions/{qId}
//	DELETE /api/v1/test-sets/{id}/questions/{qId}
func testSetsSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/test-sets/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		testSetID := parts[0]

		// /api/v1/test-sets/{id}/publish
		if len(parts) == 2 && parts[1] == "publish" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handlePublishTestSet(deps, testSetID, w, r)
			return
		}

		// /api/v1/test-sets/{id}/questions[/{qId}]
		if len(parts) >= 2 && parts[1] == "questions" {
			if len(parts) == 2 {
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleAddTestSetQuestion(deps, testSetID, w, r)
				return
			}
			if len(parts) == 3 {
				qID := parts[2]
				switch r.Method {
				case http.MethodPut, http.MethodPatch:
					handleUpdateTestSetQuestion(deps, testSetID, qID, w, r)
				case http.MethodDelete:
					handleRemoveTestSetQuestion(deps, testSetID, qID, w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
		}

		// /api/v1/test-sets/{id}
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				handleGetTestSet(deps, testSetID, w, r)
			case http.MethodPatch:
				handleUpdateTestSet(deps, testSetID, w, r)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	}
}

// -----------------------------------------------------------------------------
// Request shapes
// -----------------------------------------------------------------------------

type createTestSetReq struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	// TenantID is intentionally NOT consumed from the body — the caller's
	// X-Tenant-Id header is the source-of-truth (mesh-trusted).
}

type addTestSetQuestionReq struct {
	QuestionAtomID string `json:"question_atom_id"`
	// QuestionID — LEG3-D R3 Option B (per docs/m13/wave3-leg3d-round3-
	// blocker-2026-05-16.md). UUIDv7 of the embedded Question inside the
	// atom's payload (e.g., mcq_payload.question_id). REQUIRED — the
	// handler rejects 400 when blank so the LEG3-D publish-time 502 (atom
	// id passed where chora-creation expected the embedded question_id)
	// can never recur. FE picker extracts it from the loaded atom.
	QuestionID   string  `json:"question_id"`
	QuestionType string  `json:"question_type"`
	DisplayOrder int     `json:"display_order"`
	Points       float64 `json:"points"`
}

type updateTestSetQuestionReq struct {
	Points       *float64 `json:"points"`
	DisplayOrder *int     `json:"display_order"`
}

// updateTestSetReq is the wire shape for PATCH /api/v1/test-sets/{id} —
// mirrors UpdateTestSetRequest in
// chora-contracts/openapi/delivery-test-sets.yaml. Pointer fields are
// optional so the FE can rename in isolation (most common case — title-
// only PATCH on the test-set author page). DRAFT-only per the contract;
// PUBLISHED returns 409.
type updateTestSetReq struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

func handleCreateTestSet(deps Deps, w http.ResponseWriter, r *http.Request) {
	gcid := strings.TrimSpace(r.Header.Get("gcid"))
	if gcid == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required (caller identity)")
		return
	}
	if !callerHoldsAuthoringRole(r) {
		writeError(w, http.StatusForbidden,
			"caller lacks an authoring role (instructor, admin, author, training_admin, tenant_admin, owner)")
		return
	}
	var req createTestSetReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:    r.Header.Get("X-Tenant-Id"),
		AuthorGCID:  gcid,
		Title:       req.Title,
		Description: req.Description,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deps.TestSets.Save(r.Context(), ts); err != nil {
		writeError(w, http.StatusInternalServerError, "test-set save failed: "+err.Error())
		return
	}
	publishTestSetCreated(deps, ts, r)
	writeJSON(w, http.StatusCreated, testSetDTO(ts))
}

func handleGetTestSet(deps Deps, testSetID string, w http.ResponseWriter, r *http.Request) {
	ts, ok, err := deps.TestSets.Get(r.Context(), r.Header.Get("X-Tenant-Id"), testSetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "test-set read failed: "+err.Error())
		return
	}
	if !ok || ts == nil {
		writeError(w, http.StatusNotFound, "test-set not found")
		return
	}
	writeJSON(w, http.StatusOK, testSetWithQuestionsDTO(ts))
}

// handleUpdateTestSet — PATCH /api/v1/test-sets/{id}. Partial update on
// title + description. Closes E2E-BE-TESTSET-PATCH-TITLE-504: the
// handler was missing (PATCH fell through to "method not allowed").
//
// State guard enforced at the domain (UpdateMetadata returns
// ErrTestSetPublishedImmutable / ErrTestSetArchived); HTTP layer maps to
// 409 via mapTestSetMutationErr.
func handleUpdateTestSet(deps Deps, testSetID string, w http.ResponseWriter, r *http.Request) {
	ts, ok, err := deps.TestSets.Get(r.Context(), r.Header.Get("X-Tenant-Id"), testSetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "test-set read failed: "+err.Error())
		return
	}
	if !ok || ts == nil {
		writeErrorCode(w, http.StatusNotFound, "DELIVERY_TEST_SET_NOT_FOUND",
			"test-set not found")
		return
	}
	if !authorisedForTestSet(r, ts) {
		writeError(w, http.StatusForbidden,
			"caller is not the author and lacks an authoring role")
		return
	}
	var req updateTestSetReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ts.UpdateMetadata(domain.UpdateTestSetMetadataInput{
		Title:       req.Title,
		Description: req.Description,
	}); err != nil {
		mapTestSetMutationErr(w, err)
		return
	}
	if err := deps.TestSets.Save(r.Context(), ts); err != nil {
		writeError(w, http.StatusInternalServerError, "test-set save failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, testSetDTO(ts))
}

func handleAddTestSetQuestion(deps Deps, testSetID string, w http.ResponseWriter, r *http.Request) {
	ts, ok, err := deps.TestSets.Get(r.Context(), r.Header.Get("X-Tenant-Id"), testSetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "test-set read failed: "+err.Error())
		return
	}
	if !ok || ts == nil {
		writeErrorCode(w, http.StatusNotFound, "DELIVERY_TEST_SET_NOT_FOUND",
			"test-set not found")
		return
	}
	if !authorisedForTestSet(r, ts) {
		writeError(w, http.StatusForbidden, "caller is not the author and lacks an authoring role")
		return
	}
	var req addTestSetQuestionReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// LEG3-D R3 Option B — explicit handler-level 400 on missing
	// question_id so the picker contract is checked at the API boundary
	// (in addition to the domain guard).
	if strings.TrimSpace(req.QuestionID) == "" {
		writeError(w, http.StatusBadRequest,
			"question_id required (embedded UUID inside the atom payload, e.g. mcq_payload.question_id)")
		return
	}
	q, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: req.QuestionAtomID,
		QuestionID:     req.QuestionID,
		QuestionType:   req.QuestionType,
		DisplayOrder:   req.DisplayOrder,
		Points:         req.Points,
	})
	if err != nil {
		mapTestSetMutationErr(w, err)
		return
	}
	if err := deps.TestSets.Save(r.Context(), ts); err != nil {
		writeError(w, http.StatusInternalServerError, "test-set save failed: "+err.Error())
		return
	}
	publishQuestionAdded(deps, ts, q, r)
	writeJSON(w, http.StatusCreated, testSetQuestionDTO(q))
}

func handleUpdateTestSetQuestion(deps Deps, testSetID, qID string, w http.ResponseWriter, r *http.Request) {
	ts, ok, err := deps.TestSets.Get(r.Context(), r.Header.Get("X-Tenant-Id"), testSetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "test-set read failed: "+err.Error())
		return
	}
	if !ok || ts == nil {
		writeErrorCode(w, http.StatusNotFound, "DELIVERY_TEST_SET_NOT_FOUND",
			"test-set not found")
		return
	}
	if !authorisedForTestSet(r, ts) {
		writeError(w, http.StatusForbidden, "caller is not the author and lacks an authoring role")
		return
	}
	var req updateTestSetQuestionReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q, err := ts.UpdateQuestion(qID, domain.UpdateQuestionInput{
		Points:       req.Points,
		DisplayOrder: req.DisplayOrder,
	})
	if err != nil {
		mapTestSetMutationErr(w, err)
		return
	}
	if err := deps.TestSets.Save(r.Context(), ts); err != nil {
		writeError(w, http.StatusInternalServerError, "test-set save failed: "+err.Error())
		return
	}
	publishQuestionUpdated(deps, ts, q, r)
	writeJSON(w, http.StatusOK, testSetQuestionDTO(q))
}

func handleRemoveTestSetQuestion(deps Deps, testSetID, qID string, w http.ResponseWriter, r *http.Request) {
	ts, ok, err := deps.TestSets.Get(r.Context(), r.Header.Get("X-Tenant-Id"), testSetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "test-set read failed: "+err.Error())
		return
	}
	if !ok || ts == nil {
		writeErrorCode(w, http.StatusNotFound, "DELIVERY_TEST_SET_NOT_FOUND",
			"test-set not found")
		return
	}
	if !authorisedForTestSet(r, ts) {
		writeError(w, http.StatusForbidden, "caller is not the author and lacks an authoring role")
		return
	}
	if err := ts.RemoveQuestion(qID); err != nil {
		mapTestSetMutationErr(w, err)
		return
	}
	if err := deps.TestSets.SaveQuestionRemoval(r.Context(), ts, qID); err != nil {
		writeError(w, http.StatusInternalServerError, "test-set save failed: "+err.Error())
		return
	}
	publishQuestionRemoved(deps, ts, qID, r)
	w.WriteHeader(http.StatusNoContent)
}

func handlePublishTestSet(deps Deps, testSetID string, w http.ResponseWriter, r *http.Request) {
	ts, ok, err := deps.TestSets.Get(r.Context(), r.Header.Get("X-Tenant-Id"), testSetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "test-set read failed: "+err.Error())
		return
	}
	if !ok || ts == nil {
		writeErrorCode(w, http.StatusNotFound, "DELIVERY_TEST_SET_NOT_FOUND",
			"test-set not found")
		return
	}
	if !authorisedForTestSet(r, ts) {
		writeError(w, http.StatusForbidden, "caller is not the author and lacks an authoring role")
		return
	}
	wasPublished := ts.State == domain.TestSetStatePublished
	// Fix-F: PublishWithSnapshot atomically snapshots every MCQ+OE payload
	// from chora_creation via gRPC BEFORE the DRAFT→PUBLISHED flip. Fail-
	// loud per `feedback_no_stubs_real_wiring`. When deps.QuestionSnapshotter
	// is nil (dev / in-mem), this is equivalent to Publish() (no snapshots).
	// ADR-229 WS-2 (CHO-2133): thread the publish actor (gateway-injected
	// `gcid` header — the same identity authorisedForTestSet trusts) so
	// chora-creation's snapshot gate evaluates the consent predicate for
	// the REAL caller and writes the D2 audit grant.
	if err := ts.PublishWithSnapshot(r.Context(), deps.QuestionSnapshotter, strings.TrimSpace(r.Header.Get("gcid"))); err != nil {
		mapPublishErr(w, err)
		return
	}
	if err := deps.TestSets.Save(r.Context(), ts); err != nil {
		writeError(w, http.StatusInternalServerError, "test-set save failed: "+err.Error())
		return
	}
	// Idempotent re-publish: do NOT re-emit the event (the previous publish
	// already minted one with the same idempotency key, but consumers may
	// double-count if we replay here).
	if !wasPublished {
		publishTestSetPublished(deps, ts, r)
	}
	writeJSON(w, http.StatusOK, testSetDTO(ts))
}

// handleListTestSets serves GET /api/v1/test-sets.
//
// Authorization: instructor or admin role required. Tenant scope is implicit
// via the X-Tenant-Id header → port-level RLS.
//
// Filter parsing per chora-contracts/openapi/delivery-test-sets.yaml:
//
//   - state[]        — default {DRAFT, PUBLISHED}; ARCHIVED opt-in.
//   - author_gcid[]  — optional.
//   - q              — case-insensitive substring on title.
//   - page_size      — enum {10,20,50,100}; default 20; values outside
//     the enum are coerced to 20.
//   - page_token     — opaque cursor; passed through to the port.
//
// Sort param is accepted but ignored in v1 (port pins to created_at DESC,
// id DESC tie-break). include_total / sort honouring are forward work.
func handleListTestSets(deps Deps, w http.ResponseWriter, r *http.Request) {
	gcid := strings.TrimSpace(r.Header.Get("gcid"))
	if gcid == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required (caller identity)")
		return
	}
	if !callerHoldsAuthoringRole(r) {
		writeError(w, http.StatusForbidden,
			"caller lacks an authoring role required to list test-sets")
		return
	}
	q := r.URL.Query()
	filter := domain.TestSetListFilter{
		PageSize:   parseTestSetPageSize(q.Get("page_size")),
		PageToken:  q.Get("page_token"),
		TitleQuery: q.Get("q"),
	}
	// Lane 1c (CHO-1703 / ADR-180): exact-match discovery filter for the
	// batch-authoring FE post-accept poll. Contract format is uuid — reject
	// malformed values with 400 rather than letting the pg `::uuid` cast
	// surface a 22P02 as a 500.
	if raw := strings.TrimSpace(q.Get("source_job_id")); raw != "" {
		if _, err := uuid.Parse(raw); err != nil {
			writeError(w, http.StatusBadRequest, "source_job_id must be a UUID")
			return
		}
		filter.SourceJobID = raw
	}
	for _, raw := range q["state"] {
		st := domain.TestSetState(strings.ToUpper(strings.TrimSpace(raw)))
		switch st {
		case domain.TestSetStateDraft, domain.TestSetStatePublished, domain.TestSetStateArchived:
			filter.States = append(filter.States, st)
		}
	}
	for _, raw := range q["author_gcid"] {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			filter.AuthorGCIDs = append(filter.AuthorGCIDs, raw)
		}
	}
	tenantID := r.Header.Get("X-Tenant-Id")
	items, nextToken, err := deps.TestSets.List(r.Context(), tenantID, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "test-set list failed: "+err.Error())
		return
	}
	dtos := make([]map[string]interface{}, 0, len(items))
	for _, ts := range items {
		dtos = append(dtos, testSetDTO(ts))
	}
	out := map[string]interface{}{
		"items":           dtos,
		"next_page_token": nullableToken(nextToken),
	}
	writeJSON(w, http.StatusOK, out)
}

// parseTestSetPageSize maps the raw query value to one of {10,20,50,100}.
// Empty / non-numeric / out-of-enum → 20 (the contract default).
func parseTestSetPageSize(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 20
	}
	switch raw {
	case "10", "20", "50", "100":
		// no-op
	default:
		return 20
	}
	// Re-parse via strconv to avoid string-bool sentinels.
	switch raw {
	case "10":
		return 10
	case "20":
		return 20
	case "50":
		return 50
	case "100":
		return 100
	}
	return 20
}

// sortTestSetsCreatedAtDesc stable-sorts by created_at DESC then ID DESC.
// Lives here so InMemTestSetStore.List has a deterministic ordering matching
// the pg-side ORDER BY clause.
func sortTestSetsCreatedAtDesc(items []*domain.TestSet) {
	sortSlice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
}

// sortSlice is a thin shim over sort.Slice so test files needn't import sort.
func sortSlice(items []*domain.TestSet, less func(i, j int) bool) {
	sortableSlice(items).sortBy(less)
}

type sortableSlice []*domain.TestSet

func (s sortableSlice) sortBy(less func(i, j int) bool) {
	// Simple insertion sort — n is tiny in the in-memory adapter; production
	// uses pg ORDER BY.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(j, j-1); j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// -----------------------------------------------------------------------------
// Authorization helpers
// -----------------------------------------------------------------------------

// callerHoldsAuthoringRole reports whether the caller's x-mesh-user-roles
// header carries a role that the FE grants assessment:author to (per
// role-capabilities.ts): instructor, admin, tenant_admin, owner,
// author, training_admin (and the training-admin hyphen variant).
// This matches hasInstructorRole (assessment_handler.go) and
// hasClassroomInstructorRole (classroom_session_handler.go) — the
// previous 2-role gate (instructor+admin only) was an outlier that
// rejected `author` and `training_admin` users who the FE correctly
// lets through to the R+ test-set page.
func callerHoldsAuthoringRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleInstructor, roleAdmin, roleTrainingAdmin,
		roleTenantAdmin, roleAuthor, roleOwner)
}

// authorisedForTestSet reports whether the caller may mutate the test-set.
//   - Self-author: caller_gcid == test_set.author_gcid → ALWAYS allowed.
//   - Role-based: caller holds an authoring role (instructor, admin,
//     tenant_admin, owner, author, training_admin) per callerHoldsAuthoringRole.
func authorisedForTestSet(r *http.Request, ts *domain.TestSet) bool {
	caller := strings.TrimSpace(r.Header.Get("gcid"))
	if caller == "" {
		return false
	}
	if caller == ts.AuthorGCID {
		return true
	}
	return callerHoldsAuthoringRole(r)
}

// -----------------------------------------------------------------------------
// Error mapping (OpenAPI envelope shape per delivery-test-sets.yaml)
// -----------------------------------------------------------------------------

func mapTestSetMutationErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrTestSetPublishedImmutable):
		writeErrorCode(w, http.StatusConflict,
			"DELIVERY_TEST_SET_PUBLISHED_IMMUTABLE", err.Error())
	case errors.Is(err, domain.ErrTestSetArchived):
		writeErrorCode(w, http.StatusConflict,
			"DELIVERY_TEST_SET_ARCHIVED", err.Error())
	case errors.Is(err, domain.ErrTestSetQuestionNotFound):
		writeErrorCode(w, http.StatusNotFound,
			"DELIVERY_TEST_SET_QUESTION_NOT_FOUND", err.Error())
	case errors.Is(err, domain.ErrInvalidArgument):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func mapPublishErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrTestSetNoQuestions):
		writeErrorCode(w, http.StatusConflict,
			"DELIVERY_TEST_SET_NO_QUESTIONS", err.Error())
	case errors.Is(err, domain.ErrTestSetArchived):
		writeErrorCode(w, http.StatusConflict,
			"DELIVERY_TEST_SET_ARCHIVED", err.Error())
	case errors.Is(err, domain.ErrQuestionSnapshotNotFound):
		// Fix-F: snapshotter could not resolve one of the questions in
		// chora_creation (RLS-scoped lookup missed). Caller fixed by
		// re-authoring the question in chora-creation or removing it
		// from the test-set.
		writeErrorCode(w, http.StatusConflict,
			"DELIVERY_TEST_SET_QUESTION_SNAPSHOT_NOT_FOUND", err.Error())
	case errors.Is(err, domain.ErrQuestionReuseDenied):
		// ADR-229 WS-2 (CHO-2133 / CHO-2139): the atom's author narrowed its
		// reuse visibility and the publish actor is not entitled. TERMINAL
		// 403 — NOT a retryable 502. The discriminated upstream message
		// (`ADR229_REUSE_DENIED …`) is preserved in err.Error() for the FE.
		writeErrorCode(w, http.StatusForbidden,
			"DELIVERY_TEST_SET_QUESTION_REUSE_DENIED", err.Error())
	case errors.Is(err, domain.ErrQuestionSnapshotInvalidArgument):
		// chora-creation rejected the snapshot request as malformed — a
		// terminal client error (HTTP 400), not an upstream outage.
		writeErrorCode(w, http.StatusBadRequest,
			"DELIVERY_TEST_SET_QUESTION_SNAPSHOT_INVALID", err.Error())
	case errors.Is(err, domain.ErrQuestionSnapshotPreconditionFailed):
		// Upstream precondition unmet (e.g. a dependency unwired). Terminal
		// for this attempt (HTTP 409); the test-set stays DRAFT.
		writeErrorCode(w, http.StatusConflict,
			"DELIVERY_TEST_SET_QUESTION_SNAPSHOT_PRECONDITION_FAILED", err.Error())
	default:
		// Fix-F: only genuine gRPC transport / 5xx failures (codes.Unavailable
		// / codes.Internal / non-status) reach here as 502 so the caller can
		// retry. State stays in DRAFT (PublishWithSnapshot rolls back).
		// Terminal consent/argument refusals are mapped to 4xx above.
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

// writeErrorCode emits the discriminated error envelope per OpenAPI.
func writeErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": msg,
		},
	})
}

// -----------------------------------------------------------------------------
// DTOs (JSON shape per OpenAPI)
// -----------------------------------------------------------------------------

func testSetDTO(ts *domain.TestSet) map[string]interface{} {
	// Lane 1c — source_job_id is nullable per delivery-test-sets.yaml
	// v1.1.0: the key is ALWAYS present (explicit null for hand-authored
	// rows) so the FE discovery poll can distinguish "not assembled" from
	// "field missing".
	var sourceJobID interface{}
	if ts.SourceJobID != nil {
		sourceJobID = *ts.SourceJobID
	}
	out := map[string]interface{}{
		"id":             ts.ID,
		"test_set_id":    ts.ID, // OpenAPI uses test_set_id; FE may use id
		"tenant_id":      ts.TenantID,
		"author_gcid":    ts.AuthorGCID,
		"title":          ts.Title,
		"description":    ts.Description,
		"state":          string(ts.State),
		"question_count": ts.QuestionCount(),
		"total_points":   ts.TotalPoints(),
		"source_job_id":  sourceJobID,
		"created_at":     ts.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":     ts.UpdatedAt.Format(time.RFC3339Nano),
	}
	if ts.PublishedAt != nil {
		out["published_at"] = ts.PublishedAt.Format(time.RFC3339Nano)
	}
	if ts.DeletedAt != nil {
		out["deleted_at"] = ts.DeletedAt.Format(time.RFC3339Nano)
	}
	return out
}

func testSetWithQuestionsDTO(ts *domain.TestSet) map[string]interface{} {
	out := testSetDTO(ts)
	qs := ts.Questions()
	dtos := make([]map[string]interface{}, 0, len(qs))
	for _, q := range qs {
		dtos = append(dtos, testSetQuestionDTO(q))
	}
	out["questions"] = dtos
	return out
}

func testSetQuestionDTO(q *domain.TestSetQuestion) map[string]interface{} {
	// LEG3-D R3 Option B — question_atom_id + question_id are now DISTINCT
	// values per the request schema. Pre-Option-B both echoed the atom_id
	// (legacy conflated model).
	out := map[string]interface{}{
		"id":                   q.ID,
		"test_set_question_id": q.ID, // OpenAPI name
		"test_set_id":          q.TestSetID,
		"question_atom_id":     q.QuestionAtomID,
		"question_id":          q.QuestionID,
		"question_type":        q.QuestionType,
		"display_order":        q.DisplayOrder,
		"points":               q.Points,
		"added_at":             q.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":           q.UpdatedAt.Format(time.RFC3339Nano),
	}

	// CHO-2402 — the copy pinned at publish belongs on the wire.
	//
	// PublishWithSnapshot writes test_set_questions.payload_snapshot and the
	// repo selects it back onto the aggregate, but this DTO used to drop it.
	// A PUBLISHED set is served from that copy, so an author surface with no
	// access to it can only render the LIVE question, which is not what the
	// learners sitting the set see. DRAFT rows carry no snapshot and follow
	// the live question by design, so both keys stay absent there.
	if snap := strings.TrimSpace(q.PayloadSnapshot); snap != "" {
		if json.Valid([]byte(snap)) {
			out["payload_snapshot"] = json.RawMessage(snap)
			if q.SnapshotAt != nil {
				out["snapshot_at"] = q.SnapshotAt.Format(time.RFC3339Nano)
			}
		} else {
			// Fail loud rather than serve a malformed copy as if it were
			// sound: name the fault on the wire AND in the log, and leave
			// payload_snapshot absent so no consumer renders it as pinned.
			log.Printf("service=chora-delivery event=payload_snapshot_invalid test_set_question_id=%s bytes=%d",
				q.ID, len(snap))
			out["payload_snapshot_error"] = "stored snapshot is not valid JSON"
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// Event publish helpers
// -----------------------------------------------------------------------------

func publishTestSetCreated(deps Deps, ts *domain.TestSet, r *http.Request) {
	if deps.Publisher == nil {
		return
	}
	_, _ = deps.Publisher.PublishTestSetCreated(events.TestSetCreated{
		TenantID:    ts.TenantID,
		GCID:        ts.AuthorGCID,
		TestSetID:   ts.ID,
		AuthorGCID:  ts.AuthorGCID,
		Title:       ts.Title,
		Traceparent: r.Header.Get("traceparent"),
	})
}

func publishQuestionAdded(deps Deps, ts *domain.TestSet, q *domain.TestSetQuestion, r *http.Request) {
	if deps.Publisher == nil {
		return
	}
	_, _ = deps.Publisher.PublishTestSetQuestionAdded(events.TestSetQuestionAdded{
		TenantID:          ts.TenantID,
		GCID:              ts.AuthorGCID,
		TestSetID:         ts.ID,
		TestSetQuestionID: q.ID,
		QuestionAtomID:    q.QuestionAtomID,
		QuestionType:      q.QuestionType,
		DisplayOrder:      int32(q.DisplayOrder),
		Points:            q.Points,
		Traceparent:       r.Header.Get("traceparent"),
	})
}

func publishQuestionUpdated(deps Deps, ts *domain.TestSet, q *domain.TestSetQuestion, r *http.Request) {
	if deps.Publisher == nil {
		return
	}
	_, _ = deps.Publisher.PublishTestSetQuestionUpdated(events.TestSetQuestionUpdated{
		TenantID:          ts.TenantID,
		GCID:              ts.AuthorGCID,
		TestSetID:         ts.ID,
		TestSetQuestionID: q.ID,
		DisplayOrder:      int32(q.DisplayOrder),
		Points:            q.Points,
		Traceparent:       r.Header.Get("traceparent"),
	})
}

func publishQuestionRemoved(deps Deps, ts *domain.TestSet, qID string, r *http.Request) {
	if deps.Publisher == nil {
		return
	}
	_, _ = deps.Publisher.PublishTestSetQuestionRemoved(events.TestSetQuestionRemoved{
		TenantID:          ts.TenantID,
		GCID:              ts.AuthorGCID,
		TestSetID:         ts.ID,
		TestSetQuestionID: qID,
		Traceparent:       r.Header.Get("traceparent"),
	})
}

func publishTestSetPublished(deps Deps, ts *domain.TestSet, r *http.Request) {
	if deps.Publisher == nil {
		return
	}
	_, _ = deps.Publisher.PublishTestSetPublished(events.TestSetPublished{
		TenantID:      ts.TenantID,
		GCID:          ts.AuthorGCID,
		TestSetID:     ts.ID,
		AuthorGCID:    ts.AuthorGCID,
		QuestionCount: int32(ts.QuestionCount()),
		TotalPoints:   ts.TotalPoints(),
		Traceparent:   r.Header.Get("traceparent"),
	})
}

// Compile-time port assertion.
var _ TestSetPort = (*InMemTestSetStore)(nil)

// _ keeps fmt referenced when only the compile-time port check is used.
var _ = fmt.Sprintf
