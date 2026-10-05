// offering_attendance_handler_test.go — handler-level verification of the R+
// offering-nested Attendance surface:
//
//	GET  /api/v1/offerings/{id}/attendance?session_id={sid}
//	POST /api/v1/offerings/{id}/attendance
//
// Session-scoped marks (offering_attendance domain, mig 0037); Upsert idempotent
// on (tenant,session,gcid). Mirrors the schedule handler harness.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
)

const (
	oattOfferingID = "01985e7f-7777-7abc-8def-0000000000f1"
	oattSessionID  = "01985e7f-7777-7abc-8def-000000000501"
	oattG1         = "00000000-0000-7000-9000-00000000f001"
)

type oattRecord struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	GCID       string `json:"gcid"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	RecordedAt string `json:"recorded_at"`
}

type oattListResp struct {
	Records []oattRecord `json:"records"`
}

func newOfferingAttendanceTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:          oRepo,
		OfferingAttendance: repoinmem.NewOfferingAttendanceRepo(),
	})
	return srv, oRepo
}

func postMark(t *testing.T, srv http.Handler, offeringID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringID+"/attendance", []byte(body), instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// getAttendance drives the CANONICAL read route — the session id rides in the
// PATH: GET /api/v1/offerings/{id}/attendance/{sid}. A blank sessionID omits the
// segment (the len==2 GET), which must still 400.
func getAttendance(t *testing.T, srv http.Handler, offeringID, sessionID, role string) (*httptest.ResponseRecorder, oattListResp) {
	t.Helper()
	path := "/api/v1/offerings/" + offeringID + "/attendance"
	if sessionID != "" {
		path += "/" + sessionID
	}
	return doAttendanceGet(t, srv, path, role)
}

// getAttendanceQueryForm drives the RETIRED query-string form
// (GET .../attendance?session_id={sid}). Cloud Armor / OWASP CRS 943110
// ("Session Fixation: SessionID Parameter Name with Off-Domain Referrer") denies
// this at the edge 100% of the time — both halves of the signature are permanent
// facts of this platform (SPA on rplus.chora.site, API on api.chora.site ⇒ the
// Referer is ALWAYS off-domain; the param name literally contains session_id).
// It must therefore be dead: the handler must NOT read the query param.
func getAttendanceQueryForm(t *testing.T, srv http.Handler, offeringID, sessionID, role string) (*httptest.ResponseRecorder, oattListResp) {
	t.Helper()
	path := "/api/v1/offerings/" + offeringID + "/attendance?session_id=" + sessionID
	return doAttendanceGet(t, srv, path, role)
}

func doAttendanceGet(t *testing.T, srv http.Handler, path, role string) (*httptest.ResponseRecorder, oattListResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, path, nil, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp oattListResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

func markBody(session, gcid, status, source string) string {
	return `{"session_id":"` + session + `","gcid":"` + gcid + `","status":"` + status + `","source":"` + source + `"}`
}

// -----------------------------------------------------------------------------

func TestOfferingAttendance_Get_EmptyList(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID) // reuse the short-offering seeder
	w, resp := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Records) != 0 {
		t.Fatalf("records: want 0, got %d", len(resp.Records))
	}
	if !strings.Contains(w.Body.String(), `"records":[]`) {
		t.Fatalf("body must contain records=[], got %s", w.Body.String())
	}
}

func TestOfferingAttendance_Post_MarksThenListed(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)

	w := postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var created oattRecord
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.GCID != oattG1 || created.Status != "present" || created.Source != "manual" {
		t.Fatalf("created mark: %+v", created)
	}

	_, resp := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if len(resp.Records) != 1 || resp.Records[0].GCID != oattG1 {
		t.Fatalf("list after mark: %+v", resp.Records)
	}
}

func TestOfferingAttendance_Post_ReMarkUpsertsStatus(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	// Mark present, then correct to late for the SAME (session, gcid).
	postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))
	postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "late", "manual"))

	_, resp := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if len(resp.Records) != 1 {
		t.Fatalf("re-mark must upsert (one row per learner), got %d: %+v", len(resp.Records), resp.Records)
	}
	if resp.Records[0].Status != "late" {
		t.Fatalf("status: want corrected to late, got %q", resp.Records[0].Status)
	}
}

func TestOfferingAttendance_Get_400WithoutSessionID(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	w, _ := getAttendance(t, srv, oattOfferingID, "", "instructor")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (missing session_id), got %d body=%s", w.Code, w.Body.String())
	}
}

// CHO-2186 — the session id MUST travel as a path segment, not a query param.
//
// These two tests are a PAIR and must be read together. The negative half
// (query form is ignored) is an absence assertion: on its own it would also pass
// if the whole read path were broken. The positive half proves the very same
// seeded mark IS retrievable via the path route, so the 400 below can only mean
// "the query param was genuinely not read".
func TestOfferingAttendance_Get_PathSegment_ReadsSeededMark(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))

	w, resp := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("GET .../attendance/{sid}: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Records) != 1 || resp.Records[0].GCID != oattG1 || resp.Records[0].Status != "present" {
		t.Fatalf("path-segment read must return the seeded mark, got %+v", resp.Records)
	}
}

func TestOfferingAttendance_Get_QueryParamFormIsDead(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	// Same seeded state as the positive test above — that mark IS retrievable.
	postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))

	w, resp := getAttendanceQueryForm(t, srv, oattOfferingID, oattSessionID, "instructor")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("?session_id= must NOT be honoured (it is edge-denied by CRS 943110 and can never reach us in prod): want 400, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Records) != 0 {
		t.Fatalf("query form must return no records, got %+v", resp.Records)
	}
}

func TestOfferingAttendance_405OnPutToPathSegment(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	r := reqWithHeaders(http.MethodPut, "/api/v1/offerings/"+oattOfferingID+"/attendance/"+oattSessionID, []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d", w.Code)
	}
}

func TestOfferingAttendance_403WithoutRole_PathSegment(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	w, _ := getAttendance(t, srv, oattOfferingID, oattSessionID, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET .../attendance/{sid} without role: want 403, got %d", w.Code)
	}
}

func TestOfferingAttendance_Get_404WhenOfferingMissing(t *testing.T) {
	srv, _ := newOfferingAttendanceTestServer(t)
	w, _ := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", w.Code)
	}
}

func TestOfferingAttendance_Post_400OnInvalidStatus(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	w := postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "bogus", "manual"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (invalid status), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingAttendance_Post_400OnInvalidSource(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	w := postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "carrier-pigeon"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (invalid source), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingAttendance_Post_400OnMalformedBody(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	w := postMark(t, srv, oattOfferingID, "instructor", `{nope`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (malformed), got %d", w.Code)
	}
}

func TestOfferingAttendance_403WithoutRole(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	w, _ := getAttendance(t, srv, oattOfferingID, oattSessionID, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET status: want 403, got %d", w.Code)
	}
	wp := postMark(t, srv, oattOfferingID, "", markBody(oattSessionID, oattG1, "present", "manual"))
	if wp.Code != http.StatusForbidden {
		t.Fatalf("POST status: want 403, got %d", wp.Code)
	}
}

func TestOfferingAttendance_405OnPut(t *testing.T) {
	srv, oRepo := newOfferingAttendanceTestServer(t)
	seedScheduleOffering(t, oRepo, oattOfferingID)
	r := reqWithHeaders(http.MethodPut, "/api/v1/offerings/"+oattOfferingID+"/attendance", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d", w.Code)
	}
}

func TestOfferingAttendance_503WhenUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // OfferingAttendance nil
	seedScheduleOffering(t, oRepo, oattOfferingID)
	w, _ := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}
