// offering_completion_requirement_test.go - CHO-2222.
//
// THE QUESTION THIS FILE ANSWERS: does the new CompletionRequirement actually
// PERSIST?
//
// completion_requirement.go claims the type "rides the Offering JSONB snapshot"
// following CompletionPolicy's precedent. That claim was never tested at the
// repo layer: before this file, NO pg test mentioned CompletionRequirement OR
// CompletionPolicy. Both were asserted only in the pure domain, where nothing
// marshals.
//
// The claim is load-bearing in the worst way. If OfferingRepo mapped columns or
// fields EXPLICITLY, a field added to the aggregate would silently never
// persist: every domain test would stay green, the editor would return 200 with
// the requirement echoed back, and the gate would read nil forever. The feature
// would be dead on arrival while looking complete from every angle a test
// usually looks from.
//
// It happens to be true - Save does json.Marshal(o) on the WHOLE aggregate and
// Get does json.Unmarshal into the WHOLE aggregate (offering.go:78 + :113), so
// any exported field rides along. These tests PIN that, so a future refactor to
// explicit column mapping fails HERE rather than in production.
//
// The round-trip below is a real one: the bytes the WRITE path produced are the
// bytes the READ path consumes.
package pg_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// Component refs are canonical UUIDs (assessments.assessment_id + exams.id are
// both UUID columns, and the resolver casts to uuid[]).
const (
	reqAssessmentRef = "01970000-0000-7000-a000-0000000000a1"
	reqExamRef       = "01970000-0000-7000-b000-0000000000e1"
)

// offeringWithRequirement builds a saved-shape Offering carrying a declared
// two-component requirement.
func offeringWithRequirement(t *testing.T, id string) *domain.Offering {
	t.Helper()
	o := newOffering(id, domain.OfferingStateRunning)
	if err := o.SetCompletionRequirement(domain.SetCompletionRequirementInput{
		Components: []domain.CompletionComponent{
			{Kind: domain.ComponentKindAssessment, Ref: reqAssessmentRef},
			{Kind: domain.ComponentKindExam, Ref: reqExamRef},
		},
	}); err != nil {
		t.Fatalf("SetCompletionRequirement: %v", err)
	}
	return o
}

// saveAndCaptureData runs Save through the stub and returns the exact `data`
// bytes handed to the upsert - $7, the JSONB snapshot column.
func saveAndCaptureData(t *testing.T, o *domain.Offering) []byte {
	t.Helper()
	q := &stubQuerier{}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, o); err != nil {
		t.Fatalf("Save: %v", err)
	}
	last := len(q.sqls) - 1
	if !strings.Contains(q.sqls[last], "INSERT INTO offerings") {
		t.Fatalf("expected the upsert to be the last SQL; got %q", q.sqls[last])
	}
	data, ok := q.args[last][6].([]byte) // $7 = data
	if !ok {
		t.Fatalf("the data arg ($7) must be []byte JSONB; got %T", q.args[last][6])
	}
	return data
}

