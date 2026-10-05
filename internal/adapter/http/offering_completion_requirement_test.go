// offering_completion_requirement_test.go - CHO-2222 sub-phase 2, the WRITE
// path: PATCH /api/v1/offerings/{id}/completion-requirement.
//
// This endpoint is the other half of the resolver, and the two MUST ship
// together. An editor without a resolver leaves every declared component
// permanently unsatisfied and withholds EVERY certificate on that offering; a
// resolver without an editor is code nothing can reach.
//
// It mirrors handleOfferingSetCompletionPolicy exactly - same role gate, same
// 400 / 404 / 503 shapes, PATCH-only (edge-safe, Armor rule 998) - because the
// two endpoints are the same kind of thing: editable offering-level delivery
// policy hanging off the same aggregate.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// Refs must be UUIDs: they name rows in submissions.assessment_id /
// exam_results.exam_id, and the resolver casts them to uuid[].
const (
	reqRefAssessment = "01985e7f-2222-7abc-8def-00000000aa01"
	reqRefExam       = "01985e7f-2222-7abc-8def-00000000bb01"
)

func newCompletionRequirementServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCourseCJ2Store) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
	})
	return srv, oRepo, courseStore
}

// -----------------------------------------------------------------------------
// jsonOfferingRepo — a HIGH-FIDELITY double of pg.OfferingRepo.
//
// ⚠ WHY THIS EXISTS. inmem.OfferingRepo.Get returns the SHARED POINTER it
// stores. A handler that mutates the aggregate and then never calls Save STILL
// "persists" through it, because the handler mutated the very object the repo
// holds. A persistence assertion written against it therefore cannot fail:
// mutating the Save call out of the handler left the inmem-backed test GREEN.
// That is the defect this repo has shipped 140 times over - a guard that cannot
// fail - and it was sitting in this file until the mutation run caught it.
//
// pg.OfferingRepo does NOT behave that way: it json.Marshals into the `data`
// column on Save and json.Unmarshals a FRESH aggregate on every Get, so in
// production a dropped Save loses the declaration entirely. This double copies
// that exactly (marshal in, unmarshal out), which makes "it was saved" a
// property a test can actually observe.
type jsonOfferingRepo struct {
	mu    sync.Mutex
	by    map[string][]byte
	saves int
}

func newJSONOfferingRepo() *jsonOfferingRepo {
	return &jsonOfferingRepo{by: map[string][]byte{}}
}

var _ delivery.OfferingPort = (*jsonOfferingRepo)(nil)

func (r *jsonOfferingRepo) Save(_ context.Context, o *delivery.Offering) error {
	if o == nil {
		return nil
	}
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves++
	r.by[o.ID] = b
	return nil
}

func (r *jsonOfferingRepo) Get(_ context.Context, id string) (*delivery.Offering, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	var o delivery.Offering
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, false, err
	}
	return &o, true, nil // a FRESH object, exactly as the pg repo returns
}

func (r *jsonOfferingRepo) ListByTenant(context.Context, string) ([]*delivery.Offering, error) {
	return nil, nil
}

func (r *jsonOfferingRepo) Search(context.Context, delivery.OfferingQuery) (*delivery.OfferingSearchPage, error) {
	return nil, nil
}

func newRequirementServerJSONRepo(t *testing.T) (http.Handler, *jsonOfferingRepo) {
	t.Helper()
	oRepo := newJSONOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{curCourseA},
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "CHO-2222 requirement run",
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = curOfferingID
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	seedCJ2Course(t, courseStore, curCourseA, "Course")
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
	})
	return srv, oRepo
}

