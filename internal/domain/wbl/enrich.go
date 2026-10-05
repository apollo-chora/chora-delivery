// enrich.go - the optional display-enrichment port for the WBL list surface
// (CHO-2335).
//
// The /r/wbl list rendered raw learner GCIDs + raw course UUIDs. The list
// handler resolves those to a human learner_name + course_title, but the
// resolution sources (the chora_delivery.user_directory name projection + the
// courses title repo) live OUTSIDE the WblStore. Rather than widen every
// WblStore adapter, the enrichment is an OPTIONAL capability: the list handler
// type-asserts the injected WblStore for PlacementEnricher and, when present,
// batch-resolves the display fields; a store that does not implement it (the
// bare in-mem dev/test adapter) degrades to the raw-id fallback so the screen
// still renders.
//
// Batch-oriented (maps keyed by id) so the list path costs one directory
// lookup + one deduped course pass, not one round-trip per row.
package wbl

import "context"

// PlacementEnricher resolves the display metadata a WBL placement row shows in
// place of its raw ids. Both methods return a partial map: an id that does not
// resolve is simply ABSENT from the map, and the caller applies the raw-id
// fallback (keeping the empty→id policy in the handler, not the resolver).
type PlacementEnricher interface {
	// LearnerNames maps each supplied gcid to its resolved display name
	// (chora_delivery.user_directory projection). Absent/empty ⇒ not in the
	// map. An empty input short-circuits to an empty map with no error.
	LearnerNames(ctx context.Context, gcids []string) (map[string]string, error)
	// CourseTitles maps each supplied courseID (scoped to tenantID) to its
	// title. Absent (unknown, wrong tenant, or soft-deleted) ⇒ not in the map.
	// An empty input short-circuits to an empty map with no error.
	CourseTitles(ctx context.Context, tenantID string, courseIDs []string) (map[string]string, error)
}
