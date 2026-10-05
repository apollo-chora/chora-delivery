// testset.go — TestSet aggregate root for the Content Delivery domain.
//
// A TestSet is a curated, point-weighted collection of Questions (authored
// in chora-creation) that can be instantiated as a live Assessment for a
// Class/Booking. Per chora-contracts/openapi/delivery-test-sets.yaml +
// ADR-155 §D5 (append-only-on-publish; aggregate invariant #4 in
// .claude/rules/ddd-enforcement.md).
//
// Cross-domain composition (ADR-155 D1):
//   - Questions live in `chora_creation` (cross-DB queries forbidden).
//   - TestSet holds question_atom_id UUIDs without FK; question existence is
//     validated upstream (HTTP layer or gRPC at production deploy time).
//
// Append-only-on-publish (ADR-155 D5):
//   - DRAFT → PUBLISHED is one-way for the question list; subsequent
//     Add/Update/Remove on PUBLISHED return ErrTestSetPublishedImmutable.
//   - ARCHIVED is also frozen for state-transition purposes.
//
// State machine (Lane A scope — out-of-scope ARCHIVED transitions for Tier 2):
//
//	DRAFT     → PUBLISHED  (via Publish; requires ≥1 question)
//	DRAFT     → ARCHIVED   (via Archive)
//	PUBLISHED → ARCHIVED   (via Archive)
//
// Hexagonal: pure domain. NO infrastructure imports. The mutex is the only
// concurrency primitive (mirrors Class.BookingsCount in this package).
package delivery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// QuestionPayloadSnapshot — projected payload from chora_creation
// -----------------------------------------------------------------------------

// QuestionPayloadSnapshot is the projection captured at TestSet.Publish() time
// from chora_creation's Question aggregate via the
// chora.services.creation.v1.Creation/SnapshotQuestionByID gRPC. Stored on
// chora_delivery.test_set_questions.payload_snapshot (JSONB) by the pg
// adapter and re-read at grade time so chora-delivery NEVER cross-DB-queries
// chora_creation per ddd-enforcement #3.
type QuestionPayloadSnapshot struct {
	// QuestionType is one of "mcq" | "oe" — drives downstream interpretation.
	QuestionType string

	// PayloadJSON is the canonical MCQPayload OR OEPayload JSON-string per
	// chora-contracts/openapi/creation-questions.yaml. Opaque to chora-
	// delivery — chora-creation owns the shape.
	PayloadJSON string

	// CapturedAt is the authoring-side timestamp at the moment the source
	// row in chora_creation was last persisted (questions.updated_at).
	// chora-delivery stores it in test_set_questions.snapshot_at for audit.
	CapturedAt time.Time
}

// ErrQuestionSnapshotNotFound — sentinel returned by the QuestionSnapshotter
// port when the requested question_id has no live row in chora_creation
// (RLS-scoped). Maps to a fail-loud Publish failure (HTTP 409 / 502 depending
// on cause).
var ErrQuestionSnapshotNotFound = errors.New("delivery: question snapshot not found in chora_creation (rls-scoped); cannot publish test-set without canonical answer key")

// ErrQuestionReuseDenied — sentinel returned by the QuestionSnapshotter port
// when chora-creation's ADR-229 WS-2 (CHO-2133) reuse-consent gate REFUSES the
// publish actor (upstream gRPC codes.PermissionDenied; the discriminated
// message begins `ADR229_REUSE_DENIED`). The atom's author narrowed its reuse
// visibility and the caller is not entitled — a TERMINAL client-side refusal,
// NOT a retryable upstream outage. Maps to HTTP 403 (see mapPublishErr); the
// adapter preserves the discriminated upstream message ahead of the sentinel
// so the FE can surface the reason.
var ErrQuestionReuseDenied = errors.New("delivery: question reuse denied by chora-creation consent gate (ADR-229)")

// ErrQuestionSnapshotInvalidArgument — sentinel returned when chora-creation
// rejects the snapshot request as malformed (upstream gRPC
// codes.InvalidArgument, e.g. a non-UUID question_id). Terminal client error,
// not an upstream outage → HTTP 400.
var ErrQuestionSnapshotInvalidArgument = errors.New("delivery: question snapshot request rejected as invalid by chora-creation")

