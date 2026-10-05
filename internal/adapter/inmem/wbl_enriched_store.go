// wbl_enriched_store.go - the EnrichedWblStore decorator (CHO-2335).
//
// The /r/wbl list handler receives only its wbl.WblStore, but rendering a
// human learner_name + course_title needs two sources that live outside the
// store: the chora_delivery.user_directory name projection
// (directory.UserDirectoryPort) + the courses title repo (delivery.CourseRepo).
// This decorator EMBEDS the base WblStore (so Save / Get / ListByTenant forward
// untouched) and ADDS the wbl.PlacementEnricher capability the list handler
// type-asserts for. cmd/server wires it as deps.Wbl; the bare in-mem / pg
// stores keep working when wrapped, and any store NOT wrapped degrades to the
// raw-id fallback in the handler.
//
// This is the same intra-chora_delivery read-stitch pattern the CourseRoster
// READ VIEW uses (roster_repo.go): a same-DB LEFT-JOIN-style projection, NOT a
// cross-DB query (which is forbidden). The directory is tenant-agnostic (global
// GCID key); course-title resolution is tenant-scoped via CourseRepo.Get so a
// caller never resolves a title outside its own tenant.
package inmem

import (
	"context"
	"strings"

	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
	"github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

// Compile-time assertions - the decorator MUST satisfy both the persistence
// port (so it can back deps.Wbl) and the enrichment capability (so the handler
// can type-assert it). A signature drift on either surfaces at build time.
var (
	_ wbl.WblStore          = (*EnrichedWblStore)(nil)
	_ wbl.PlacementEnricher = (*EnrichedWblStore)(nil)
)

// EnrichedWblStore wraps a base wbl.WblStore with the display-metadata
// resolvers. Both resolvers are optional: a nil directory / courses degrades
// the corresponding lookup to an empty map (the raw-id fallback), so the store
// is always safe to wrap even before the projections are wired.
type EnrichedWblStore struct {
	wbl.WblStore // embedded - Save / Get / ListByTenant forward to the inner store

	directory directory.UserDirectoryPort
	courses   delivery.CourseRepo
}

// NewEnrichedWblStore wraps inner with the supplied resolvers. inner MUST be
// non-nil - wrapping a nil store is a boot-time wiring bug, so we fail loud
// rather than defer a nil-deref to first request. dir / courses MAY be nil
// (each degrades to the id fallback for its field).
func NewEnrichedWblStore(inner wbl.WblStore, dir directory.UserDirectoryPort, courses delivery.CourseRepo) *EnrichedWblStore {
	if inner == nil {
		panic("inmem.NewEnrichedWblStore: inner WblStore required")
	}
	return &EnrichedWblStore{WblStore: inner, directory: dir, courses: courses}
}

// LearnerNames resolves gcid → display_name via the user_directory projection.
// A nil directory or empty input short-circuits to an empty map. Unresolved /
// empty names are left ABSENT - the handler applies the gcid fallback.
func (s *EnrichedWblStore) LearnerNames(ctx context.Context, gcids []string) (map[string]string, error) {
	if s.directory == nil || len(gcids) == 0 {
		return map[string]string{}, nil
	}
	raw, err := s.directory.LookupNames(ctx, gcids)
	if err != nil {
		// FAIL LOUD - surface the real cause rather than silently degrading to
		// gcid-only rendering (feedback_no_stubs_real_wiring).
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for g, name := range raw {
		if strings.TrimSpace(name) != "" {
			out[g] = name
		}
	}
	return out, nil
}

// CourseTitles resolves courseID → title via the tenant-scoped CourseRepo.
// A nil courses repo or empty input short-circuits to an empty map. Course ids
// are deduped so a list of placements sharing a course costs one Get. Unknown /
// cross-tenant / soft-deleted / empty-title courses are left ABSENT - the
// handler applies the course_id fallback.
func (s *EnrichedWblStore) CourseTitles(ctx context.Context, tenantID string, courseIDs []string) (map[string]string, error) {
	if s.courses == nil || len(courseIDs) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(courseIDs))
	for _, id := range courseIDs {
		if id == "" || out[id] != "" {
			continue // skip empties + already-resolved dupes
		}
		c, ok, err := s.courses.Get(ctx, tenantID, id)
		if err != nil {
			// FAIL LOUD - a dead course read must surface, not masquerade as an
			// unresolved title.
			return nil, err
		}
		if ok && strings.TrimSpace(c.Title) != "" {
			out[id] = c.Title
		}
	}
	return out, nil
}
