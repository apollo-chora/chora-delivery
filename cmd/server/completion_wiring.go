// completion_wiring.go — adapters that let the auto-issue engine (CHO-2157)
// read the facts it needs from ports that already exist.
//
// The engine decides; these only fetch. Everything they touch lives in
// chora_delivery, so no cross-DB query and no cross-domain event is involved.
package main

import (
	"context"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/module"
	"github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// courseCertReader adapts the existing course lookup to the engine's narrow
// "does this course require all content?" question.
type courseCertReader struct {
	courses interface {
		Get(ctx context.Context, tenantID, courseID string) (*domain.Course, bool, error)
	}
}

func (r courseCertReader) CertDefinition(ctx context.Context, tenantID, courseID string) (bool, bool, bool, error) {
	if r.courses == nil {
		return false, false, false, nil
	}
	c, ok, err := r.courses.Get(ctx, tenantID, courseID)
	if err != nil {
		return false, false, false, err
	}
	if !ok || c == nil {
		return false, false, false, nil
	}
	return c.Certification.Enabled, c.Certification.RequireAllContent, true, nil
}

// moduleContentReader answers "has this learner completed every content
// component the course declares?" from the W7 StudentModuleProgress projection.
//
// A course that declares NO modules has nothing to complete, so it reports TRUE
// — the requirement is vacuous, not unmet. Reporting false there would withhold
// a certificate from every learner on every course without a module structure,
// which is the live shape of the graduate-walk course itself (cert_require_all_
// content = true, zero modules).
type moduleContentReader struct {
	modules  module.ModulePort
	progress moduleprogress.ProgressPort
}

func (r moduleContentReader) AllContentComplete(ctx context.Context, tenantID, courseID, learnerGCID string) (bool, error) {
	if r.modules == nil || r.progress == nil {
		// No module machinery wired ⇒ nothing to require. Fail OPEN here is
		// correct: this gate exists to add a requirement, not to invent one.
		return true, nil
	}
	mods, err := r.modules.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		return false, err
	}
	if len(mods) == 0 {
		return true, nil // the course declares no content components
	}
	for _, m := range mods {
		p, ok, err := r.progress.GetByLearnerModule(ctx, tenantID, learnerGCID, m.ID)
		if err != nil {
			return false, err
		}
		if !ok || p == nil || !p.IsComplete {
			return false, nil
		}
	}
	return true, nil
}
