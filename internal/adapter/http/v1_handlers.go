// V1 path-prefix handlers — S4.3 consolidation.
//
// All routes consolidate under /v1/ for platform consistency. Legacy paths
// continue to respond for one release window with a Deprecation header
// (per .claude/rules/git-workflow.md).
//
// Endpoints (all /v1/):
//
//	POST   /v1/courses                                  create
//	GET    /v1/courses                                  list (Relay cursor)
//	GET    /v1/courses/{id}                             detail
//	PATCH  /v1/courses/{id}                             update metadata + visibility
//	POST   /v1/courses/{id}/enrolments                  same-identity enrol
//	DELETE /v1/courses/{id}/enrolments/{enrolment_id}   cancel
//	GET    /v1/me/enrolments                            learner's enrolments
//	POST   /v1/campus                                   create campus (BE-CO2)
//	GET    /v1/campus/{id}                              get campus
package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Deprecation middleware (legacy routes only)
// -----------------------------------------------------------------------------

// deprecated wraps a handler to emit RFC 8594 deprecation headers. The new
// path is recorded in `Link: rel="successor-version"`. Used by all the
// pre-/v1/ routes that the S4.3 brief consolidates.
func deprecated(oldPath, newPath string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Link", `<`+newPath+`>; rel="successor-version"`)
		w.Header().Set("Sunset", time.Now().AddDate(0, 1, 0).UTC().Format(http.TimeFormat))
		next(w, r)
	}
}

// -----------------------------------------------------------------------------
// V1 courses dispatcher
// -----------------------------------------------------------------------------

// v1CoursesHandler dispatches GET (list) + POST (create) on /v1/courses.
func v1CoursesHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleV1CourseList(deps, w, r)
		case http.MethodPost:
			handleV1CourseCreate(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// v1CoursesSubHandler dispatches:
//   - GET    /v1/courses/{id}                                detail
//   - PATCH  /v1/courses/{id}                                update
//   - POST   /v1/courses/{id}/enrolments                     enrol
//   - DELETE /v1/courses/{id}/enrolments/{enrolment_id}      cancel
func v1CoursesSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/courses/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		courseID := parts[0]

		// /v1/courses/{id}/enrolments[/{enrolment_id}]
		if len(parts) >= 2 && parts[1] == "enrolments" {
			if len(parts) == 2 {
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleV1EnrolmentCreate(deps, courseID, w, r)
				return
			}
			if len(parts) == 3 {
				if r.Method != http.MethodDelete {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleV1EnrolmentCancel(deps, courseID, parts[2], w, r)
				return
			}
			// POST /v1/courses/{id}/enrolments/{enrolment_id}/complete —
			// instructor/admin marks the enrolment complete (drives
			// chora.delivery.enrollment.completed.v1).
			if len(parts) == 4 && parts[3] == "complete" {
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleV1EnrolmentComplete(deps, courseID, parts[2], w, r)
				return
			}
		}

		// S6.1 — /v1/courses/{id}/application-form
		if len(parts) == 2 && parts[1] == "application-form" {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleApplicationForm(deps, courseID, w, r)
			return
		}

		// /v1/courses/{id}
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				handleV1CourseDetail(deps, courseID, w, r)
			case http.MethodPatch:
				handleV1CourseUpdate(deps, courseID, w, r)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	}
}

// -----------------------------------------------------------------------------
// V1 course handlers
// -----------------------------------------------------------------------------

type v1CreateCourseReq struct {
	Title           string   `json:"title"`
	SyllabusOutline []string `json:"syllabus_outline"`
	Tags            []string `json:"tags"`
	PriceSGDCents   int32    `json:"price_sgd_cents"`
	Visibility      string   `json:"visibility"`
	Public          bool     `json:"public"` // legacy alias
	SFEligible      bool     `json:"sf_eligible"`
	InstructorName  string   `json:"instructor_name"`
}

type v1PatchCourseReq struct {
	Title           *string  `json:"title"`
	Visibility      *string  `json:"visibility"`
	Tags            []string `json:"tags"`
	SyllabusOutline []string `json:"syllabus_outline"`
	PriceSGDCents   *int32   `json:"price_sgd_cents"`
	SFEligible      *bool    `json:"sf_eligible"`
	InstructorName  *string  `json:"instructor_name"`
}