// ErrQuestionSnapshotPreconditionFailed — sentinel returned when chora-creation
// cannot service the snapshot because a precondition is unmet / a dependency is
// unwired (upstream gRPC codes.FailedPrecondition). Terminal for this attempt
// (the test-set stays DRAFT) and distinct from a transient transport outage
// (which stays 502) → HTTP 409.
var ErrQuestionSnapshotPreconditionFailed = errors.New("delivery: question snapshot precondition failed at chora-creation")

// QuestionSnapshotter is the port that fetches the canonical Question
// payload from chora_creation at TestSet.Publish() time. Production wires
// the gRPC client adapter (internal/adapter/clients/question_client.go);
// tests wire a stub.
//
// Per ddd-enforcement #3 this is the ONLY mechanism for cross-domain
// snapshot lookup — no cross-DB queries.
// callerGCID (ADR-229 WS-2, CHO-2133) is the publish actor on whose behalf
// the snapshot is taken — chora-creation's SnapshotQuestionByID gate
// (chokepoint 2) evaluates owner ∨ tenant-visible ∨ granted for it and
// writes the D2 audit grant on an allowed non-owner reuse. Empty = legacy
// caller (creation skips the predicate during the migration window).
type QuestionSnapshotter interface {
	// SnapshotQuestion fetches the canonical payload for the given
	// (tenant_id, question_id). Returns ErrQuestionSnapshotNotFound when
	// no row matches under RLS; arbitrary error otherwise (e.g., gRPC
	// transport failure — fail loud).
	SnapshotQuestion(ctx context.Context, tenantID, questionID, callerGCID string) (QuestionPayloadSnapshot, error)
}

// -----------------------------------------------------------------------------
// Sentinel errors
// -----------------------------------------------------------------------------

// ErrTestSetNoQuestions is returned by Publish when the test-set has zero
// non-deleted question inclusions. Maps to HTTP 409 with
// `code=DELIVERY_TEST_SET_NO_QUESTIONS` in the publishTestSet conflict
// envelope per delivery-test-sets.yaml.
var ErrTestSetNoQuestions = errors.New("test-set: cannot publish empty test-set")

// ErrTestSetArchived is returned by Publish when the test-set is in ARCHIVED.
// Maps to HTTP 409 with `code=DELIVERY_TEST_SET_ARCHIVED`.
var ErrTestSetArchived = errors.New("test-set: archived; cannot publish")

// ErrTestSetPublishedImmutable is returned by Add/Update/Remove Question
// when the test-set is PUBLISHED. Maps to HTTP 409 with
// `code=DELIVERY_TEST_SET_PUBLISHED_IMMUTABLE`.
var ErrTestSetPublishedImmutable = errors.New("test-set: published; question list is immutable")

// ErrTestSetQuestionNotFound is returned by Update/Remove when the
// referenced TestSetQuestion does not exist (or is already removed). Maps to
// HTTP 404.
var ErrTestSetQuestionNotFound = errors.New("test-set: question inclusion not found")

// -----------------------------------------------------------------------------
// State enum
// -----------------------------------------------------------------------------

// TestSetState is the lifecycle state of a TestSet aggregate.
//
// Values mirror the TestSetState enum in delivery-test-sets.yaml.
type TestSetState string

// TestSetState values.
const (
	TestSetStateDraft     TestSetState = "DRAFT"
	TestSetStatePublished TestSetState = "PUBLISHED"
	TestSetStateArchived  TestSetState = "ARCHIVED"
)

