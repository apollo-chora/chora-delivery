// course_cj2_handler.go — HTTP handlers for the CJ#2 Course state-FSM
// authoring + release flow per
// `chora-contracts/openapi/delivery-courses.yaml`.
//
// 6 endpoints registered in handlers.go:
//
//	POST   /api/v1/courses                — create DRAFT shell (instructor)
//	GET    /api/v1/courses?state=...      — list (R+ admin queue)
//	GET    /api/v1/courses/{id}           — fetch one
//	PATCH  /api/v1/courses/{id}           — update DRAFT content
//	POST   /api/v1/courses/{id}/publish   — DRAFT → AWAITING_REVIEW
//	POST   /api/v1/courses/{id}/release   — AWAITING_REVIEW → PUBLISHED + emit
//	POST   /api/v1/courses/{id}/reject    — AWAITING_REVIEW → DRAFT
//
// Authorisation (verified inside the handler, NOT at middleware):
//   - tenantRequired enforces X-Tenant-Id presence (400 otherwise).
//   - gcid header is required (401 if missing) — Bucket 4 servicemesh
//     propagation guarantees this when the caller's JWT is valid.
//   - create / update / publish: caller is the author OR has `instructor`
//     or `admin` role. Update + publish ALSO require DRAFT state (409
//     otherwise).
//   - release / reject / list?state=AWAITING_REVIEW|DRAFT|ARCHIVED:
//     caller carries `training-admin` role (403 otherwise).
//
// RLS scoping is enforced by the pg adapter (rls.ApplySession) BEFORE
// every query. The handler's per-row visibility check (VisibleToCaller)
// is defence-in-depth.
//
// Per CJ#2 directive row at `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3.
package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/payments"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// CourseCJ2Deps wires the CJ#2 Course port + outbox publisher onto Deps.
// Production wires pg.CourseRepoCJ2Port + the same chora-delivery outbox
// publisher used by the assessments + applications surfaces. Local dev +
// unit tests wire domain.InMemCourseCJ2Store + the in-memory publisher.
type CourseCJ2Deps struct {
	Courses         domain.CourseCJ2Port
	OutboxPublisher events.Publisher

	// Payments — chora-payments PaymentService gRPC client (ADR-164 Stage C,
	// 2026-05-24). Non-nil enables POST /api/v1/courses/{id}/checkout for
	// paid CJ#2 course enrolment. When nil the route returns 501 with a
	// "checkout not configured" error. The async outcome arrives via Pub/Sub
	// (`chora.payments.course_purchase.payment_captured.v1`) and is handled
	// by `events/payments_subscriber.go`. Replaces the inline Stripe-SDK
	// adapter deleted at Stage C.
	Payments *payments.Client

	// Content — CHO-1612 heterogeneous course curriculum. Non-nil enables the
	// /api/v1/courses/{id}/content[/...] subtree (add/list/remove/reorder
	// typed content items: atom/video/youtube/document/live_classroom/
	// assessment). When nil the subtree falls through to 404.
	Content *CourseContentDeps

	// Prerequisites — ADR-226 Course Prerequisite DAG. Non-nil enables the
	// offering-nested /api/v1/offerings/{id}/prerequisites[/remove] surface
	// (author + validate + display the cycle-checked course→course edges).
	// When nil those routes return 503.
	Prerequisites *domain.CoursePrerequisiteService

	// CheckoutSuccessURLTemplate is the absolute URL Stripe redirects to
	// on a successful charge. MUST include the literal placeholder
	// `{COURSE_ID}` (this handler substitutes it) and may include
	// `{CHECKOUT_SESSION_ID}` (Stripe substitutes that at redirect-time).
	// Example: "https://chora.site/a/courses/{COURSE_ID}/enrolled?session_id={CHECKOUT_SESSION_ID}".
	// When empty the route returns 501.
	CheckoutSuccessURLTemplate string

	// CheckoutCancelURLTemplate is the absolute URL Stripe redirects to
	// if the buyer abandons Checkout. MUST include `{COURSE_ID}`. The
	// `?checkout_cancelled=1` query param is FE convention.
	// Example: "https://chora.site/a/courses/{COURSE_ID}?checkout_cancelled=1".
	// When empty the route returns 501.
	CheckoutCancelURLTemplate string
}

