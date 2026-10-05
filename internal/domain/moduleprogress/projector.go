// projector.go — the StudentModuleProgress projection service. Given a learner's
// completion of a referenced atom/assessment, it resolves the ENROLLED module
// targets and advances the learner's per-module progress on each.
//
// This is a domain service (pure orchestration over ports, no infra import): the
// atom_session.completed + submission.graded push inboxes both call RecordCompletion.
// Idempotency is layered — the subscriber dedupes on event_id, and the aggregate
// itself no-ops a re-recorded item — so an at-least-once redelivery is safe.
package moduleprogress

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Projector folds content-item completions into per-module progress.
type Projector struct {
	resolver CompletionResolver
	modules  ModuleLoader
	progress ProgressPort
}

// NewProjector wires the resolver + module loader + progress store. A nil
// dependency makes RecordCompletion fail loud (no silent no-op).
func NewProjector(resolver CompletionResolver, modules ModuleLoader, progress ProgressPort) *Projector {
	return &Projector{resolver: resolver, modules: modules, progress: progress}
}

// RecordCompletion resolves (kind, ref) to the enrolled learner's module targets
// and advances progress on each. Returns the number of projection rows changed
// (0 when the completed content is in no module of a course the learner takes —
// the common case, and a safe ack). ctx MUST already carry the tenant (the pg
// resolver + module loader apply RLS from it).
//
//   - kind "atom"       → ref is the atom id (course_content_items.ref, kind=atom)
//   - kind "assessment" → ref is the assessment id (kind=assessment)
func (p *Projector) RecordCompletion(ctx context.Context, tenantID, gcid, kind, ref string) (int, error) {
	if p == nil || p.resolver == nil || p.modules == nil || p.progress == nil {
		return 0, errors.New("moduleprogress: projector not fully wired")
	}
	tenantID = strings.TrimSpace(tenantID)
	gcid = strings.TrimSpace(gcid)
	kind = strings.TrimSpace(kind)
	ref = strings.TrimSpace(ref)
	if tenantID == "" || gcid == "" || kind == "" || ref == "" {
		return 0, fmt.Errorf("%w: tenant_id, gcid, kind and ref are all required", ErrInvalidArgument)
	}

	targets, err := p.resolver.ResolveEnrolledTargets(ctx, tenantID, gcid, kind, ref)
	if err != nil {
		return 0, fmt.Errorf("moduleprogress: resolve targets: %w", err)
	}

	changed := 0
	for _, tgt := range targets {
		m, ok, err := p.modules.Get(ctx, tgt.ModuleID)
		if err != nil {
			return changed, fmt.Errorf("moduleprogress: load module %s: %w", tgt.ModuleID, err)
		}
		if !ok || m == nil {
			// The module was soft-deleted between resolve and load — skip (the
			// resolver already excludes soft-deleted modules; this is the race).
			continue
		}

		// Advance is atomic (row-locked in pg): load-or-create + record + persist
		// in one unit so concurrent completions of different items in the same
		// module by this learner cannot race and drop one.
		didChange, err := p.progress.Advance(ctx, tenantID, gcid, tgt.ModuleID, tgt.CourseID, tgt.ContentItemID, m)
		if err != nil {
			// The resolver claimed this item is in the module but the loaded
			// module disagrees (item removed mid-flight) — skip, not fatal.
			if errors.Is(err, ErrItemNotInModule) {
				continue
			}
			return changed, fmt.Errorf("moduleprogress: advance %s/%s: %w", gcid, tgt.ModuleID, err)
		}
		if didChange {
			changed++
		}
	}
	return changed, nil
}
