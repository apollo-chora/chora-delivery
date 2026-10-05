// Package delivery — list-side method for the CertificationRegistry.
//
// Add-only file added 2026-05-26 for the R+ /r/certifications surface
// (M12, R+ buildout). The original CertificationRegistry (delivery.go)
// exposes Issue + Get + GetByLearnerCourse; the new /api/v1/certifications
// GET list handler needs a per-tenant projection with optional learner +
// course filters.
//
// Same in-memory map (`byID`) is iterated under the same mutex as the
// other registry methods so the projection is consistent with concurrent
// Issue() calls.
package delivery

import (
	"sort"
	"strings"
)

// ListByTenant returns the issued certifications for a tenant, filtered
// optionally by (learner_gcid, course_id). When a filter argument is the
// empty string it is ignored (no filtering on that axis). Results are
// sorted by ID (UUIDv7 ⇒ creation-order) for stable client pagination.
//
// Returns ([]*Certification, total). The total is the unsliced match
// count BEFORE the offset/limit window is applied so the FE can render
// "showing X of Y" counters once pagination lands; the current handler
// returns all matches in a single page.
//
// Concurrency: takes the registry mutex so the snapshot is consistent
// with concurrent Issue() callers — same lock the rest of the registry
// uses. The returned slice is a fresh allocation safe to publish to
// callers without further locking.
func (r *CertificationRegistry) ListByTenant(tenantID, learnerGCID, courseID string) ([]*Certification, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	matches := make([]*Certification, 0, len(r.byID))
	for _, c := range r.byID {
		if c.TenantID != tenantID {
			continue
		}
		if learnerGCID != "" && c.LearnerID != learnerGCID {
			continue
		}
		if courseID != "" && c.CourseID != courseID {
			continue
		}
		matches = append(matches, c)
	}
	sort.Slice(matches, func(i, j int) bool {
		return strings.Compare(matches[i].ID, matches[j].ID) < 0
	})
	return matches, len(matches)
}