// -----------------------------------------------------------------------------
// Request/response types — match chora-contracts/openapi/delivery-courses.yaml
// -----------------------------------------------------------------------------

type createCourseCJ2Req struct {
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	LearningObjectives []string       `json:"learning_objectives"`
	Prerequisites      []string       `json:"prerequisites"`
	TestSetIDs         []string       `json:"test_set_ids"`
	Certification      *courseCertReq `json:"certification,omitempty"`
}

type updateCourseCJ2Req struct {
	Title              *string        `json:"title,omitempty"`
	Description        *string        `json:"description,omitempty"`
	LearningObjectives *[]string      `json:"learning_objectives,omitempty"`
	Prerequisites      *[]string      `json:"prerequisites,omitempty"`
	TestSetIDs         *[]string      `json:"test_set_ids,omitempty"`
	Certification      *courseCertReq `json:"certification,omitempty"`
}

// courseCertReq is the wire shape of the OpenAPI CourseCertification block
// (CHO-1795). RequireAllContent defaults to true unless explicitly false.
type courseCertReq struct {
	Enabled           bool   `json:"enabled"`
	CertType          string `json:"cert_type,omitempty"`
	PassingScorePct   *int   `json:"passing_score_pct,omitempty"`
	RequireAllContent *bool  `json:"require_all_content,omitempty"`
}

// toDomain maps the wire cert block to the domain CertDefinition (validated by
// the aggregate constructor/mutator).
func (c *courseCertReq) toDomain() *domain.CertDefinition {
	if c == nil {
		return nil
	}
	cd := domain.CertDefinition{Enabled: c.Enabled, RequireAllContent: true}
	if c.CertType != "" {
		cd.CertType = domain.CertType(strings.ToUpper(strings.TrimSpace(c.CertType)))
	}
	if c.PassingScorePct != nil {
		cd.PassingScorePct = *c.PassingScorePct
	}
	if c.RequireAllContent != nil {
		cd.RequireAllContent = *c.RequireAllContent
	}
	return &cd
}

type releaseCourseCJ2Req struct {
	PriceSGDCents   int64      `json:"price_sgd_cents"`
	SFEligible      bool       `json:"sf_eligible"`
	InstructorGCIDs []string   `json:"instructor_gcids"`
	ScheduledOpenAt *time.Time `json:"scheduled_open_at,omitempty"`
}

type rejectCourseCJ2Req struct {
	ReviewNotes string `json:"review_notes"`
}

// -----------------------------------------------------------------------------
// /api/v1/courses root dispatcher (POST create + GET list)
// -----------------------------------------------------------------------------