// TestSetListFilter is the cross-adapter filter shape for List queries.
//
// Defined in the domain so both the HTTP port (httpapi.TestSetPort) and the
// Postgres adapter (pg.TestSetRepo) can use the same struct without an
// import cycle. Semantics per
// chora-contracts/openapi/delivery-test-sets.yaml#listTestSets.
//
// Empty slices / zero values mean "no filter applied on this dimension"
// (the adapter applies the contract-default state-set when States is empty).
type TestSetListFilter struct {
	// States: tenant-default {DRAFT, PUBLISHED} when empty; ARCHIVED is opt-in.
	States []TestSetState
	// AuthorGCIDs: OR-within-family filter; empty ⇒ no author bound.
	AuthorGCIDs []string
	// TitleQuery: case-insensitive substring on title; empty ⇒ no needle.
	TitleQuery string
	// PageSize: caller intent; adapter clamps to the contract enum.
	PageSize int
	// PageToken: opaque cursor; empty ⇒ first page.
	PageToken string
	// SourceJobID: Lane 1c (CHO-1703 / ADR-180 D10) exact-match on the
	// chora-creation batch job that assembled the test set. "" ⇒ no filter.
	// At most one row matches (UNIQUE partial index, migration 0028).
	SourceJobID string
}

// -----------------------------------------------------------------------------
// QuestionType
// -----------------------------------------------------------------------------

// validQuestionTypes is the v1 phyllis-scope question type set per the
// `QuestionType` enum in chora-contracts/openapi/creation-questions.yaml. Only
// these two values are accepted at AddQuestion time; the 14 `reserved_*`
// sentinels are explicitly rejected (the test-set author cannot include a
// question type that downstream graders don't yet support).
var validQuestionTypes = map[string]bool{
	"mcq": true,
	"oe":  true,
}

// canonicalQuestionType normalizes an inbound question_type to the delivery
// domain canonical vocabulary. chora-creation's question taxonomy names the
// open-ended type "essay"; the delivery test-set / grading vocabulary is "oe".
// This is delivery's inbound anti-corruption layer — accept the creation alias
// but store the delivery canonical so the downstream submission + grading path
// (which keys on QuestionTypeOE) routes essay atoms to the OE grading crew.
// Unknown types pass through unchanged so the validity check still rejects them.
func canonicalQuestionType(qtype string) string {
	if strings.EqualFold(qtype, "essay") {
		return string(QuestionTypeOE)
	}
	return qtype
}

// -----------------------------------------------------------------------------
// TestSet aggregate root
// -----------------------------------------------------------------------------

// TestSet is the primary aggregate of the Content Delivery test-set surface.
//
// Owns TestSetQuestion child entities (composition). The aggregate guards two
// invariants:
//
//  1. question_count + total_points are computed from the non-deleted
//     inclusions in `questions`.
//  2. Once published, the question list is immutable.
type TestSet struct {
	ID          string
	TenantID    string
	AuthorGCID  string
	Title       string
	Description string
	State       TestSetState
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PublishedAt *time.Time
	DeletedAt   *time.Time

	// SourceJobID — Lane 1c (CHO-1703 / ADR-180 D10): UUIDv7 of the
	// chora-creation batch question_generation_jobs row whose
	// chora.creation.question_batch.accepted.v1 event assembled this test
	// set. nil for hand-authored test sets. Cross-domain UUID reference
	// WITHOUT FK per ddd-enforcement #3; UNIQUE among non-null values
	// (migration 0028) — the event subscriber's idempotency key.
	// Immutable provenance: set at construction, never re-pointed.
	SourceJobID *string

	mu        sync.Mutex
	questions []*TestSetQuestion // ordered by display_order asc via Questions()

	// LEG3-E summary counters — set by List-style adapters that scan an
	// aggregate subquery instead of loading child rows. nil ⇒ accessors
	// walk the `questions` slice. See HydrateSummaryCounters.
	summaryQuestionCount *int
	summaryTotalPoints   *float64
}

