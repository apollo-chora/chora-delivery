// offering_schedule_handler_test.go — handler-level verification of the R+
// offering-nested Schedule & Rooms surface:
//
//	GET  /api/v1/offerings/{id}/schedule  — list the offering's sessions
//	POST /api/v1/offerings/{id}/schedule  — create one session
//
// OfferingSession is offering-scoped (own table, mig 0036) — NO cross-DB query,
// NO JSONB-on-offering (unlike Sections; Attendance references a session id).
// Mirrors offering_sections_handler_test.go's admin-gated write harness.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	oschOfferingID = "01985e7f-6666-7abc-8def-0000000000f1"
	oschOfferingB  = "01985e7f-6666-7abc-8def-0000000000f2"
)

type oschSession struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Room           string `json:"room"`
	InstructorGCID string `json:"instructor_gcid"`
	StartsAt       string `json:"starts_at"`
	EndsAt         string `json:"ends_at"`
}

type oschListResp struct {
	Sessions []oschSession `json:"sessions"`
}

func newOfferingScheduleTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *repoinmem.OfferingSessionRepo) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	sessRepo := repoinmem.NewOfferingSessionRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:        oRepo,
		OfferingSessions: sessRepo,
	})
	return srv, oRepo, sessRepo
}

// seedScheduleOffering stores a short offering under the standard tenant.
func seedScheduleOffering(t *testing.T, oRepo *inmem.OfferingRepo, id string) {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01985e7f-6666-7abc-8def-000000000a01"},
		DeliveryType: delivery.DeliveryTypeShort,
		Label:        "Schedule Run",
		Capacity:     30,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
}

func postSession(t *testing.T, srv http.Handler, offeringID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringID+"/schedule", []byte(body), instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func getSchedule(t *testing.T, srv http.Handler, offeringID, role string) (*httptest.ResponseRecorder, oschListResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringID+"/schedule", nil, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp oschListResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

// validSessionBody is a ROOMLESS session (no room_id) — allowed, and exercises
// the non-room concerns (create/list/order/isolation/authz) without seeding a
// Room. Room-booking is exercised in offering_schedule_room_gate_test.go.
func validSessionBody(title string, start time.Time) string {
	return `{"title":"` + title + `","instructor_gcid":"` + instructor +
		`","starts_at":"` + start.UTC().Format(time.RFC3339) +
		`","ends_at":"` + start.Add(2*time.Hour).UTC().Format(time.RFC3339) + `"}`
}

// -----------------------------------------------------------------------------

func TestOfferingSchedule_Get_EmptyList(t *testing.T) {
	srv, oRepo, _ := newOfferingScheduleTestServer(t)
	seedScheduleOffering(t, oRepo, oschOfferingID)
	w, resp := getSchedule(t, srv, oschOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Sessions) != 0 {
		t.Fatalf("sessions: want 0, got %d", len(resp.Sessions))
	}
	if !strings.Contains(w.Body.String(), `"sessions":[]`) {
		t.Fatalf("body must contain sessions=[], got %s", w.Body.String())
	}
}

func TestOfferingSchedule_Post_CreatesThenListed(t *testing.T) {
	srv, oRepo, _ := newOfferingScheduleTestServer(t)
	seedScheduleOffering(t, oRepo, oschOfferingID)
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

	w := postSession(t, srv, oschOfferingID, "instructor", validSessionBody("Week 1", start))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var created oschSession
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.ID == "" || created.Title != "Week 1" || created.Room != "" {
		t.Fatalf("created session (want roomless): %+v", created)
	}

	_, resp := getSchedule(t, srv, oschOfferingID, "instructor")
	if len(resp.Sessions) != 1 || resp.Sessions[0].ID != created.ID {
		t.Fatalf("list after create: want [%s], got %+v", created.ID, resp.Sessions)
	}
}

func TestOfferingSchedule_Get_OrderedByStartsAt(t *testing.T) {
	srv, oRepo, _ := newOfferingScheduleTestServer(t)
	seedScheduleOffering(t, oRepo, oschOfferingID)
	late := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	early := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	// Create the LATER session first; the list must still come back early→late.
	postSession(t, srv, oschOfferingID, "instructor", validSessionBody("Week 2", late))
	postSession(t, srv, oschOfferingID, "instructor", validSessionBody("Week 1", early))

	_, resp := getSchedule(t, srv, oschOfferingID, "instructor")
	if len(resp.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(resp.Sessions))
	}
	if resp.Sessions[0].Title != "Week 1" || resp.Sessions[1].Title != "Week 2" {
		t.Fatalf("order: want [Week 1, Week 2], got [%s, %s]", resp.Sessions[0].Title, resp.Sessions[1].Title)
	}
}

func TestOfferingSchedule_Isolation_OtherOfferingNotListed(t *testing.T) {
	srv, oRepo, _ := newOfferingScheduleTestServer(t)
	seedScheduleOffering(t, oRepo, oschOfferingID)
	seedScheduleOffering(t, oRepo, oschOfferingB)
	postSession(t, srv, oschOfferingID, "instructor", validSessionBody("A-only", time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)))

	_, respB := getSchedule(t, srv, oschOfferingB, "instructor")
	if len(respB.Sessions) != 0 {
		t.Fatalf("offering B must not see A's session, got %d", len(respB.Sessions))
	}
}

func TestOfferingSchedule_Get_404WhenOfferingMissing(t *testing.T) {
	srv, _, _ := newOfferingScheduleTestServer(t)
	w, _ := getSchedule(t, srv, oschOfferingID, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSchedule_Post_400OnInvalid(t *testing.T) {
	srv, oRepo, _ := newOfferingScheduleTestServer(t)
	seedScheduleOffering(t, oRepo, oschOfferingID)
	// Missing title → domain guard → 400.
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	body := `{"title":"","starts_at":"` + start.Format(time.RFC3339) + `","ends_at":"` + start.Add(time.Hour).Format(time.RFC3339) + `"}`
	w := postSession(t, srv, oschOfferingID, "instructor", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (blank title), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSchedule_Post_400OnMalformedBody(t *testing.T) {
	srv, oRepo, _ := newOfferingScheduleTestServer(t)
	seedScheduleOffering(t, oRepo, oschOfferingID)
	w := postSession(t, srv, oschOfferingID, "instructor", `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (malformed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSchedule_403WithoutRole(t *testing.T) {
	srv, oRepo, _ := newOfferingScheduleTestServer(t)
	seedScheduleOffering(t, oRepo, oschOfferingID)
	w, _ := getSchedule(t, srv, oschOfferingID, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET status: want 403, got %d", w.Code)
	}
	wp := postSession(t, srv, oschOfferingID, "", validSessionBody("X", time.Now().UTC()))
	if wp.Code != http.StatusForbidden {
		t.Fatalf("POST status: want 403, got %d", wp.Code)
	}
}

func TestOfferingSchedule_405OnPut(t *testing.T) {
	srv, oRepo, _ := newOfferingScheduleTestServer(t)
	seedScheduleOffering(t, oRepo, oschOfferingID)
	r := reqWithHeaders(http.MethodPut, "/api/v1/offerings/"+oschOfferingID+"/schedule", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d", w.Code)
	}
}

func TestOfferingSchedule_503WhenSessionsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // OfferingSessions nil
	seedScheduleOffering(t, oRepo, oschOfferingID)
	w, _ := getSchedule(t, srv, oschOfferingID, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}
