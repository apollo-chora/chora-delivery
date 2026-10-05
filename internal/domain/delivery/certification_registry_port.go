// certification_registry_port.go — CertificationRegistry as a CertificationStore
// (CHO-2157).
//
// The registry keeps its original map-backed methods (used widely in tests) and
// gains the ctx-aware CertificationStore surface, so production can swap in
// pg.CertificationRepo and everything else keeps compiling.
//
// The registry is now explicitly the TEST / LOCAL-DEV implementation. It is NOT
// durable — a restart empties it — which is exactly why certificates issued by
// the deployed platform used to vanish, and why an auto-issue engine must never
// be wired to it in production.
package delivery

import "context"

// Compile-time proof that both implementations satisfy the port. If either ever
// drifts, the build breaks here rather than in production.
var _ CertificationStore = (*CertificationRegistry)(nil)

// IssueCtx implements CertificationStore.Issue over the in-memory maps.
//
// score is accepted and ignored: the in-memory registry has nowhere to put it,
// and only the durable pg store records it (certifications.score).
func (r *CertificationRegistry) IssueCtx(_ context.Context, tenantID, learnerGCID, courseID string, _ *int, accomplishments []string) (*Certification, error) {
	return r.Issue(tenantID, learnerGCID, courseID, accomplishments)
}

// GetCtx implements CertificationStore.Get.
func (r *CertificationRegistry) GetCtx(_ context.Context, tenantID, id string) (*Certification, bool, error) {
	c, ok := r.Get(id)
	if !ok || c.TenantID != tenantID {
		return nil, false, nil
	}
	return c, true, nil
}

// GetByLearnerCourseCtx implements CertificationStore.GetByLearnerCourse.
func (r *CertificationRegistry) GetByLearnerCourseCtx(_ context.Context, tenantID, learnerGCID, courseID string) (*Certification, bool, error) {
	c, ok := r.GetByLearnerCourse(learnerGCID, courseID)
	if !ok || c.TenantID != tenantID {
		return nil, false, nil
	}
	return c, true, nil
}

// ListByTenantCtx implements CertificationStore.ListByTenant.
func (r *CertificationRegistry) ListByTenantCtx(_ context.Context, tenantID, learnerGCID, courseID string) ([]*Certification, error) {
	certs, _ := r.ListByTenant(tenantID, learnerGCID, courseID)
	return certs, nil
}