// TestSetQuestion is a per-question inclusion row owned by the TestSet
// aggregate.
//
// LEG3-D R3 Option B (2026-05-16) — the inclusion row stores TWO distinct
// cross-domain references:
//
//   - QuestionAtomID — UUIDv7 of the LearningAtom in chora_creation.
//     The canonical authoring identifier.
//   - QuestionID — UUIDv7 of the Question embedded inside the atom's
//     payload (e.g., `mcq_payload.question_id`). Consumed by
//     chora.services.creation.v1.Creation/SnapshotQuestionByID at
//     TestSet.PublishWithSnapshot time.
//
// No FKs (cross-DB queries forbidden per ddd-enforcement #3); both UUIDs
// are validated at the HTTP layer + at AddQuestion guard time.
type TestSetQuestion struct {
	ID             string
	TestSetID      string
	QuestionAtomID string
	// QuestionID is the embedded Question UUID inside the atom's payload.
	// Distinct from QuestionAtomID per LEG3-D R3 Option B. Required —
	// AddQuestion rejects blank.
	QuestionID   string
	QuestionType string // mcq | oe (validated at construction)
	DisplayOrder int
	Points       float64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time

	// Fix-F (Lane A snapshot debt — migrations/0011_test_set_questions_snapshot.up.sql):
	// PayloadSnapshot is the canonical MCQPayload / OEPayload JSON captured
	// from chora_creation at TestSet.Publish() time. Empty for DRAFT rows;
	// SET to non-empty for PUBLISHED rows. SnapshotAt mirrors the producer's
	// updated_at.
	//
	// Per ddd-enforcement #3 (cross-DB queries forbidden): this field is
	// the runtime grading lookup source — chora-delivery NEVER touches
	// chora_creation.questions at /submit time.
	PayloadSnapshot string
	SnapshotAt      *time.Time
}

// -----------------------------------------------------------------------------
// Construction
// -----------------------------------------------------------------------------

// NewTestSetInput is the input shape for NewTestSet. Tenant + Author + Title
// are required; Description is optional.
//
// SourceJobID (optional, Lane 1c / ADR-180 D10) is the chora-creation batch
// job UUID for event-assembled test sets — empty/whitespace ⇒ hand-authored
// (nil on the aggregate).
type NewTestSetInput struct {
	TenantID    string
	AuthorGCID  string
	Title       string
	Description string
	SourceJobID string
}