func handleV1CourseCreate(deps Deps, w http.ResponseWriter, r *http.Request) {
	gcid := strings.TrimSpace(r.Header.Get("gcid"))
	if gcid == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required (instructor identity)")
		return
	}
	var req v1CreateCourseReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	visibility := domain.Visibility(req.Visibility)

	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:        r.Header.Get("X-Tenant-Id"),
		Title:           req.Title,
		InstructorGCID:  gcid,
		InstructorName:  req.InstructorName,
		PriceSGDCents:   req.PriceSGDCents,
		Visibility:      visibility,
		Public:          req.Public,
		SFEligible:      req.SFEligible,
		Tags:            req.Tags,
		SyllabusOutline: req.SyllabusOutline,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deps.Catalogue.Save(r.Context(), pc); err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue write failed")
		return
	}

	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishCourseCreated(events.CourseCreated{
			TenantID:       pc.TenantID,
			GCID:           gcid,
			CourseID:       pc.ID,
			Title:          pc.Title,
			InstructorGCID: gcid,
			Public:         pc.Public,
			PriceSGDCents:  pc.PriceSGDCents,
			Traceparent:    r.Header.Get("traceparent"),
		})
		// Course created with `public` visibility → also emit course.published
		// (chora-sharing subscribes for the discovery feed).
		if pc.Visibility == domain.VisibilityPublic {
			_, _ = deps.Publisher.PublishCoursePublished(events.CoursePublished{
				TenantID:       pc.TenantID,
				GCID:           gcid,
				CourseID:       pc.ID,
				Title:          pc.Title,
				InstructorGCID: gcid,
				Traceparent:    r.Header.Get("traceparent"),
			})
		}
	}
	writeJSON(w, http.StatusCreated, v1CourseDTO(pc, 0))
}

