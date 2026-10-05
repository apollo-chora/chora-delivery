// sitting_repos_test.go — direct unit tests for the W4 Brick-B in-memory
// doubles (SittingRepo + InvigilatorRepo + IncidentRepo). Pins tenant/sitting
// scoping, soft-delete filtering, and the incident append-only surface.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	sbTenantA = "019e2f93-d586-71b5-8c3d-e2b0d0d5bb01"
	sbTenantB = "019e2f93-d586-71b5-8c3d-e2b0d0d5bb02"
	sbExam1   = "019e2f93-d586-71b5-8c3d-e2b0d0d5cc01"
	sbSit1    = "019e2f93-d586-71b5-8c3d-e2b0d0d5dd01"
	sbSit2    = "019e2f93-d586-71b5-8c3d-e2b0d0d5dd02"
	sbGCID    = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee01"
)

func ctxBG() context.Context { return context.Background() }

func mkSitting(t *testing.T, tenant, examID string) *exam.ExamSitting {
	t.Helper()
	start := time.Now().UTC().Add(24 * time.Hour)
	s, err := exam.NewExamSitting(exam.NewExamSittingInput{
		TenantID: tenant, ExamID: examID, StartsAt: start, EndsAt: start.Add(2 * time.Hour), Capacity: 20,
	})
	if err != nil {
		t.Fatalf("NewExamSitting: %v", err)
	}
	return s
}

// -----------------------------------------------------------------------------
// SittingRepo
// -----------------------------------------------------------------------------

func TestInmemSittingRepo_SaveGetList(t *testing.T) {
	r := inmem.NewSittingRepo()
	s := mkSitting(t, sbTenantA, sbExam1)
	if err := r.Save(ctxBG(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, _ := r.Get(ctxBG(), s.ID)
	if !ok || got.ID != s.ID {
		t.Fatalf("Get: want hit, got ok=%v", ok)
	}
	list, err := r.ListByExam(ctxBG(), sbTenantA, sbExam1)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByExam: err=%v len=%d want 1", err, len(list))
	}
}

func TestInmemSittingRepo_TenantAndSoftDeleteScoping(t *testing.T) {
	r := inmem.NewSittingRepo()
	a := mkSitting(t, sbTenantA, sbExam1)
	b := mkSitting(t, sbTenantB, sbExam1) // different tenant, same exam id
	del := mkSitting(t, sbTenantA, sbExam1)
	_ = del.SoftDelete()
	for _, s := range []*exam.ExamSitting{a, b, del} {
		if err := r.Save(ctxBG(), s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	list, err := r.ListByExam(ctxBG(), sbTenantA, sbExam1)
	if err != nil {
		t.Fatalf("ListByExam: %v", err)
	}
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("ListByExam must exclude other-tenant + soft-deleted; got %d", len(list))
	}
}

// -----------------------------------------------------------------------------
// InvigilatorRepo
// -----------------------------------------------------------------------------

func mkInvig(t *testing.T, tenant, sitting, gcid string, rank exam.InvigilatorRank) *exam.ExamInvigilator {
	t.Helper()
	iv, err := exam.NewExamInvigilator(exam.NewExamInvigilatorInput{
		TenantID: tenant, SittingID: sitting, InvigilatorGCID: gcid, Rank: rank,
	})
	if err != nil {
		t.Fatalf("NewExamInvigilator: %v", err)
	}
	return iv
}

func TestInmemInvigilatorRepo_ListBySittingExcludesDeletedAndOtherSitting(t *testing.T) {
	r := inmem.NewInvigilatorRepo()
	keep := mkInvig(t, sbTenantA, sbSit1, sbGCID, exam.InvigilatorRankInvigilator)
	other := mkInvig(t, sbTenantA, sbSit2, sbGCID, exam.InvigilatorRankInvigilator)
	gone := mkInvig(t, sbTenantA, sbSit1, "019e2f93-d586-71b5-8c3d-e2b0d0d5ee09", exam.InvigilatorRankObserver)
	_ = gone.Unassign()
	for _, iv := range []*exam.ExamInvigilator{keep, other, gone} {
		if err := r.Save(ctxBG(), iv); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	list, err := r.ListBySitting(ctxBG(), sbTenantA, sbSit1)
	if err != nil {
		t.Fatalf("ListBySitting: %v", err)
	}
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("ListBySitting must scope to sitting + exclude unassigned; got %d", len(list))
	}
	if got, ok, _ := r.Get(ctxBG(), keep.ID); !ok || got.ID != keep.ID {
		t.Fatalf("Get: want hit")
	}
}

// -----------------------------------------------------------------------------
// IncidentRepo (append-only)
// -----------------------------------------------------------------------------

func mkIncident(t *testing.T, tenant, sitting string) *exam.IncidentReport {
	t.Helper()
	ir, err := exam.NewIncidentReport(exam.NewIncidentReportInput{
		TenantID: tenant, SittingID: sitting, ReportedByGCID: sbGCID,
		Kind: exam.IncidentKindOther, Narrative: "note",
	})
	if err != nil {
		t.Fatalf("NewIncidentReport: %v", err)
	}
	return ir
}

func TestInmemIncidentRepo_AppendListGet(t *testing.T) {
	r := inmem.NewIncidentRepo()
	a := mkIncident(t, sbTenantA, sbSit1)
	b := mkIncident(t, sbTenantA, sbSit1)
	other := mkIncident(t, sbTenantA, sbSit2)
	for _, ir := range []*exam.IncidentReport{a, b, other} {
		if err := r.Append(ctxBG(), ir); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	list, err := r.ListBySitting(ctxBG(), sbTenantA, sbSit1)
	if err != nil {
		t.Fatalf("ListBySitting: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListBySitting want 2 (scoped to sitting); got %d", len(list))
	}
	if got, ok, _ := r.Get(ctxBG(), a.ID); !ok || got.ID != a.ID {
		t.Fatalf("Get: want hit")
	}
}

// Compile-time assertions that the inmem doubles satisfy the domain ports.
var (
	_ exam.ExamSittingStore     = (*inmem.SittingRepo)(nil)
	_ exam.ExamInvigilatorStore = (*inmem.InvigilatorRepo)(nil)
	_ exam.IncidentReportStore  = (*inmem.IncidentRepo)(nil)
)