// NewTestSet constructs a TestSet aggregate in DRAFT state with an empty
// question list.
//
// Validation guards (all return ErrInvalidArgument):
//
//   - tenant_id required
//   - author_gcid required
//   - title required (trim-non-empty, max 256 chars per OpenAPI)
//   - description, when set, capped at 2000 chars per OpenAPI
func NewTestSet(in NewTestSetInput) (*TestSet, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.AuthorGCID) == "" {
		return nil, fmt.Errorf("%w: author_gcid required", ErrInvalidArgument)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: title required", ErrInvalidArgument)
	}
	if len(title) > 256 {
		return nil, fmt.Errorf("%w: title exceeds 256 chars", ErrInvalidArgument)
	}
	if len(in.Description) > 2000 {
		return nil, fmt.Errorf("%w: description exceeds 2000 chars", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	ts := &TestSet{
		ID:          NewUUIDv7(),
		TenantID:    in.TenantID,
		AuthorGCID:  in.AuthorGCID,
		Title:       title,
		Description: in.Description,
		State:       TestSetStateDraft,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if sj := strings.TrimSpace(in.SourceJobID); sj != "" {
		ts.SourceJobID = &sj
	}
	return ts, nil
}

// -----------------------------------------------------------------------------
// AddQuestion / UpdateQuestion / RemoveQuestion
// -----------------------------------------------------------------------------

// AddQuestionInput is the input shape for TestSet.AddQuestion.
//
// LEG3-D R3 Option B — both QuestionAtomID AND QuestionID are required.
// The HTTP layer maps them from request body fields `question_atom_id` +
// `question_id`. See docs/m13/wave3-leg3d-round3-blocker-2026-05-16.md.
type AddQuestionInput struct {
	// QuestionAtomID is the UUIDv7 of the LearningAtom in chora_creation
	// that carries the question payload.
	QuestionAtomID string
	// QuestionID is the UUIDv7 of the embedded Question inside the atom's
	// payload (e.g., mcq_payload.question_id). Required — chora-delivery
	// does NOT resolve this from the atom; the caller (FE picker)
	// extracts it from the loaded atom payload before POST.
	QuestionID   string
	QuestionType string  // mcq | oe
	DisplayOrder int     // 0 = default (last + 1)
	Points       float64 // > 0
}

// AddQuestion appends a question inclusion to a DRAFT test-set.
//
//   - Returns ErrTestSetPublishedImmutable when state != DRAFT.
//   - When DisplayOrder is 0, the inclusion is placed at last+1 (1-indexed).
func (ts *TestSet) AddQuestion(in AddQuestionInput) (*TestSetQuestion, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if err := ts.guardMutableLocked(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.QuestionAtomID) == "" {
		return nil, fmt.Errorf("%w: question_atom_id required", ErrInvalidArgument)
	}
	// LEG3-D R3 Option B — explicit embedded question_id required.
	// Fail loud at add-time so a missing FE picker resolution never
	// bubbles to a publish-time SnapshotQuestionByID NotFound (502).
	if strings.TrimSpace(in.QuestionID) == "" {
		return nil, fmt.Errorf("%w: question_id required (embedded UUID inside the atom payload, e.g. mcq_payload.question_id)", ErrInvalidArgument)
	}
	qtype := strings.TrimSpace(in.QuestionType)
	if qtype == "" {
		return nil, fmt.Errorf("%w: question_type required", ErrInvalidArgument)
	}
	qtype = canonicalQuestionType(qtype)
	if !validQuestionTypes[qtype] {
		return nil, fmt.Errorf("%w: question_type %q not supported (allowed: mcq, oe)", ErrInvalidArgument, qtype)
	}
	if in.Points <= 0 {
		return nil, fmt.Errorf("%w: points must be > 0", ErrInvalidArgument)
	}

	order := in.DisplayOrder
	if order == 0 {
		order = ts.nextDisplayOrderLocked()
	}
	now := time.Now().UTC()
	q := &TestSetQuestion{
		ID:             NewUUIDv7(),
		TestSetID:      ts.ID,
		QuestionAtomID: in.QuestionAtomID,
		QuestionID:     in.QuestionID,
		QuestionType:   qtype,
		DisplayOrder:   order,
		Points:         in.Points,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	ts.questions = append(ts.questions, q)
	ts.UpdatedAt = now
	return q, nil
}

// UpdateTestSetMetadataInput is the input shape for TestSet.UpdateMetadata.
// Each pointer field is optional — nil means "do not modify". Mirrors the
// REST UpdateTestSetRequest schema (chora-contracts/openapi/
// delivery-test-sets.yaml#UpdateTestSetRequest) for the fields the
// aggregate currently carries (title + description). Wider fields
// (learner_facing_name, tags, default_grading_config) are reserved for
// Phase 2 once they land on the aggregate.
type UpdateTestSetMetadataInput struct {
	Title       *string
	Description *string
}

// UpdateMetadata partially updates a DRAFT test-set's metadata.
//
// Contract: chora-contracts/openapi/delivery-test-sets.yaml#updateTestSet.
// State guard: DRAFT only. PUBLISHED → ErrTestSetPublishedImmutable (HTTP
// 409 — caller MUST archive + recreate). ARCHIVED → ErrTestSetArchived.
//
// Empty/whitespace Title rejected (titles are user-displayed and a blank
// is the symptom that originally surfaced as "10+ rows look identical").
// updated_at bumps only when at least one field actually mutates.
func (ts *TestSet) UpdateMetadata(in UpdateTestSetMetadataInput) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if err := ts.guardMutableLocked(); err != nil {
		return err
	}

	if in.Title == nil && in.Description == nil {
		return nil // no-op — no UpdatedAt bump
	}

	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return fmt.Errorf("%w: title must be non-blank", ErrInvalidArgument)
		}
		if len(t) > 256 {
			return fmt.Errorf("%w: title exceeds 256 chars", ErrInvalidArgument)
		}
		ts.Title = t
	}
	if in.Description != nil {
		d := *in.Description
		if len(d) > 2000 {
			return fmt.Errorf("%w: description exceeds 2000 chars", ErrInvalidArgument)
		}
		ts.Description = d
	}
	ts.UpdatedAt = time.Now().UTC()
	return nil
}

