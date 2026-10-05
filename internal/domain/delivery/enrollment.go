// Enrollment — same-identity registration on a Course (Phyllis MVP).
//
// Comic anchors:
//   - Ch4 P8: "instructor on CSPO + learner on CSM, same gcid".
//   - Ch5 P10 P3: "I just enrolled. As me. Same session."
//
// Invariant (Comic Ch4 P8): POST /enrollments must NOT create a new user.
// It just stores a row linking gcid + course_id. The role context is
// computed downstream by chora-identity (`/me/roles?course_id=...`)
// based on the enrollment row + the Course's creator GCID.
//
// chora-delivery is responsible for:
//  1. storing the enrollment row,
//  2. emitting `chora.delivery.enrollment.created.v1`,
//  3. enforcing idempotency on (course_id, gcid) — duplicate POSTs
//     return the existing enrollment, NOT a fresh row.
//
// chora-delivery is NOT responsible for role projection — that's identity.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrEnrollmentNotFound is returned when a registry lookup misses.
var ErrEnrollmentNotFound = errors.New("enrollment not found")

// ErrEnrollmentNotActive is returned by Complete when the enrollment has been
// cancelled (soft-deleted) — a cancelled registration can never transition to
// completed. Fail-loud per the engineering standard.
var ErrEnrollmentNotActive = errors.New("enrollment is not active (cancelled)")

// EnrollmentStatus is the enrollment lifecycle state.
//
// Lifecycle:
//
//	active    -> completed   (Complete, on instructor/admin sign-off)
//	active    -> cancelled   (SoftDelete)
//	completed -> completed    (Complete is idempotent — re-complete is a no-op)
//
// A cancelled enrollment is terminal: Complete refuses it. The completed state
// is the per-learner course-completion fact that drives
// chora.delivery.enrollment.completed.v1.
type EnrollmentStatus string

const (
	// EnrollmentStatusActive is the default state of a fresh registration.
	EnrollmentStatusActive EnrollmentStatus = "active"
	// EnrollmentStatusCompleted is a finished registration (Complete).
	EnrollmentStatusCompleted EnrollmentStatus = "completed"
	// EnrollmentStatusCancelled is a soft-deleted registration (SoftDelete).
	EnrollmentStatusCancelled EnrollmentStatus = "cancelled"
)

// Enrollment is one learner's registration on one course.
//
// The (course_id, gcid) tuple is the natural key — duplicate Register()
// calls are idempotent (return the existing row).
//
// Completion lifecycle (migration 0035):
//   - Status      : active | completed | cancelled (reconciled with DeletedAt)
//   - CompletedAt : set when Complete() transitions active -> completed
//   - Passed      : whether the learner met the passing requirements (nil until
//     completed; pointer so "completed-without-passing" is distinct from "not
//     yet completed")
type Enrollment struct {
	ID          string
	TenantID    string
	CourseID    string
	GCID        string
	EnrolledAt  time.Time
	DeletedAt   *time.Time
	Status      EnrollmentStatus
	CompletedAt *time.Time
	Passed      *bool
}

// Complete transitions an active enrollment to completed, stamping the
// completion instant (normalised to UTC) and whether the learner passed.
//
// Semantics (per the chora.delivery.enrollment.completed.v1 driver brief):
//   - idempotent: re-completing an already-completed enrollment is a no-op —
//     CompletedAt + Passed are preserved, no error is returned. Callers detect
//     "first transition" by checking Status == completed BEFORE calling.
//   - refuses a cancelled/soft-deleted enrollment with ErrEnrollmentNotActive.
//
// Pure + time-injected — no clock, no I/O. The caller persists via
// EnrollmentCompletionPort.MarkCompleted and emits the event.
func (e *Enrollment) Complete(passed bool, now time.Time) error {
	if e.DeletedAt != nil || e.Status == EnrollmentStatusCancelled {
		return ErrEnrollmentNotActive
	}
	if e.Status == EnrollmentStatusCompleted {
		return nil // idempotent no-op
	}
	at := now.UTC()
	e.Status = EnrollmentStatusCompleted
	e.CompletedAt = &at
	e.Passed = &passed
	return nil
}