func handleV1CourseList(deps Deps, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	visibility := domain.VisibilityFilterAny
	switch q.Get("visibility") {
	case "public":
		visibility = domain.VisibilityFilterPublic
	case "tenant_or_public":
		visibility = domain.VisibilityFilterTenantOrPublic
	}
	first := 20
	if v := q.Get("first"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 200 {
			first = n
		}
	}
	// S6.1 paid=true filter — the Course Application UX A-CRS-1 catalog filters
	// to paid-only courses (IsFree() == false). Performed at the HTTP layer
	// since the catalogue index doesn't natively split free/paid.
	paidOnly := q.Get("paid") == "true"

	page, err := deps.Catalogue.SearchCursor(r.Context(), domain.CatalogueQuery{
		TenantID:    r.Header.Get("X-Tenant-Id"),
		Visibility:  visibility,
		Q:           q.Get("q"),
		First:       first,
		AfterCursor: q.Get("after"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue read failed")
		return
	}

	items := make([]map[string]interface{}, 0, len(page.Items))
	skipped := 0
	for _, pc := range page.Items {
		if paidOnly && pc.IsFree() {
			skipped++
			continue
		}
		// Tenant must land in ctx for pg.EnrollmentRepo's RLS read.
		countCtx := tracing.WithTenantID(r.Context(), pc.TenantID)
		n, _ := deps.Enrollments.CountByCourse(countCtx, pc.TenantID, pc.ID)
		count := int32(n)
		items = append(items, v1CourseDTO(pc, count))
	}
	total := page.Total
	if paidOnly {
		total -= skipped
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
		"page_info": map[string]interface{}{
			"has_next_page": page.HasNextPage,
			"end_cursor":    page.EndCursor,
		},
		"total": total,
	})
}

func handleV1CourseDetail(deps Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	pc, ok, err := deps.Catalogue.Get(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue read failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	// Cross-tenant access is allowed for VisibilityPublic; tenant_only +
	// private must match the active tenant.
	if pc.Visibility != domain.VisibilityPublic && pc.TenantID != r.Header.Get("X-Tenant-Id") {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	// Tenant must land in ctx for pg.EnrollmentRepo's RLS read.
	countCtx := tracing.WithTenantID(r.Context(), pc.TenantID)
	n, _ := deps.Enrollments.CountByCourse(countCtx, pc.TenantID, pc.ID)
	count := int32(n)
	writeJSON(w, http.StatusOK, v1CourseDTO(pc, count))
}

func handleV1CourseUpdate(deps Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	pc, ok, err := deps.Catalogue.Get(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue read failed")
		return
	}
	if !ok || pc.TenantID != r.Header.Get("X-Tenant-Id") {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	var req v1PatchCourseReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	publishedSignal := false
	if req.Title != nil {
		t := strings.TrimSpace(*req.Title)
		if t == "" {
			writeError(w, http.StatusBadRequest, "title cannot be empty")
			return
		}
		pc.Title = t
	}
	if req.InstructorName != nil {
		pc.InstructorName = *req.InstructorName
	}
	if req.Tags != nil {
		pc.Tags = append([]string(nil), req.Tags...)
	}
	if req.SyllabusOutline != nil {
		pc.SyllabusOutline = append([]string(nil), req.SyllabusOutline...)
	}
	if req.PriceSGDCents != nil {
		if *req.PriceSGDCents < 0 {
			writeError(w, http.StatusBadRequest, "price_sgd_cents must be >= 0")
			return
		}
		pc.PriceSGDCents = *req.PriceSGDCents
	}
	if req.SFEligible != nil {
		pc.SFEligible = *req.SFEligible
	}
	if req.Visibility != nil {
		nextV := domain.Visibility(*req.Visibility)
		if !nextV.IsValid() {
			writeError(w, http.StatusBadRequest, "visibility must be one of private|tenant_only|public")
			return
		}
		publishedSignal = pc.SetVisibility(nextV)
	}
	pc.UpdatedAt = time.Now().UTC()
	if err := deps.Catalogue.Save(r.Context(), pc); err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue write failed")
		return
	}

	// CHO-2247: the course.updated.v1 publish that used to sit here was DELETED.
	// It aimed at a topic that has never existed (gcloud topics describe →
	// NOT_FOUND; absent from chora-infra/topics/topics.yaml) and emitted zero
	// events in its lifetime (delivery outbox count 0, all time) — while
	// discarding its own error via `_, _ =`, so the failure was invisible. The
	// course_directory projection is fed by course.released.v1 (the CJ#2 FSM
	// terminus); this catalogue lane is dead per the CHO-2247 owner ruling.
	//
	// course.published.v1 below is LIVE and stays: topic exists and
	// chora-sharing.delivery-course-published consumes it.
	if deps.Publisher != nil && publishedSignal {
		if _, err := deps.Publisher.PublishCoursePublished(events.CoursePublished{
			TenantID:       pc.TenantID,
			GCID:           r.Header.Get("gcid"),
			CourseID:       pc.ID,
			Title:          pc.Title,
			InstructorGCID: pc.InstructorGCID,
			Traceparent:    r.Header.Get("traceparent"),
		}); err != nil {
			// Fail LOUD, don't fail the request: the visibility flip is already
			// durable and the outbox owns redelivery. This publish previously
			// swallowed its error via `_, _ =` — on a lane that has already
			// dead-lettered once (outbox course.published: 1 deadlettered).
			log.Printf("delivery: PublishCoursePublished course=%s tenant=%s FAILED: %v",
				pc.ID, pc.TenantID, err)
		}
	}

	// Tenant must land in ctx for pg.EnrollmentRepo's RLS read.
	countCtx := tracing.WithTenantID(r.Context(), pc.TenantID)
	n, _ := deps.Enrollments.CountByCourse(countCtx, pc.TenantID, pc.ID)
	count := int32(n)
	writeJSON(w, http.StatusOK, v1CourseDTO(pc, count))
}

// -----------------------------------------------------------------------------
// V1 enrolment handlers
// -----------------------------------------------------------------------------

type v1CreateEnrolmentReq struct {
	GCID string `json:"gcid"`
}

func handleV1EnrolmentCreate(deps Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	var req v1CreateEnrolmentReq
	if r.ContentLength > 0 {
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	gcid := strings.TrimSpace(req.GCID)
	if gcid == "" {
		gcid = strings.TrimSpace(r.Header.Get("gcid"))
	}
	if gcid == "" {
		writeError(w, http.StatusUnauthorized, "gcid required (body or header)")
		return
	}
	tenantID := r.Header.Get("X-Tenant-Id")

	// Resolve the course — cross-tenant lookup allowed for public courses
	// (Phyllis enrolling in Mr. Chen's CSM-Prep across tenants).
	pc, ok, err := deps.Catalogue.Get(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue read failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	if pc.Visibility != domain.VisibilityPublic && pc.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}

	// The enrolment row is recorded against the LEARNER'S tenant (so
	// /v1/me/enrolments returns it for that learner under that tenant
	// context). For cross-tenant public enrol we record under the
	// learner's tenant.
	//
	// Decorate ctx with tenant_id so pg.EnrollmentRepo's
	// rls.ApplySession picks it up via tracing.TenantIDFromContext.
	ctx := tracing.WithTenantID(r.Context(), tenantID)

	// ADR-226 §4 — block self-enrol while a hard_gate prerequisite is unmet.
	// Same-tenant only: a course's prerequisite edges + the learner's completion
	// facts must share a tenant (cross-tenant public enrol, pc.TenantID !=
	// tenantID, is a tracked follow-up — not gated here).
	if pc.TenantID == tenantID {
		unmet, gerr := unmetEnrolHardGates(deps, ctx, tenantID, courseID, gcid)
		if gerr != nil {
			writeError(w, http.StatusInternalServerError, "prerequisite check failed")
			return
		}
		if len(unmet) > 0 {
			writeEnrolHardGateBlocked(w, unmet)
			return
		}
	}

	_, existed, err := deps.Enrollments.GetByCourseAndGCID(ctx, tenantID, courseID, gcid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "enrollment lookup failed")
		return
	}
	e, err := deps.Enrollments.Register(ctx, tenantID, courseID, gcid)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	status := http.StatusOK
	if !existed {
		// Bump the public enrolled counter — same for the course owner's
		// tenant and for a cross-tenant public enrol. Save persists it back
		// to chora_delivery; a write failure here is non-fatal to the
		// enrolment itself (the enrolment row is already registered), so we
		// log-and-continue rather than fail the request.
		pc.IncrementEnrolled()
		_ = deps.Catalogue.Save(r.Context(), pc)
		if deps.Publisher != nil {
			_, _ = deps.Publisher.PublishEnrollmentCreated(events.EnrollmentCreated{
				TenantID:     tenantID,
				GCID:         gcid,
				EnrollmentID: e.ID,
				CourseID:     e.CourseID,
				LearnerGCID:  gcid,
				Traceparent:  r.Header.Get("traceparent"),
			})
		}
		status = http.StatusCreated
	}
	writeJSON(w, status, enrollmentDTO(e))
}

func handleV1EnrolmentCancel(deps Deps, courseID, enrolmentID string, w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Tenant-Id")
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	e, ok, err := deps.Enrollments.Get(ctx, enrolmentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "enrolment lookup failed")
		return
	}
	if !ok || e.CourseID != courseID || e.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "enrolment not found")
		return
	}
	// Persist the soft-delete via the port. Mutating the loaded aggregate
	// (e.SoftDelete()) is a silent no-op on Postgres — Get materialises a fresh
	// struct, so the mutation is never written back unless it goes through the
	// port's Cancel UPDATE. Fail loud on error rather than reporting a 204 for
	// a cancel that never happened.
	if err := deps.Enrollments.Cancel(ctx, tenantID, e.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "enrolment cancel failed")
		return
	}

	// Emit the cancellation event only AFTER the row is soft-deleted, so a
	// failed Cancel never announces a phantom cancellation to subscribers.
	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishEnrollmentCancelled(events.EnrollmentCancelled{
			TenantID:     e.TenantID,
			GCID:         e.GCID,
			EnrollmentID: e.ID,
			CourseID:     e.CourseID,
			LearnerGCID:  e.GCID,
			Reason:       "user-initiated",
			Traceparent:  r.Header.Get("traceparent"),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// /v1/campus  + /v1/campus/{id}
// -----------------------------------------------------------------------------

type v1CreateCampusReq struct {
	Name      string `json:"name"`
	AddressL1 string `json:"address_l1"`
	AddressL2 string `json:"address_l2"`
	City      string `json:"city"`
	Country   string `json:"country"`
}

func campusHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req v1CreateCampusReq
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		c, err := campusops.NewCampus(campusops.NewCampusInput{
			TenantID:  r.Header.Get("X-Tenant-Id"),
			Name:      req.Name,
			AddressL1: req.AddressL1,
			AddressL2: req.AddressL2,
			City:      req.City,
			Country:   req.Country,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// CHO-2293 fail-loud: a failed durable write must NEVER report 201.
		// tenant into the CONTEXT (not just the header) or rls.ApplySession
		// fails with ErrNoTenantContext before any SQL runs.
		sctx := tracing.WithTenantID(r.Context(), r.Header.Get("X-Tenant-Id"))
		if err := deps.CampusOps.Save(sctx, c); err != nil {
			log.Printf("campus create failed: tenant=%s err=%v", r.Header.Get("X-Tenant-Id"), err)
			writeError(w, http.StatusInternalServerError, "failed to persist campus")
			return
		}
		writeJSON(w, http.StatusCreated, campusDTO(c))
	}
}

func campusByIDHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/campus/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		// CHO-2293 / CHO-2184 fail-loud: an infra or RLS failure must NOT read as
		// an absent row. ok=false with a nil error is a GENUINE miss (404); a
		// non-nil error is a 5xx.
		tenantID := r.Header.Get("X-Tenant-Id")
		gctx := tracing.WithTenantID(r.Context(), tenantID)
		c, ok, err := deps.CampusOps.GetForTenant(gctx, tenantID, id)
		if err != nil {
			log.Printf("campus get failed: tenant=%s id=%s err=%v", tenantID, id, err)
			writeError(w, http.StatusInternalServerError, "failed to load campus")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "campus not found")
			return
		}
		writeJSON(w, http.StatusOK, campusDTO(c))
	}
}