// UpdateQuestionInput is the input shape for TestSet.UpdateQuestion. Each
// pointer field is optional — nil means "do not modify".
type UpdateQuestionInput struct {
	Points       *float64
	DisplayOrder *int
}

// UpdateQuestion updates a single question inclusion's points / display_order
// on a DRAFT test-set.
//
//   - Returns ErrTestSetPublishedImmutable when state != DRAFT.
//   - Returns ErrTestSetQuestionNotFound when no inclusion matches the id.
//   - question_atom_id is IMMUTABLE — to swap atoms, remove + add a new row.
func (ts *TestSet) UpdateQuestion(questionID string, in UpdateQuestionInput) (*TestSetQuestion, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if err := ts.guardMutableLocked(); err != nil {
		return nil, err
	}
	q := ts.findQuestionLocked(questionID)
	if q == nil {
		return nil, fmt.Errorf("%w: id=%s", ErrTestSetQuestionNotFound, questionID)
	}
	if in.Points != nil {
		if *in.Points <= 0 {
			return nil, fmt.Errorf("%w: points must be > 0", ErrInvalidArgument)
		}
		q.Points = *in.Points
	}
	if in.DisplayOrder != nil {
		if *in.DisplayOrder < 0 {
			return nil, fmt.Errorf("%w: display_order must be >= 0", ErrInvalidArgument)
		}
		q.DisplayOrder = *in.DisplayOrder
	}
	now := time.Now().UTC()
	q.UpdatedAt = now
	ts.UpdatedAt = now
	return q, nil
}

// RemoveQuestion soft-deletes a question inclusion on a DRAFT test-set.
//
//   - Returns ErrTestSetPublishedImmutable when state != DRAFT.
//   - Returns ErrTestSetQuestionNotFound when no live inclusion matches.
//   - Idempotent: removing an already-deleted row is a no-op (nil error).
func (ts *TestSet) RemoveQuestion(questionID string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if err := ts.guardMutableLocked(); err != nil {
		return err
	}
	q := ts.findQuestionLocked(questionID)
	if q == nil {
		return fmt.Errorf("%w: id=%s", ErrTestSetQuestionNotFound, questionID)
	}
	now := time.Now().UTC()
	q.DeletedAt = &now
	ts.UpdatedAt = now
	return nil
}

// -----------------------------------------------------------------------------
// Publish / Archive
// -----------------------------------------------------------------------------

// Publish transitions the test-set from DRAFT to PUBLISHED.
//
// Idempotent: re-publishing an already-PUBLISHED test-set returns nil with
// no domain mutation (PublishedAt is preserved). Per delivery-test-sets.yaml
// §publishTestSet "Idempotent re-publish".
//
// Errors:
//
//   - ErrTestSetNoQuestions when the live question_count is 0.
//   - ErrTestSetArchived when the test-set has been archived.
func (ts *TestSet) Publish() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	switch ts.State {
	case TestSetStatePublished:
		return nil // idempotent
	case TestSetStateArchived:
		return ErrTestSetArchived
	}
	if ts.liveQuestionCountLocked() == 0 {
		return ErrTestSetNoQuestions
	}
	now := time.Now().UTC()
	ts.State = TestSetStatePublished
	ts.PublishedAt = &now
	ts.UpdatedAt = now
	return nil
}

