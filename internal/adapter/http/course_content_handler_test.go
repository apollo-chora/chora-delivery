package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

const (
	tnt     = "11111111-1111-7111-8111-111111111111"
	crs     = "019e30db-692f-7d10-8ce0-59669fe9298d"
	atomRef = "019e30db-0000-7000-8000-0000000000a1"
)

type noopPub struct{}

func (noopPub) PublishContentComposed(context.Context, string, *cc.CourseContent) error { return nil }

func newHandler() http.HandlerFunc {
	svc := cc.NewService(inmem.NewCourseContentRepo(), noopPub{})
	return courseContentHandler(&CourseContentDeps{Svc: svc})
}

func do(h http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Tenant-Id", tnt)
	req.Header.Set("gcid", "00000000-0000-7000-8000-0000000000ac")
	// CHO-2233: mutations gate on an instructor-level role — the shared
	// harness represents the R+ instructor editor session.
	req.Header.Set("x-mesh-user-roles", "instructor")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestHandler_AddListReorderRemove(t *testing.T) {
	h := newHandler()
	base := "/api/v1/courses/" + crs + "/content"

	// add atom
	rec := do(h, http.MethodPost, base, `{"kind":"atom","ref":"`+atomRef+`","title":"Intro"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	// add video
	rec = do(h, http.MethodPost, base, `{"kind":"video","ref":"https://cdn/x.mp4","title":"Lecture"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add video: want 201, got %d", rec.Code)
	}

	// list → 2 items
	rec = do(h, http.MethodGet, base, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", rec.Code)
	}
	var got struct {
		CourseID string `json:"course_id"`
		Items    []struct {
			ItemID string `json:"item_id"`
			Kind   string `json:"kind"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Items) != 2 {
		t.Fatalf("list: want 2 items, got %d", len(got.Items))
	}
	id0, id1 := got.Items[0].ItemID, got.Items[1].ItemID

	// reorder
	rec = do(h, http.MethodPost, base+"/reorder", `{"ordered_item_ids":["`+id1+`","`+id0+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	// remove id0
	rec = do(h, http.MethodDelete, base+"/"+id0, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: want 200, got %d", rec.Code)
	}
	rec = do(h, http.MethodGet, base, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Items) != 1 {
		t.Fatalf("after remove: want 1 item, got %d", len(got.Items))
	}
}

// ---------------------------------------------------------------------------
// CHO-2233 — curriculum writes must gate on an instructor-level role read from
// x-mesh-user-roles, mirroring the CJ2 course-lifecycle gate. Live-proven
// 2026-07-17: a pure learner and a pure auditor each appended and deleted
// curriculum items via the API while the FE surface guard bounced them.
// ---------------------------------------------------------------------------

func doWithRoles(h http.HandlerFunc, method, path, body, roles string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Tenant-Id", tnt)
	req.Header.Set("gcid", "00000000-0000-7000-8000-0000000000ac")
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestHandler_WriteEndpoints_RequireInstructorRole(t *testing.T) {
	base := "/api/v1/courses/" + crs + "/content"
	addBody := `{"kind":"atom","ref":"` + atomRef + `","title":"Intro"}`

	writes := []struct {
		name, method, path, body string
	}{
		{"append", http.MethodPost, base, addBody},
		{"reorder", http.MethodPost, base + "/reorder", `{"ordered_item_ids":[]}`},
		{"upload-url", http.MethodPost, base + "/upload-url", `{"mime":"video/mp4","size_bytes":1024}`},
		{"remove", http.MethodDelete, base + "/some-item-id", ""},
	}

	// Non-instructor sessions are refused on every mutation branch, and the
	// absent-header case fails CLOSED (403, not a pass-through).
	for _, wr := range writes {
		for _, roles := range []string{"learner", "auditor", "auditor,learner", ""} {
			t.Run(wr.name+"/refused/"+roles, func(t *testing.T) {
				h := newHandler()
				rec := doWithRoles(h, wr.method, wr.path, wr.body, roles)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s as %q: want 403, got %d (%s)", wr.name, roles, rec.Code, rec.Body.String())
				}
			})
		}
	}

	// Instructor-level sessions pass the gate. The list pins the CANONICAL
	// identity token `training_admin` (what the gateway mint actually stamps,
	// mint_handler.go TrainingAdminRole) alongside the legacy hyphen constant,
	// and the parse is case-insensitive over a comma list.
	for _, roles := range []string{"instructor", "ADMIN", "training_admin,learner", "training-admin"} {
		t.Run("append/allowed/"+roles, func(t *testing.T) {
			h := newHandler()
			rec := doWithRoles(h, http.MethodPost, base, addBody, roles)
			if rec.Code != http.StatusCreated {
				t.Fatalf("append as %q: want 201, got %d (%s)", roles, rec.Code, rec.Body.String())
			}
		})
	}

	// The read path stays open by design (CHO-2233 AC: do not over-restrict
	// reads here; the learner read-path is consumption's projection anyway).
	t.Run("list/stays-open", func(t *testing.T) {
		h := newHandler()
		rec := doWithRoles(h, http.MethodGet, base, "", "learner")
		if rec.Code != http.StatusOK {
			t.Fatalf("list as learner: want 200, got %d", rec.Code)
		}
	})
}

func TestHandler_MissingTenant(t *testing.T) {
	h := newHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/courses/"+crs+"/content", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant: want 400, got %d", rec.Code)
	}
}

func TestHandler_InvalidKind(t *testing.T) {
	h := newHandler()
	rec := do(h, http.MethodPost, "/api/v1/courses/"+crs+"/content", `{"kind":"podcast","ref":"`+atomRef+`","title":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind: want 400, got %d", rec.Code)
	}
}

func TestHandler_GetEmptyCourse_Returns200EmptyList(t *testing.T) {
	// A course with no curriculum yet is an EMPTY collection (200 { items: [] }),
	// NOT a 404 — so the instructor editor renders the empty add-item state.
	// (Walk-caught 2026-06-19: the 404 made the R+ editor show "Couldn't load".)
	h := newHandler()
	rec := do(h, http.MethodGet, "/api/v1/courses/"+crs+"/content", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty course: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		CourseID string `json:"course_id"`
		Items    []any  `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CourseID != crs || len(got.Items) != 0 {
		t.Fatalf("empty course: want course_id=%s + 0 items, got %+v", crs, got)
	}
}

func TestHandler_RemoveNotFound(t *testing.T) {
	h := newHandler()
	// create curriculum first so the aggregate exists, then remove a ghost id
	_ = do(h, http.MethodPost, "/api/v1/courses/"+crs+"/content", `{"kind":"atom","ref":"`+atomRef+`","title":"x"}`)
	rec := do(h, http.MethodDelete, "/api/v1/courses/"+crs+"/content/ghost", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("remove ghost: want 404, got %d", rec.Code)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	h := newHandler()
	rec := do(h, http.MethodPut, "/api/v1/courses/"+crs+"/content", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: want 405, got %d", rec.Code)
	}
}