// -----------------------------------------------------------------------------
// DTOs
// -----------------------------------------------------------------------------

func v1CourseDTO(pc *domain.PublicCourse, enrolledCount int32) map[string]interface{} {
	out := map[string]interface{}{
		"id":                     pc.ID,
		"tenant_id":              pc.TenantID,
		"title":                  pc.Title,
		"instructor_gcid":        pc.InstructorGCID,
		"instructor_name":        pc.InstructorName,
		"price_sgd_cents":        pc.PriceSGDCents,
		"is_free":                pc.IsFree(),
		"public":                 pc.Public,
		"visibility":             string(pc.Visibility),
		"sf_eligible":            pc.SFEligible,
		"tags":                   pc.Tags,
		"syllabus_outline_count": pc.SyllabusOutlineCount(),
		"enrolled_count":         enrolledCount,
		"created_at":             pc.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":             pc.UpdatedAt.Format(time.RFC3339Nano),
	}
	if pc.Tags == nil {
		out["tags"] = []string{}
	}
	if pc.DeletedAt != nil {
		out["deleted_at"] = pc.DeletedAt.Format(time.RFC3339Nano)
	}
	return out
}

func campusDTO(c *campusops.Campus) map[string]interface{} {
	out := map[string]interface{}{
		"id":         c.ID,
		"tenant_id":  c.TenantID,
		"name":       c.Name,
		"address_l1": c.AddressL1,
		"address_l2": c.AddressL2,
		"city":       c.City,
		"country":    c.Country,
		"created_at": c.CreatedAt.Format(time.RFC3339Nano),
		"updated_at": c.UpdatedAt.Format(time.RFC3339Nano),
	}
	if c.DeletedAt != nil {
		out["deleted_at"] = c.DeletedAt.Format(time.RFC3339Nano)
	}
	return out
}

// -----------------------------------------------------------------------------
// Misc helper — kept here to avoid a circular import with the legacy file.
// -----------------------------------------------------------------------------

// (json import is brought in by handlers.go; this stub keeps the linter
// happy if the file is built in isolation.)
var _ = json.Marshal