// cj2CoursesRootHandler dispatches /api/v1/courses.
//
// NOTE: this handler is registered separately from /api/v1/instructors/...
// — the chora-delivery service mounts /api/v1/courses ONLY when the CJ#2
// port (CourseCJ2Deps.Courses) is non-nil, so the route is opt-in based
// on cmd/server wiring.
func cj2CoursesRootHandler(cdeps *CourseCJ2Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleCJ2CourseCreate(cdeps, w, r)
		case http.MethodGet:
			handleCJ2CourseList(cdeps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// cj2CoursesSubHandler dispatches /api/v1/courses/{id}[/publish|release|reject].
func cj2CoursesSubHandler(cdeps *CourseCJ2Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/courses/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		courseID := parts[0]

		// CHO-1612 — /api/v1/courses/{id}/content[/reorder|/{item_id}] subtree
		// delegates to the heterogeneous course-content handler.
		if len(parts) >= 2 && parts[1] == "content" && cdeps.Content != nil {
			courseContentHandler(cdeps.Content).ServeHTTP(w, r)
			return
		}

		// /api/v1/courses/{id}/{action}
		if len(parts) == 2 {
			switch parts[1] {
			case "publish":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleCJ2CoursePublish(cdeps, courseID, w, r)
				return
			case "release":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleCJ2CourseRelease(cdeps, courseID, w, r)
				return
			case "reject":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleCJ2CourseReject(cdeps, courseID, w, r)
				return
			case "checkout":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleCJ2CourseCheckout(cdeps, courseID, w, r)
				return
			default:
				writeError(w, http.StatusNotFound, "not found")
				return
			}
		}

		// /api/v1/courses/{id}
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				handleCJ2CourseGet(cdeps, courseID, w, r)
			case http.MethodPatch:
				handleCJ2CourseUpdate(cdeps, courseID, w, r)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}

		writeError(w, http.StatusNotFound, "not found")
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleCJ2CourseCreate(cdeps *CourseCJ2Deps, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin role")
		return
	}
	var req createCourseCJ2Req
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := domain.NewCJ2Course(domain.NewCJ2CourseInput{
		TenantID:           tenantID,
		AuthorGCID:         gcid,
		Title:              req.Title,
		Description:        req.Description,
		LearningObjectives: req.LearningObjectives,
		PrerequisiteNotes:  req.Prerequisites,
		TestSetIDs:         req.TestSetIDs,
		Certification:      req.Certification.toDomain(),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := withTenantAndGCID(r.Context(), tenantID, gcid, meshRolesForRLS(r))
	if err := cdeps.Courses.Save(ctx, c); err != nil {
		writeError(w, http.StatusInternalServerError, "course save failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, courseCJ2DTO(c))
}

func handleCJ2CourseList(cdeps *CourseCJ2Deps, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	stateStr := strings.TrimSpace(r.URL.Query().Get("state"))
	if stateStr == "" {
		writeError(w, http.StatusBadRequest, "state query parameter required")
		return
	}
	state := domain.CourseState(stateStr)
	if !state.IsValid() {
		writeError(w, http.StatusBadRequest, "invalid state: "+stateStr)
		return
	}

	// Visibility gate (ONBOARD-UI F1):
	//   - PUBLISHED                 → any in-tenant caller lists every
	//     published course (unchanged).
	//   - non-PUBLISHED + training-admin/instructor/admin → the FULL review
	//     queue (every course in that state for the tenant) — unchanged.
	//   - non-PUBLISHED + author (ADR-182 A+ Creator) → ONLY the courses the
	//     caller authored, regardless of lifecycle state. The previous gate
	//     ignored authorship and 403'd every non-admin, so an author could
	//     never list their own DRAFT / AWAITING_REVIEW courses.
	//   - anyone else (learner / no role) → 403 (preserved).
	// Authorship scoping happens at the QUERY level (ListByStateAndAuthor) so
	// pagination stays correct and another author's non-PUBLISHED courses
	// never leak. RLS keeps every branch tenant-scoped — no cross-tenant
	// broadening.
	adminQueue := state == domain.CourseStatePublished || hasTrainingAdminRole(r)
	ownAuthor := !adminQueue && hasAuthorRole(r)
	if !adminQueue && !ownAuthor {
		writeError(w, http.StatusForbidden,
			"caller lacks training-admin or author role for non-PUBLISHED state")
		return
	}

	page, per, perr := parseCJ2Pagination(r)
	if perr != nil {
		writeError(w, http.StatusUnprocessableEntity, perr.Error())
		return
	}
	offset := (page - 1) * per
	// R+ course entity-picker search (kills the single-page course dropdown):
	// additive, backward-compatible — `state` stays required, `q` narrows by
	// title. Mirrors chora-creation's atom ?q= pattern (commit 85b2bae14).
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	ctx := withTenantAndGCID(r.Context(), tenantID, gcid, meshRolesForRLS(r))

	var (
		items []*domain.Course
		err   error
	)
	if ownAuthor {
		items, err = cdeps.Courses.ListByStateAndAuthor(ctx, tenantID, state, gcid, query, offset, per)
	} else {
		items, err = cdeps.Courses.ListByState(ctx, tenantID, state, query, offset, per)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "course list failed: "+err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, c := range items {
		out = append(out, courseCJ2DTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
	})
}

func handleCJ2CourseGet(cdeps *CourseCJ2Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	ctx := withTenantAndGCID(r.Context(), tenantID, gcid, meshRolesForRLS(r))
	c, ok, err := cdeps.Courses.Get(ctx, tenantID, courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "course read failed: "+err.Error())
		return
	}
	if !ok || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	if !c.VisibleToCaller(gcid, roleSet(r)) {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	writeJSON(w, http.StatusOK, courseCJ2DTO(c))
}

func handleCJ2CourseUpdate(cdeps *CourseCJ2Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	ctx := withTenantAndGCID(r.Context(), tenantID, gcid, meshRolesForRLS(r))
	c, ok, err := cdeps.Courses.Get(ctx, tenantID, courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "course read failed: "+err.Error())
		return
	}
	if !ok || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	// RBAC: caller is author OR has instructor role.
	if c.AuthorGCID != gcid && !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller is not author and lacks instructor role")
		return
	}
	var req updateCourseCJ2Req
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	update := domain.UpdateCourseInput{
		Title:       req.Title,
		Description: req.Description,
	}
	if req.LearningObjectives != nil {
		update.LearningObjectives = *req.LearningObjectives
	}
	if req.Prerequisites != nil {
		update.PrerequisiteNotes = *req.Prerequisites
	}
	if req.TestSetIDs != nil {
		update.TestSetIDs = *req.TestSetIDs
	}
	if req.Certification != nil {
		update.Certification = req.Certification.toDomain()
	}
	if err := c.UpdateDraftContent(update); err != nil {
		if errors.Is(err, domain.ErrCourseNotDraft) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cdeps.Courses.Save(ctx, c); err != nil {
		writeError(w, http.StatusInternalServerError, "course save failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, courseCJ2DTO(c))
}

func handleCJ2CoursePublish(cdeps *CourseCJ2Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	ctx := withTenantAndGCID(r.Context(), tenantID, gcid, meshRolesForRLS(r))
	c, ok, err := cdeps.Courses.Get(ctx, tenantID, courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "course read failed: "+err.Error())
		return
	}
	if !ok || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	if c.AuthorGCID != gcid && !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller is not author and lacks instructor role")
		return
	}
	if err := c.Publish(); err != nil {
		if errors.Is(err, domain.ErrCourseNotDraft) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cdeps.Courses.Save(ctx, c); err != nil {
		writeError(w, http.StatusInternalServerError, "course save failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, courseCJ2DTO(c))
}

func handleCJ2CourseRelease(cdeps *CourseCJ2Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasTrainingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks training-admin role")
		return
	}
	ctx := withTenantAndGCID(r.Context(), tenantID, gcid, meshRolesForRLS(r))
	c, ok, err := cdeps.Courses.Get(ctx, tenantID, courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "course read failed: "+err.Error())
		return
	}
	if !ok || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	var req releaseCourseCJ2Req
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := c.Release(domain.ReleaseInput{
		PriceSGDCents:   req.PriceSGDCents,
		SFEligible:      req.SFEligible,
		InstructorGCIDs: req.InstructorGCIDs,
		ScheduledOpenAt: req.ScheduledOpenAt,
	}); err != nil {
		if errors.Is(err, domain.ErrCourseNotAwaitingReview) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cdeps.Courses.Save(ctx, c); err != nil {
		writeError(w, http.StatusInternalServerError, "course save failed: "+err.Error())
		return
	}
	// Best-effort emit chora.delivery.course.released.v1 — fire-and-forget;
	// outbox dispatcher handles delivery durability (Tier 2 D6).
	emitCourseReleased(cdeps.OutboxPublisher, c, r)
	writeJSON(w, http.StatusOK, courseCJ2DTO(c))
}

func handleCJ2CourseReject(cdeps *CourseCJ2Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasTrainingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks training-admin role")
		return
	}
	ctx := withTenantAndGCID(r.Context(), tenantID, gcid, meshRolesForRLS(r))
	c, ok, err := cdeps.Courses.Get(ctx, tenantID, courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "course read failed: "+err.Error())
		return
	}
	if !ok || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	var req rejectCourseCJ2Req
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := c.Reject(req.ReviewNotes); err != nil {
		if errors.Is(err, domain.ErrCourseNotAwaitingReview) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cdeps.Courses.Save(ctx, c); err != nil {
		writeError(w, http.StatusInternalServerError, "course save failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, courseCJ2DTO(c))
}

// -----------------------------------------------------------------------------
// RBAC helpers
// -----------------------------------------------------------------------------

// hasInstructorOrAdmin reports whether the caller carries an instructor-level
// role: instructor, admin, or the training-admin label in either shape. The
// CANONICAL Identity token is `training_admin` (underscore — what the gateway
// mint stamps, mint_handler.go TrainingAdminRole); the hyphen constant is the
// legacy assessment-surface shape kept for parity with the sibling dual-shape
// gates (exam / live-quiz / offering / classroom). CHO-2233: the canonical
// arm was missing here, a dead branch masked live only because identity
// always pairs training_admin with instructor.
func hasInstructorOrAdmin(r *http.Request) bool {
	return callerHasMeshRole(r, roleInstructor, roleAdmin, roleTrainingAdmin)
}

// hasAuthorRole reports whether the caller carries the ADR-182 `author`
// (A+ Creator) typed role. Authors can author + manage their OWN courses but
// are NOT review-queue admins, so the training-admin list scopes them to their
// own authored courses. Token + spelling live in mesh_roles.go.
func hasAuthorRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleAuthor)
}

// hasTrainingAdminRole reports whether the caller carries the
// `training-admin` (or `admin`) typed role.
//
// Per chora-identity's adminRoles map (search_tenant_members_handler.go:
// 262-269) + the ADR-141 reconciliation, the schema enum `instructor`
// maps onto the contract role `TRAINING_ADMIN` (instructors who manage
// cohort assessments are the canonical admin callers). We accept BOTH:
//   - canonical: `training-admin` / `TRAINING_ADMIN` / `admin` / `TENANT_ADMIN`
//   - legacy:    `instructor` / `INSTRUCTOR` (schema enum → TRAINING_ADMIN)
//
// Case-insensitive parse.
func hasTrainingAdminRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleTrainingAdmin, roleAdmin, roleTenantAdmin, roleInstructor)
}

// roleSet parses the `x-mesh-user-roles` header into a lowercased set.
func roleSet(r *http.Request) map[string]bool {
	out := make(map[string]bool)
	roles := strings.ToLower(r.Header.Get("x-mesh-user-roles"))
	for _, role := range strings.Split(roles, ",") {
		role = strings.TrimSpace(role)
		if role != "" {
			out[role] = true
		}
	}
	return out
}

// withTenantAndGCID returns a context with tenant_id + gcid + roles bound — the
// pg adapter's rls.ApplySession reads these to SET LOCAL chora.tenant_id +
// chora.user_gcid + chora.user_roles before user queries. The roles GUC powers
// the state-aware RLS on `courses` (B2.4): a caller sees PUBLISHED courses, their
// OWN authored courses (any state), and — when admin/training-admin — every
// course; a bare learner/instructor cannot read another author's draft even if an
// app WHERE clause omits the filter.
func withTenantAndGCID(ctx context.Context, tenantID, gcid, roles string) context.Context {
	ctx = tracing.WithTenantID(ctx, tenantID)
	if gcid != "" {
		ctx = tracing.WithGCID(ctx, gcid)
	}
	if roles != "" {
		ctx = tracing.WithUserRoles(ctx, roles)
	}
	return ctx
}

// meshRolesForRLS extracts the caller's mesh role set as a sanitised, lowercase,
// comma-joined token list safe for a SET LOCAL (rls.ValidateUserRoles): only
// [a-z0-9-_] tokens survive, spaces are dropped. Empty when no roles header.
func meshRolesForRLS(r *http.Request) string {
	raw := strings.ToLower(strings.TrimSpace(r.Header.Get("x-mesh-user-roles")))
	if raw == "" {
		return ""
	}
	clean := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		ok := true
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				ok = false
				break
			}
		}
		if ok {
			clean = append(clean, p)
		}
	}
	return strings.Join(clean, ",")
}

// parseCJ2Pagination — page + per parsing with sane defaults.
func parseCJ2Pagination(r *http.Request) (page, per int, err error) {
	page = 1
	per = 20
	q := r.URL.Query()
	if v := q.Get("page"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 {
			return 0, 0, errPaginationInvalid("page must be a positive integer")
		}
		page = n
	}
	if v := q.Get("page_size"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 100 {
			return 0, 0, errPaginationInvalid("page_size must be in [1,100]")
		}
		per = n
	}
	return page, per, nil
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

func courseCJ2DTO(c *domain.Course) map[string]interface{} {
	out := map[string]interface{}{
		"id":                  c.ID,
		"tenant_id":           c.TenantID,
		"title":               c.Title,
		"description":         c.Description,
		"state":               string(c.State),
		"author_gcid":         c.AuthorGCID,
		"learning_objectives": defaultedSlice(c.LearningObjectives),
		"prerequisites":       defaultedSlice(c.PrerequisiteNotes),
		"test_set_ids":        defaultedSlice(c.TestSetIDs),
		"instructor_gcids":    defaultedSlice(c.InstructorGCIDs),
		"price_sgd_cents":     c.PriceSGDCents,
		"sf_eligible":         c.SFEligible,
		"created_at":          c.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":          c.UpdatedAt.Format(time.RFC3339Nano),
	}
	if c.ScheduledOpenAt != nil {
		out["scheduled_open_at"] = c.ScheduledOpenAt.Format(time.RFC3339Nano)
	}
	if c.ReviewNotes != "" {
		out["review_notes"] = c.ReviewNotes
	}
	if c.PublishedAt != nil {
		out["published_at"] = c.PublishedAt.Format(time.RFC3339Nano)
	}
	// CHO-1795 — certification block (always emitted; enabled=false ⇒ no cert).
	cert := map[string]interface{}{
		"enabled":             c.Certification.Enabled,
		"require_all_content": c.Certification.RequireAllContent,
	}
	if c.Certification.CertType != "" {
		cert["cert_type"] = string(c.Certification.CertType)
	}
	if c.Certification.Enabled {
		cert["passing_score_pct"] = c.Certification.PassingScorePct
	}
	out["certification"] = cert
	return out
}

func defaultedSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// -----------------------------------------------------------------------------
// Event emission — chora.delivery.course.released.v1
// -----------------------------------------------------------------------------

func emitCourseReleased(pub events.Publisher, c *domain.Course, r *http.Request) {
	if pub == nil || c == nil {
		return
	}
	publishedAt := time.Now().UTC()
	if c.PublishedAt != nil {
		publishedAt = *c.PublishedAt
	}
	payload := map[string]any{
		"course_id":        c.ID,
		"title":            c.Title,
		"author_gcid":      c.AuthorGCID,
		"price_sgd_cents":  c.PriceSGDCents,
		"sf_eligible":      c.SFEligible,
		"instructor_gcids": c.InstructorGCIDs,
		"test_set_ids":     c.TestSetIDs,
		"released_at":      publishedAt,
	}
	if c.ScheduledOpenAt != nil {
		payload["scheduled_open_at"] = *c.ScheduledOpenAt
	}
	publishCourseEvent(pub, "chora.delivery.course.released.v1",
		c.TenantID, c.AuthorGCID, payload)
}

// publishCourseEvent — thin shim mirroring publishCustomEvent in
// assessment_handler.go so we can publish via the generic PublishCustom
// API without coupling to a specific publisher implementation. The
// InMemoryPublisher captures it for tests; CloudPublisher pipes through
// the outbox dispatcher.
func publishCourseEvent(pub events.Publisher, topic, tenantID, gcid string, payload map[string]any) {
	type publishCustomer interface {
		PublishCustom(topic, tenantID, gcid string, payload map[string]any) (events.PublishedEvent, error)
	}
	if pc, ok := pub.(publishCustomer); ok {
		if _, err := pc.PublishCustom(topic, tenantID, gcid, payload); err != nil {
			log.Printf("delivery: PublishCustom topic=%s err=%v", topic, err)
		}
	}
}

// -----------------------------------------------------------------------------
// CJ#2 Stripe Checkout (paid course enrolment) — CJ#2 extension 2026-05-24
//
// POST /api/v1/courses/{id}/checkout
//
// Validates the course is PUBLISHED + paid (price_sgd_cents > 0), then
// calls the Stripe Checkout Session API with course price + success/
// cancel URLs + metadata (course_id + learner_gcid). Returns the hosted
// Checkout URL the FE redirects to via window.location.
//
// The webhook handler (separate file / route) consumes the
// `checkout.session.completed` event and creates the Enrolment row
// using the metadata attribution.
// -----------------------------------------------------------------------------

type courseCheckoutResp struct {
	CheckoutURL string `json:"checkout_url"`
	SessionID   string `json:"session_id"`
}

func handleCJ2CourseCheckout(cdeps *CourseCJ2Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	// Wiring guards — surface a 501 with an actionable error so an
	// unwired prod deploy fails loud rather than silently 404ing.
	//
	// Per ADR-164 Stage C (2026-05-24): inline Stripe SDK calls have been
	// retired in favour of the canonical chora-payments PaymentService gRPC.
	// CourseCJ2Deps.Payments is the typed adapter wrapping the upstream
	// gRPC client; nil means cmd/server bootstrap did not wire the dial.
	if cdeps.Payments == nil {
		writeError(w, http.StatusNotImplemented,
			"course checkout not configured (CourseCJ2Deps.Payments is nil)")
		return
	}
	if strings.TrimSpace(cdeps.CheckoutSuccessURLTemplate) == "" ||
		strings.TrimSpace(cdeps.CheckoutCancelURLTemplate) == "" {
		writeError(w, http.StatusNotImplemented,
			"course checkout not configured (success/cancel URL templates empty)")
		return
	}

	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if strings.TrimSpace(gcid) == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required")
		return
	}

	ctx := withTenantAndGCID(r.Context(), tenantID, gcid, meshRolesForRLS(r))
	c, ok, err := cdeps.Courses.Get(ctx, tenantID, courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "course read failed: "+err.Error())
		return
	}
	if !ok || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	if !c.VisibleToCaller(gcid, roleSet(r)) {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	if c.State != domain.CourseStatePublished {
		writeError(w, http.StatusConflict,
			"course is not PUBLISHED — only PUBLISHED courses can be paid for")
		return
	}
	if c.PriceSGDCents <= 0 {
		writeError(w, http.StatusConflict,
			"course is free — use POST /api/courses/{id}/enrol instead")
		return
	}

	successURL := strings.ReplaceAll(cdeps.CheckoutSuccessURLTemplate, "{COURSE_ID}", courseID)
	cancelURL := strings.ReplaceAll(cdeps.CheckoutCancelURLTemplate, "{COURSE_ID}", courseID)

	// chora-payments PaymentService gRPC — mint the Checkout Session.
	// The adapter forwards a deterministic idempotency key derived from
	// (course_id, learner_gcid) when we leave the field blank, so a
	// double-clicked Enrol button within Stripe's 24h window returns the
	// same Session.
	out, err := cdeps.Payments.CreateCourseCheckoutSession(ctx, payments.CreateCourseCheckoutInput{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		CourseID:    courseID,
		AmountCents: c.PriceSGDCents,
		Currency:    "SGD",
		SuccessURL:  successURL,
		CancelURL:   cancelURL,
	})
	if err != nil {
		log.Printf("delivery: payments checkout create failed course_id=%s gcid=%s err=%v",
			courseID, gcid, err)
		// Wrap as 502 — upstream (chora-payments / Stripe) failed; don't
		// leak raw error to caller.
		writeError(w, http.StatusBadGateway, "payments checkout session creation failed")
		return
	}

	log.Printf("delivery: payments checkout session created course_id=%s gcid=%s session_id=%s purchase_id=%s",
		courseID, gcid, out.StripeSessionID, out.PurchaseID)

	writeJSON(w, http.StatusOK, courseCheckoutResp{
		CheckoutURL: out.StripeCheckoutURL,
		SessionID:   out.StripeSessionID,
	})
}