// The WRITE path must actually put the requirement in the blob. If Save ever
// stops marshalling the whole aggregate, this is the test that says so.
func TestOfferingRepo_Save_PutsCompletionRequirementInTheJSONB(t *testing.T) {
	t.Parallel()
	o := offeringWithRequirement(t, "01970000-0000-7000-9999-f00000000101")
	data := saveAndCaptureData(t, o)

	// Decode generically rather than into the aggregate: unmarshalling into
	// domain.Offering would prove only that Go can read its own struct back,
	// not that the KEY the column actually carries is the one we think.
	var blob map[string]json.RawMessage
	if err := json.Unmarshal(data, &blob); err != nil {
		t.Fatalf("the data column must be valid JSON: %v", err)
	}
	// The JSONB key is the Go field name: the Offering aggregate carries NO json
	// tags (which is why the live search predicate reads data->'CourseIDs'
	// verbatim, offering.go). Pinning the key name here means a future
	// json:"-" / rename / explicit-mapping refactor breaks THIS test rather
	// than silently emptying the gate in production.
	raw, ok := blob["CompletionRequirement"]
	if !ok {
		t.Fatalf("the JSONB snapshot has NO CompletionRequirement key - the declaration does "+
			"NOT persist and the whole gate is dead on arrival. Keys present: %v", blobKeys(blob))
	}
	var req domain.CompletionRequirement
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal CompletionRequirement from the blob: %v", err)
	}
	if got := len(req.Components); got != 2 {
		t.Fatalf("want 2 declared components in the blob, got %d (%+v)", got, req.Components)
	}
	if req.Components[0].Ref != reqAssessmentRef || req.Components[0].Kind != domain.ComponentKindAssessment {
		t.Errorf("component[0] must survive the write verbatim, got %+v", req.Components[0])
	}
	if req.Components[1].Ref != reqExamRef || req.Components[1].Kind != domain.ComponentKindExam {
		t.Errorf("component[1] must survive the write verbatim, got %+v", req.Components[1])
	}
	if req.UpdatedAt.IsZero() {
		t.Error("UpdatedAt must persist (a declaration with no timestamp cannot be audited)")
	}
}

// The full loop: the bytes Save produced, fed to Get, rehydrate the declaration.
// This is the property the gate depends on - the subscriber reads
// off.CompletionRequirement straight off a repo Get.
func TestOfferingRepo_CompletionRequirement_RoundTripsThroughJSONB(t *testing.T) {
	t.Parallel()
	want := offeringWithRequirement(t, "01970000-0000-7000-9999-f00000000102")
	data := saveAndCaptureData(t, want)

	// Read the SAME bytes back through the real read path.
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = data
				return nil
			}}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	got, ok, err := r.Get(tracing.WithTenantID(context.Background(), tenantID), want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got == nil {
		t.Fatal("Get must rehydrate the offering")
	}
	if got.CompletionRequirement == nil {
		t.Fatal("CompletionRequirement is NIL after a round-trip through the JSONB snapshot - " +
			"the declaration does not survive persistence, so every declared component would " +
			"read as 'none declared' and the gate would never fire")
	}
	if n := len(got.CompletionRequirement.Components); n != 2 {
		t.Fatalf("want 2 components after round-trip, got %d", n)
	}
	for i, want := range []domain.CompletionComponent{
		{Kind: domain.ComponentKindAssessment, Ref: reqAssessmentRef},
		{Kind: domain.ComponentKindExam, Ref: reqExamRef},
	} {
		if got.CompletionRequirement.Components[i] != want {
			t.Errorf("component[%d]: want %+v, got %+v", i, want, got.CompletionRequirement.Components[i])
		}
	}
	// The sibling policy rides the same blob; a regression would hit both.
	if got.CompletionPolicy != nil {
		t.Errorf("this fixture declares no policy; got %+v", got.CompletionPolicy)
	}
}

// An offering that declares NOTHING must round-trip as nil, NOT as an empty
// non-nil requirement. This is the backward-compatible shape of every offering
// that predates the type, and EvaluateCertificate treats nil as vacuously
// satisfied - so a write path that fabricated an empty requirement would be
// harmless, but one that fabricated a NON-empty one would withhold every
// certificate. Pin the shape.
func TestOfferingRepo_NoRequirementDeclared_RoundTripsAsNil(t *testing.T) {
	t.Parallel()
	o := newOffering("01970000-0000-7000-9999-f00000000103", domain.OfferingStateRunning)
	data := saveAndCaptureData(t, o)

	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = data
				return nil
			}}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	got, ok, err := r.Get(tracing.WithTenantID(context.Background(), tenantID), o.ID)
	if err != nil || !ok {
		t.Fatalf("Get: err=%v ok=%v", err, ok)
	}
	if got.CompletionRequirement != nil {
		t.Errorf("an offering that declares nothing must rehydrate with a NIL requirement, got %+v",
			got.CompletionRequirement)
	}
}

func blobKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