// PublishWithSnapshot atomically transitions DRAFT → PUBLISHED while capturing
// the canonical Question payload for every live question inclusion into the
// `PayloadSnapshot` field via the supplied QuestionSnapshotter port.
//
// Per Fix-F (Lane A snapshot debt close) + `feedback_no_stubs_real_wiring`:
//
//   - Snapshotter error → state NOT advanced; return the error. Caller
//     surfaces as HTTP 502 (DELIVERY_TEST_SET_SNAPSHOT_FAILED). The aggregate
//     stays in DRAFT so a retry is safe.
//   - Snapshotter nil (dev / in-memory) → equivalent to Publish() (no
//     snapshot captured). Production wiring MUST supply a non-nil
//     snapshotter or grading will fail-loud at /submit per
//     Submission.GradeMCQAnswersFromSnapshot's ErrMCQSnapshotMissing path.
//   - Idempotent re-publish (already PUBLISHED) → no error, no re-fetch.
//
// The snapshot fetch happens UNDER the aggregate lock so concurrent Publish
// attempts can't race the snapshot capture.
//
// Errors:
//
//   - ErrTestSetNoQuestions / ErrTestSetArchived (echoed from Publish())
//   - QuestionSnapshotter error (transport / NotFound / etc.) — fail loud
//
// actorGCID is the authenticated publish actor (gateway `gcid` header /
// event envelope gcid) threaded verbatim into every SnapshotQuestion call —
// the ADR-229 consent gate refuses on the far side, so no predicate logic
// lives here (chokepoint 2 is chora-creation's).
func (ts *TestSet) PublishWithSnapshot(ctx context.Context, snapshotter QuestionSnapshotter, actorGCID string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	switch ts.State {
	case TestSetStatePublished:
		return nil // idempotent — do NOT re-fetch snapshots
	case TestSetStateArchived:
		return ErrTestSetArchived
	}
	if ts.liveQuestionCountLocked() == 0 {
		return ErrTestSetNoQuestions
	}
	// Fetch snapshots BEFORE state mutation. Any failure leaves the
	// aggregate in DRAFT so the caller can retry.
	//
	// LEG3-D R3 Option B — the snapshotter receives `q.QuestionID` (the
	// embedded Question UUID inside the atom payload), NOT
	// `q.QuestionAtomID` (the LearningAtom row UUID). chora-creation's
	// SnapshotQuestionByID looks up by question_id; passing atom_id
	// returns NotFound. See docs/m13/wave3-leg3d-round3-blocker-
	// 2026-05-16.md.
	if snapshotter != nil {
		now := time.Now().UTC()
		for _, q := range ts.questions {
			if q.DeletedAt != nil {
				continue
			}
			snap, err := snapshotter.SnapshotQuestion(ctx, ts.TenantID, q.QuestionID, actorGCID)
			if err != nil {
				return fmt.Errorf("delivery: snapshot question %s (atom %s) for test_set %s: %w",
					q.QuestionID, q.QuestionAtomID, ts.ID, err)
			}
			q.PayloadSnapshot = snap.PayloadJSON
			capturedAt := snap.CapturedAt
			if capturedAt.IsZero() {
				capturedAt = now
			}
			q.SnapshotAt = &capturedAt
		}
	}
	now := time.Now().UTC()
	ts.State = TestSetStatePublished
	ts.PublishedAt = &now
	ts.UpdatedAt = now
	return nil
}

// Archive soft-archives the test-set. ANY state can be archived; subsequent
// calls are no-ops (idempotent).
func (ts *TestSet) Archive() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.State == TestSetStateArchived {
		return
	}
	now := time.Now().UTC()
	ts.State = TestSetStateArchived
	ts.DeletedAt = &now
	ts.UpdatedAt = now
}

// HydrateQuestion appends a pre-persisted question inclusion onto the
// aggregate WITHOUT triggering the state guard. Used by the pg adapter's
// Get() to rehydrate a loaded test-set's child rows after the parent's
// state has already flipped to PUBLISHED.
//
// This is a load-time builder — NOT part of the mutation API. Callers
// outside of repository adapters MUST use AddQuestion instead so the
// append-only-on-PUBLISHED invariant is enforced.
func (ts *TestSet) HydrateQuestion(in TestSetQuestion) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if in.TestSetID == "" {
		in.TestSetID = ts.ID
	}
	// Copy to avoid external mutation; the in value lives in the caller.
	cp := in
	ts.questions = append(ts.questions, &cp)
}

// -----------------------------------------------------------------------------
// Read accessors
// -----------------------------------------------------------------------------