func patchRequirement(t *testing.T, srv http.Handler, offeringID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+offeringID+"/completion-requirement", b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// The declaration must SURVIVE the write path - not merely be echoed back.
//
// Driven through jsonOfferingRepo, which marshals on Save and unmarshals on Get
// exactly as pg.OfferingRepo does. Against inmem.OfferingRepo this assertion is
// unfalsifiable (see the double's comment): the handler mutates the very object
// the repo holds, so a handler with NO Save at all passes. Here, dropping the
// Save loses the declaration, which is what production would do.
func TestOfferingReq_SetCompletionRequirement_200AndPersists(t *testing.T) {
	srv, oRepo := newRequirementServerJSONRepo(t)

	w := patchRequirement(t, srv, curOfferingID, "instructor", map[string]any{
		"components": []map[string]any{
			{"kind": "assessment", "ref": "  " + reqRefAssessment + "  "},
			{"kind": "exam", "ref": reqRefExam},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	comps, ok := got["components"].([]any)
	if !ok || len(comps) != 2 {
		t.Fatalf("response must echo the 2 declared components; got %#v", got)
	}
	first := comps[0].(map[string]any)
	if first["kind"] != "assessment" || first["ref"] != reqRefAssessment {
		t.Errorf("component[0] must come back trimmed + canonical; got %#v", first)
	}
	if got["updated_at"] == nil {
		t.Error("the DTO must carry updated_at (a declaration with no timestamp cannot be audited)")
	}

	// It must actually be DURABLE - a 200 is not persistence. Reloading through
	// a repo that rehydrates from bytes is the only way to tell the difference.
	o, found, err := oRepo.Get(context.Background(), curOfferingID)
	if err != nil || !found {
		t.Fatalf("reload offering: err=%v found=%v", err, found)
	}
	if o.CompletionRequirement == nil || len(o.CompletionRequirement.Components) != 2 {
		t.Fatalf("the requirement must SURVIVE the write (reloaded from the stored bytes), got %+v. "+
			"A 200 whose declaration is not written means the gate reads 'none declared' forever.",
			o.CompletionRequirement)
	}
	if o.CompletionRequirement.Components[0].Ref != reqRefAssessment {
		t.Errorf("the stored ref must be canonical, got %q", o.CompletionRequirement.Components[0].Ref)
	}
	if oRepo.saves == 0 {
		t.Error("the handler must persist via Save; nothing was written")
	}
}

// The GET the later FE sub-phase reads: the requirement must be visible
// alongside the policy, or the editor has nothing to render.
func TestOfferingReq_GetCertification_ReflectsTheRequirement(t *testing.T) {
	srv, oRepo, courseStore := newCompletionRequirementServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Course")

	if w := patchRequirement(t, srv, curOfferingID, "instructor", map[string]any{
		"components": []map[string]any{{"kind": "assessment", "ref": reqRefAssessment}},
	}); w.Code != http.StatusOK {
		t.Fatalf("seed PATCH: %d %s", w.Code, w.Body.String())
	}

	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+curOfferingID+"/certification", nil, instructor, "instructor")
	gw := httptest.NewRecorder()
	srv.ServeHTTP(gw, r)
	if gw.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d body=%s", gw.Code, gw.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(gw.Body.Bytes(), &got)
	cr, ok := got["completion_requirement"].(map[string]any)
	if !ok {
		t.Fatalf("GET must expose completion_requirement; got %#v", got)
	}
	comps, ok := cr["components"].([]any)
	if !ok || len(comps) != 1 {
		t.Fatalf("completion_requirement.components wrong: %#v", cr)
	}
}

// Declaring nothing is legitimate and meaningful: it records "this offering
// requires nothing beyond its policy", and it is how an admin CLEARS a
// declaration. It must be a 200, not a 400.
func TestOfferingReq_DeclaringNothing_Is200AndClears(t *testing.T) {
	srv, oRepo := newRequirementServerJSONRepo(t)

	if w := patchRequirement(t, srv, curOfferingID, "instructor", map[string]any{
		"components": []map[string]any{{"kind": "assessment", "ref": reqRefAssessment}},
	}); w.Code != http.StatusOK {
		t.Fatalf("seed PATCH: %d", w.Code)
	}
	w := patchRequirement(t, srv, curOfferingID, "instructor", map[string]any{"components": []map[string]any{}})
	if w.Code != http.StatusOK {
		t.Fatalf("declaring no components must be allowed; want 200, got %d body=%s", w.Code, w.Body.String())
	}
	o, _, _ := oRepo.Get(context.Background(), curOfferingID)
	if o.CompletionRequirement == nil {
		t.Fatal("an empty declaration is still a declaration and must be stored")
	}
	if len(o.CompletionRequirement.Components) != 0 {
		t.Errorf("the declaration must be cleared, got %+v", o.CompletionRequirement.Components)
	}
}

// Every domain guard must surface as a 400 naming the offending entry - not a
// 500, and never a silent drop. A 500 would blame the platform for the admin's
// typo and would never alert as a client error.
func TestOfferingReq_Guards_400(t *testing.T) {
	cases := []struct {
		name string
		body any
		want string // a fragment the message must carry
	}{
		{
			name: "unknown kind",
			body: map[string]any{"components": []map[string]any{{"kind": "homework", "ref": reqRefAssessment}}},
			want: "kind",
		},
		{
			// chora_delivery has no per-learner project-completion source, so
			// arming one would withhold every certificate on this offering
			// forever. Refused at the point the admin types it.
			name: "project kind has no completion source",
			body: map[string]any{"components": []map[string]any{{"kind": "project", "ref": reqRefAssessment}}},
			want: "completion source",
		},
		{
			name: "blank ref",
			body: map[string]any{"components": []map[string]any{{"kind": "assessment", "ref": "   "}}},
			want: "blank",
		},
		{
			// The pg-as-validator trap: without this the ref reaches a uuid[]
			// cast inside a Pub/Sub handler and 22P02s every released grade on
			// the offering, far from the request that stored it.
			name: "ref is not a UUID",
			body: map[string]any{"components": []map[string]any{{"kind": "assessment", "ref": "assessment-a"}}},
			want: "UUID",
		},
		{
			name: "duplicate component",
			body: map[string]any{"components": []map[string]any{
				{"kind": "assessment", "ref": reqRefAssessment},
				{"kind": "assessment", "ref": reqRefAssessment},
			}},
			want: "twice",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, oRepo, courseStore := newCompletionRequirementServer(t)
			seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
			seedCJ2Course(t, courseStore, curCourseA, "Course")

			w := patchRequirement(t, srv, curOfferingID, "instructor", c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
			}
			if !jsonBodyContains(w, c.want) {
				t.Errorf("the 400 must say what is wrong (want a mention of %q); got %s", c.want, w.Body.String())
			}
			// A refused declaration must not half-apply.
			o, _, _ := oRepo.Get(context.Background(), curOfferingID)
			if o != nil && o.CompletionRequirement != nil {
				t.Errorf("a refused declaration must leave the offering untouched, got %+v", o.CompletionRequirement)
			}
		})
	}
}

func TestOfferingReq_404WhenOfferingMissing(t *testing.T) {
	srv, _, _ := newCompletionRequirementServer(t)
	w := patchRequirement(t, srv, curOfferingID, "instructor", map[string]any{
		"components": []map[string]any{{"kind": "assessment", "ref": reqRefAssessment}},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("offering missing: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingReq_403WithoutRole(t *testing.T) {
	srv, oRepo, courseStore := newCompletionRequirementServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Course")
	w := patchRequirement(t, srv, curOfferingID, "", map[string]any{
		"components": []map[string]any{{"kind": "assessment", "ref": reqRefAssessment}},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("no role: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

// PATCH-only: the declaration is read via GET /certification alongside the
// policy, so any other verb on this path is a 405 rather than a silent 200.
func TestOfferingReq_MethodNotAllowed(t *testing.T) {
	srv, oRepo, courseStore := newCompletionRequirementServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Course")

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		r := reqWithHeaders(method, "/api/v1/offerings/"+curOfferingID+"/completion-requirement", nil, instructor, "instructor")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: want 405, got %d", method, w.Code)
		}
	}
}

// An unwired repo is a 503, never a 200. Silently accepting a declaration that
// is never stored would be the worst kind of green: the admin sees success, the
// gate never sees a component.
func TestOfferingReq_503WhenOfferingsRepoUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{}) // nothing wired
	w := patchRequirement(t, srv, curOfferingID, "instructor", map[string]any{
		"components": []map[string]any{{"kind": "assessment", "ref": reqRefAssessment}},
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired repo: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}

// A malformed body is the CALLER's fault: 400, not 500.
func TestOfferingReq_400WhenBodyIsNotJSON(t *testing.T) {
	srv, oRepo, courseStore := newCompletionRequirementServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Course")

	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+curOfferingID+"/completion-requirement",
		[]byte("{not json"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// jsonBodyContains reports whether the error body mentions `want`, case-
// insensitively: the assertion is that the 400 EXPLAINS itself, not that it
// matches a byte-exact sentence.
func jsonBodyContains(w *httptest.ResponseRecorder, want string) bool {
	return want == "" || strings.Contains(strings.ToLower(w.Body.String()), strings.ToLower(want))
}