// NewEnrollment constructs a fresh Enrollment with a UUIDv7 ID.
func NewEnrollment(tenantID, courseID, gcid string) (*Enrollment, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(courseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(gcid) == "" {
		return nil, fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	return &Enrollment{
		ID:         NewUUIDv7(),
		TenantID:   tenantID,
		CourseID:   courseID,
		GCID:       gcid,
		EnrolledAt: time.Now().UTC(),
		Status:     EnrollmentStatusActive,
	}, nil
}

// SoftDelete marks the enrollment deleted (idempotent). It reconciles the
// lifecycle Status to cancelled so the status column and deleted_at agree —
// a cancelled enrollment can never be Completed.
func (e *Enrollment) SoftDelete() {
	if e.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	e.DeletedAt = &now
	e.Status = EnrollmentStatusCancelled
}

// -----------------------------------------------------------------------------
// EnrollmentRegistry — in-memory store with idempotency on (course_id, gcid)
// -----------------------------------------------------------------------------

// EnrollmentRegistry is the in-memory enrolment store.
//
// Idempotency invariant: Register(tenant, course, gcid) called twice with
// the same tuple returns the SAME enrollment row both times — no duplicate
// rows, no error.
type EnrollmentRegistry struct {
	mu    sync.Mutex
	byID  map[string]*Enrollment
	byKey map[string]*Enrollment // key = tenant_id|course_id|gcid
}

// NewEnrollmentRegistry returns an empty registry.
func NewEnrollmentRegistry() *EnrollmentRegistry {
	return &EnrollmentRegistry{
		byID:  make(map[string]*Enrollment),
		byKey: make(map[string]*Enrollment),
	}
}

// Register either creates a fresh enrolment or returns the existing one
// for the (tenant, course, gcid) tuple. The boolean second return value
// is true ONLY when a fresh row was created.
func (r *EnrollmentRegistry) Register(tenantID, courseID, gcid string) (*Enrollment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(courseID) == "" || strings.TrimSpace(gcid) == "" {
		return nil, fmt.Errorf("%w: tenant_id, course_id, gcid required", ErrInvalidArgument)
	}
	key := keyFor(tenantID, courseID, gcid)
	if existing, ok := r.byKey[key]; ok && existing.DeletedAt == nil {
		return existing, nil
	}
	e, err := NewEnrollment(tenantID, courseID, gcid)
	if err != nil {
		return nil, err
	}
	r.byID[e.ID] = e
	r.byKey[key] = e
	return e, nil
}

// Get returns an enrolment by ID.
func (r *EnrollmentRegistry) Get(id string) (*Enrollment, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok || e.DeletedAt != nil {
		return nil, false
	}
	return e, true
}

// GetByCourseAndGCID looks up the enrolment for a (tenant, course, gcid)
// tuple — the natural key.
func (r *EnrollmentRegistry) GetByCourseAndGCID(tenantID, courseID, gcid string) (*Enrollment, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byKey[keyFor(tenantID, courseID, gcid)]
	if !ok || e.DeletedAt != nil {
		return nil, false
	}
	return e, true
}

// ListByGCID returns every active enrolment for a learner inside a tenant.
//
// Used by GET /me/enrollments — the dashboard view that powers Comic Ch4 P3
// dual-card render ("CSPO → Instructor / CSM-Prep → Learner").
func (r *EnrollmentRegistry) ListByGCID(tenantID, gcid string) []*Enrollment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Enrollment, 0, 4)
	for _, e := range r.byID {
		if e.TenantID != tenantID || e.GCID != gcid || e.DeletedAt != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// CountByCourse returns the active enrolment count for a course in a tenant.
func (r *EnrollmentRegistry) CountByCourse(tenantID, courseID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.byID {
		if e.TenantID != tenantID || e.CourseID != courseID || e.DeletedAt != nil {
			continue
		}
		n++
	}
	return n
}

// keyFor builds the (tenant_id, course_id, gcid) natural key.
func keyFor(tenantID, courseID, gcid string) string {
	return tenantID + "|" + courseID + "|" + gcid
}

// -----------------------------------------------------------------------------
// EnrollmentPort — abstract surface for enrolment persistence.
//
// Mirrors the CataloguePort split: production wires pg.EnrollmentRepo
// (chora_delivery.course_enrollments backed); dev / unit tests wire
// InMemEnrollmentStore which delegates to the legacy ctx-free
// EnrollmentRegistry above.
//
// Methods take ctx so the DB adapter can:
//   1. propagate tenant_id from tracing context into rls.ApplySession()
//   2. respect cancellation on pod shutdown
//   3. carry W3C trace context across the SQL boundary
//
// Why a new interface name + wrapper instead of changing the existing
// EnrollmentRegistry: keeps the in-memory registry's many unit-test call
// sites untouched + matches the proven CataloguePort/InMemCatalogue split.
// -----------------------------------------------------------------------------

// EnrollmentPort is the 5-method abstract surface every production
// EnrollmentRegistry-style consumer (PaymentsSubscriber, httpapi handlers,
// gRPC server) depends on. Concrete impls: InMemEnrollmentStore (dev) and
// pg.EnrollmentRepo (prod).
type EnrollmentPort interface {
	// Register either creates a fresh enrolment for (tenant, course, gcid)
	// or returns the existing one. Idempotent on the natural key.
	Register(ctx context.Context, tenantID, courseID, gcid string) (*Enrollment, error)
	// Get looks up an enrolment by its enrollment_id. RLS-scoped so the
	// row only returns under the caller's tenant context. Used by the
	// V1 DELETE /v1/courses/{cid}/enrolments/{eid} cancel path.
	Get(ctx context.Context, enrollmentID string) (*Enrollment, bool, error)
	// GetByCourseAndGCID looks up the enrolment for the natural key.
	GetByCourseAndGCID(ctx context.Context, tenantID, courseID, gcid string) (*Enrollment, bool, error)
	// ListByGCID returns every active enrolment for a learner inside a tenant.
	ListByGCID(ctx context.Context, tenantID, gcid string) ([]*Enrollment, error)
	// CountByCourse returns the active enrolment count for a course.
	CountByCourse(ctx context.Context, tenantID, courseID string) (int, error)
	// Cancel soft-deletes (deleted_at=now, status=cancelled) the enrolment
	// row for enrollmentID, RLS/tenant-scoped. Idempotent — cancelling an
	// already-cancelled or absent row is a no-op that returns nil. This is
	// the persist call the DELETE cancel path was missing: on the pg adapter
	// a mutated aggregate is never written back without it (Get materialises
	// a fresh struct, so an in-struct SoftDelete() silently no-ops the row).
	Cancel(ctx context.Context, tenantID, enrollmentID string) error
}

// InMemEnrollmentStore wraps the legacy in-memory EnrollmentRegistry so it
// satisfies EnrollmentPort. The context arg is accepted then dropped — the
// underlying registry is sync + ctx-free.
type InMemEnrollmentStore struct {
	r *EnrollmentRegistry
}

// NewInMemEnrollmentStore constructs an in-memory EnrollmentPort impl.
func NewInMemEnrollmentStore() *InMemEnrollmentStore {
	return &InMemEnrollmentStore{r: NewEnrollmentRegistry()}
}

// NewInMemEnrollmentStoreFrom wraps an existing (typically pre-seeded)
// registry in the EnrollmentPort surface — mirrors NewInMemCatalogueFrom so
// fixtures can seed via the ctx-free registry API then expose it as the port.
func NewInMemEnrollmentStoreFrom(r *EnrollmentRegistry) *InMemEnrollmentStore {
	if r == nil {
		r = NewEnrollmentRegistry()
	}
	return &InMemEnrollmentStore{r: r}
}

// Registry returns the wrapped registry — exposed so dev wiring + tests
// can fall back to the ctx-free API when convenient (e.g., GET /me/enrolments
// list rendering). Production code MUST go through the EnrollmentPort
// surface.
func (a *InMemEnrollmentStore) Registry() *EnrollmentRegistry { return a.r }

// Register satisfies EnrollmentPort by delegating to the wrapped registry.
func (a *InMemEnrollmentStore) Register(_ context.Context, tenantID, courseID, gcid string) (*Enrollment, error) {
	return a.r.Register(tenantID, courseID, gcid)
}

// Get satisfies EnrollmentPort.
func (a *InMemEnrollmentStore) Get(_ context.Context, enrollmentID string) (*Enrollment, bool, error) {
	enr, ok := a.r.Get(enrollmentID)
	return enr, ok, nil
}

// GetByCourseAndGCID satisfies EnrollmentPort.
func (a *InMemEnrollmentStore) GetByCourseAndGCID(_ context.Context, tenantID, courseID, gcid string) (*Enrollment, bool, error) {
	enr, ok := a.r.GetByCourseAndGCID(tenantID, courseID, gcid)
	return enr, ok, nil
}

// ListByGCID satisfies EnrollmentPort.
func (a *InMemEnrollmentStore) ListByGCID(_ context.Context, tenantID, gcid string) ([]*Enrollment, error) {
	return a.r.ListByGCID(tenantID, gcid), nil
}

// CountByCourse satisfies EnrollmentPort.
func (a *InMemEnrollmentStore) CountByCourse(_ context.Context, tenantID, courseID string) (int, error) {
	return a.r.CountByCourse(tenantID, courseID), nil
}

// Cancel satisfies EnrollmentPort. EnrollmentRegistry.Get returns the stored
// pointer, so SoftDelete() mutates the live in-memory row — the soft-delete is
// then observable through the port's active reads. Tenant-scoped + idempotent:
// an absent row, an already-cancelled row (Get filters deleted_at), or a
// tenant mismatch is a no-op returning nil.
func (a *InMemEnrollmentStore) Cancel(_ context.Context, tenantID, enrollmentID string) error {
	enr, ok := a.r.Get(enrollmentID)
	if ok && enr.TenantID == tenantID {
		enr.SoftDelete()
	}
	return nil
}

// Compile-time assertion: InMemEnrollmentStore must satisfy EnrollmentPort.
var _ EnrollmentPort = (*InMemEnrollmentStore)(nil)