// QuestionCount returns the count of non-deleted question inclusions.
//
// When the aggregate has been loaded via a List-style query that scanned
// only the parent row + an aggregated counter (per LEG3-E), the live
// `questions` slice is empty but the shadow override is set; this path
// returns the override so list views don't render `0 questions` for
// test-sets that actually carry N MCQs. Full Get loads children + clears
// the override automatically (HydrateQuestion drops the shadow).
func (ts *TestSet) QuestionCount() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.questions) == 0 && ts.summaryQuestionCount != nil {
		return *ts.summaryQuestionCount
	}
	return ts.liveQuestionCountLocked()
}

// TotalPoints returns the sum of points across non-deleted inclusions.
// Same summary-override semantics as QuestionCount.
func (ts *TestSet) TotalPoints() float64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.questions) == 0 && ts.summaryTotalPoints != nil {
		return *ts.summaryTotalPoints
	}
	var total float64
	for _, q := range ts.questions {
		if q.DeletedAt != nil {
			continue
		}
		total += q.Points
	}
	return total
}

// HydrateSummaryCounters injects pre-aggregated question_count + total_points
// onto the TestSet without loading the child rows. Intended for List-style
// queries that include a SQL aggregate subquery (LEG3-E). Once child rows
// are hydrated via HydrateQuestion these overrides are ignored — the live
// slice is the source of truth.
//
// Pass a negative `count` or negative `total` to leave the corresponding
// override unset.
func (ts *TestSet) HydrateSummaryCounters(count int, total float64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if count >= 0 {
		c := count
		ts.summaryQuestionCount = &c
	}
	if total >= 0 {
		t := total
		ts.summaryTotalPoints = &t
	}
}

// Questions returns a copy of the non-deleted inclusions ordered by
// display_order ascending. Stable order: equal display_order ties break by
// CreatedAt ascending.
func (ts *TestSet) Questions() []*TestSetQuestion {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]*TestSetQuestion, 0, len(ts.questions))
	for _, q := range ts.questions {
		if q.DeletedAt != nil {
			continue
		}
		// Copy so external callers cannot mutate aggregate state.
		cp := *q
		out = append(out, &cp)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DisplayOrder != out[j].DisplayOrder {
			return out[i].DisplayOrder < out[j].DisplayOrder
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// -----------------------------------------------------------------------------
// Internal helpers (locked-context callers)
// -----------------------------------------------------------------------------

// guardMutableLocked returns an error when the test-set's question list is
// not in a mutable state (PUBLISHED or ARCHIVED).
func (ts *TestSet) guardMutableLocked() error {
	switch ts.State {
	case TestSetStatePublished:
		return ErrTestSetPublishedImmutable
	case TestSetStateArchived:
		return ErrTestSetArchived
	}
	return nil
}

func (ts *TestSet) liveQuestionCountLocked() int {
	n := 0
	for _, q := range ts.questions {
		if q.DeletedAt == nil {
			n++
		}
	}
	return n
}

func (ts *TestSet) nextDisplayOrderLocked() int {
	max := 0
	for _, q := range ts.questions {
		if q.DeletedAt != nil {
			continue
		}
		if q.DisplayOrder > max {
			max = q.DisplayOrder
		}
	}
	return max + 1
}

func (ts *TestSet) findQuestionLocked(id string) *TestSetQuestion {
	for _, q := range ts.questions {
		if q.ID == id && q.DeletedAt == nil {
			return q
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Port — TestSetRepo
// -----------------------------------------------------------------------------

// TestSetRepo is the hexagonal persistence port for TestSet. Two adapters
// satisfy it:
//
//   - pg.TestSetRepo — production, Postgres-backed via chora_delivery.
//   - InMemTestSetRepo (this package) — local dev + unit tests.
//
// All methods take a context.Context so the pg adapter can apply RLS
// (rls.ApplySession) per multi-tenant-rls.
//
// Cross-DB queries forbidden — chora-delivery reads only chora_delivery.
type TestSetRepo interface {
	Save(ctx context.Context, ts *TestSet) error
	Get(ctx context.Context, tenantID, id string) (*TestSet, bool, error)
}
