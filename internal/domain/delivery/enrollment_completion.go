// enrollment_completion.go — ADD-ONLY extension that persists the
// enrollment-completion fact. Drives chora.delivery.enrollment.completed.v1.
//
// Separate file (mirrors enrollment_listbycourse.go) so the completion add
// doesn't perturb enrollment.go's Phyllis-MVP invariants + Comic anchors.
//
// Port-choice rationale: a focused EnrollmentCompletionPort kept SEPARATE from
// the 5-method EnrollmentPort — exactly the EnrollmentListByCoursePort pattern
// — so the broad port's four consumers (gRPC server, HTTP handlers, payments
// subscriber, roster) are untouched, and the production pg adapter opts into
// the write on its own cutover schedule via a compile-time assertion.
package delivery

import "context"

// EnrollmentCompletionPort persists a completed Enrollment's lifecycle fields
// (status / completed_at / passed). The domain decision (the Complete() state
// transition) is made by the caller on the loaded aggregate; this port only
// persists the resulting aggregate state.
//
// Concrete impls: InMemEnrollmentStore (dev / unit tests) and
// pg.EnrollmentRepo (prod, RLS-scoped UPDATE).
type EnrollmentCompletionPort interface {
	// MarkCompleted persists the completion fields of an already-Completed
	// Enrollment. Implementations MUST be tenant/RLS-scoped and MUST NOT
	// resurrect a soft-deleted row.
	MarkCompleted(ctx context.Context, e *Enrollment) error
}

// MarkCompleted persists the completion fields onto the registry's stored
// entry. The registry is the canonical in-memory store; this looks the row up
// by ID and copies the completion fields so the persist is real even when the
// caller mutated a different *Enrollment pointer.
func (r *EnrollmentRegistry) MarkCompleted(e *Enrollment) error {
	if e == nil {
		return ErrEnrollmentNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[e.ID]
	if !ok || stored.DeletedAt != nil {
		return ErrEnrollmentNotFound
	}
	stored.Status = e.Status
	stored.CompletedAt = e.CompletedAt
	stored.Passed = e.Passed
	return nil
}

// MarkCompleted satisfies EnrollmentCompletionPort for the InMemEnrollmentStore
// by delegating to the wrapped registry.
func (a *InMemEnrollmentStore) MarkCompleted(_ context.Context, e *Enrollment) error {
	return a.r.MarkCompleted(e)
}

// Compile-time assertion: InMemEnrollmentStore must satisfy the focused port.
var _ EnrollmentCompletionPort = (*InMemEnrollmentStore)(nil)
