// offering_certification_handler.go — HTTP handler for the W2.D offering-nested
// Certification surface: a READ-ONLY view of the certification CONFIG that
// applies to an Offering's attached courses.
//
// Endpoint (dispatched from offeringsSubHandler in offering_handler.go):
//
//	GET /api/v1/offerings/{id}/certification  — this offering's attached-course
//	                                            certification config (object-derived tab)
//
// Intra-chora_delivery only (Offering + CJ#2 Course share the DB per
// ddd-enforcement #3) — NO cross-DB query, NO new aggregate, NO migration,
// NO new event. The cert config is the CertDefinition VALUE OBJECT carried on
// the CJ#2 Course aggregate (course_cert.go); it is read via the SAME
// deps.CourseCJ2.Courses port the Curriculum surface uses (the offering-create
// course-id space). This is config (what cert a course awards on completion) —
// NOT issuance: it touches NO Certification credential, transcript,
// CompletionRequirement, or chora_consumption data.
//
// A course with no enabled cert definition awards no certificate → an EMPTY
// cert list (200, certifications: []), not an error.
//
// Authorisation mirrors the Curriculum + Assessments surfaces: instructor /
// training-admin (hasInstructorRole) + X-Tenant-Id + gcid (callerTenantGCID) —
// role-driven VISIBILITY, R+ (Rhythm+) audience. Read-only (GET); the
// dispatcher 405s any other method.
package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// handleOfferingGetCertification — GET /api/v1/offerings/{id}/certification.
//
// Steps:
//
//	(a) verify wiring (offerings repo + CJ#2 course port);
//	(b) enforce tenant (400) + gcid (401) + instructor/admin role (403);
//	(c) load the offering for this tenant (404 if missing / cross-tenant /
//	    soft-deleted), copying the guard from offering_curriculum_handler.go;
//	(d) for each o.CourseIDs entry resolve the course title + its cert config
//	    (CertDefinition on the CJ#2 Course), preserving offering order — a
//	    disabled/absent definition yields an empty cert list (not an error);
//	(e) write a hand-built snake_case DTO
//	    { "courses": [ { "id", "title", "certifications":[{enabled,cert_type,
//	      passing_score_pct,require_all_content}] } ] }.
func handleOfferingGetCertification(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	// (a) Wiring — fail-loud 503 (no stub).
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.CourseCJ2 == nil || deps.CourseCJ2.Courses == nil {
		writeError(w, http.StatusServiceUnavailable, "course port not wired")
		return
	}
	// (b) Identity + RBAC (enforces tenant 400 + gcid 401; then role 403).
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	// (c) Offering must exist for this tenant (same-DB soft FK; no Go FK).
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	// (d) Resolve each attached course's title + cert config (offering order
	//     preserved). Both reads are intra-chora_delivery.
	courses := make([]map[string]interface{}, 0, len(o.CourseIDs))
	for _, courseID := range o.CourseIDs {
		title := ""
		// At most one CertDefinition per course; emit a list so the wire can
		// model 0..N uniformly and degrade an absent/disabled cert to [].
		certs := make([]map[string]interface{}, 0, 1)
		c, found, err := deps.CourseCJ2.Courses.Get(ctx, tenantID, courseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "course lookup failed: "+err.Error())
			return
		}
		if found && c != nil {
			title = c.Title
			// A disabled/zero CertDefinition means the course awards no
			// certificate → NO cert config applies (empty list, not an error).
			if c.Certification.Enabled {
				certs = append(certs, offeringCertEntry(c.Certification))
			}
		}
		courses = append(courses, map[string]interface{}{
			"id":             courseID,
			"title":          title,
			"certifications": certs,
		})
	}
	// (e) Hand-built snake_case wire DTO — the read-only per-course cert config
	// PLUS the editable offering-level completion policy (S2), when set, PLUS
	// the DECLARED completion requirement (CHO-2222), when declared. The
	// requirement is written via PATCH /completion-requirement but READ here, so
	// the Certification tab loads its whole state in one round-trip. Both are
	// omitted when unset rather than rendered null, mirroring each other.
	resp := map[string]interface{}{"courses": courses}
	if o.CompletionPolicy != nil {
		resp["completion_policy"] = offeringCompletionPolicyDTO(o.CompletionPolicy)
	}
	if o.CompletionRequirement != nil {
		resp["completion_requirement"] = offeringCompletionRequirementDTO(o.CompletionRequirement)
	}
	writeJSON(w, http.StatusOK, resp)
}

// offeringCertEntry renders an enabled CertDefinition as a snake_case config
// row, mirroring courseCJ2DTO's certification block (course_cj2_handler.go).
// Read-only config — there is no issuance here.
func offeringCertEntry(cd delivery.CertDefinition) map[string]interface{} {
	return map[string]interface{}{
		"enabled":             cd.Enabled,
		"cert_type":           string(cd.CertType),
		"passing_score_pct":   cd.PassingScorePct,
		"require_all_content": cd.RequireAllContent,
	}
}

// -----------------------------------------------------------------------------
// R+ Phase-2 S2 — offering-level completion policy (make the Certification tab
// actionable). The per-course cert config above stays read-only (authored in A+
// while the course is DRAFT); THIS is the editable delivery policy for the
// offering ("cert = policy on top"). PATCH-only ⇒ edge-safe (Armor rule 998).
// -----------------------------------------------------------------------------

// setCompletionPolicyReq is the PATCH /certification body.
type setCompletionPolicyReq struct {
	AwardsCertificate bool   `json:"awards_certificate"`
	PassingScorePct   int    `json:"passing_score_pct"`
	CertTitle         string `json:"cert_title"`
}

// handleOfferingSetCompletionPolicy — PATCH /api/v1/offerings/{id}/certification.
func handleOfferingSetCompletionPolicy(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	var req setCompletionPolicyReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := o.SetCompletionPolicy(delivery.SetCompletionPolicyInput{
		AwardsCertificate: req.AwardsCertificate,
		PassingScorePct:   req.PassingScorePct,
		CertTitle:         req.CertTitle,
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error()) // score out of [0,100]
		return
	}
	if err := deps.Offerings.Save(ctx, o); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offeringCompletionPolicyDTO(o.CompletionPolicy))
}

// offeringCompletionPolicyDTO renders the offering completion policy (snake_case).
func offeringCompletionPolicyDTO(p *delivery.CompletionPolicy) map[string]interface{} {
	return map[string]interface{}{
		"awards_certificate": p.AwardsCertificate,
		"passing_score_pct":  p.PassingScorePct,
		"cert_title":         p.CertTitle,
		"updated_at":         p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// -----------------------------------------------------------------------------
// R+ per-offering MANUAL certificate issuance.
//
// The WIRED lane already exists (CompletionSubscriber consumes
// submission.released.v1, evaluates the offering CompletionPolicy, and calls
// Certifications.IssueCtx anchored on the offering's primary course). THIS is
// the MANUAL admin lane for the same offering: an explicit "issue this learner
// their certificate now" action. It reuses the same durable store + event
// shape as the legacy POST /api/certifications, but ADDS the role gate that
// route never had, is TENANT-SCOPED, and validates the course anchor against
// the offering (a provided course_id must be one of the offering's attached
// courses; absent ⇒ the primary course). Intra-chora_delivery — no cross-DB
// query, no new aggregate, no migration.
// -----------------------------------------------------------------------------

// issueOfferingCertReq is the POST /certification/issue body.
type issueOfferingCertReq struct {
	// LearnerGCID is the certificate RECIPIENT (required).
	LearnerGCID string `json:"learner_gcid"`
	// CourseID optionally anchors the cert on a SPECIFIC attached course; when
	// blank the offering's primary course is used. A provided value MUST be one
	// of the offering's attached courses (400 otherwise).
	CourseID string `json:"course_id"`
}

// handleOfferingIssueCertification — POST /api/v1/offerings/{id}/certification/issue.
//
// Steps:
//
//	(a) verify wiring (offerings repo + certification store) — fail-loud 503;
//	(b) enforce tenant (400) + gcid (401) + offering-admin role (403) — the role
//	    gate the legacy manual route lacks;
//	(c) load the offering for this tenant (404 if missing / cross-tenant /
//	    soft-deleted, same guard as handleOfferingGetCertification);
//	(d) decode { learner_gcid (required), course_id (optional) };
//	(e) resolve the course anchor: a provided course_id validated ∈
//	    offering.CourseIDs (400 otherwise), else the primary course;
//	(f) issue via Certifications.IssueCtx (nil score — manual issue records
//	    none; accomplishments from the offering's CompletionPolicy) — a duplicate
//	    (course_id, gcid) is refused by the DB with ErrCertAlreadyIssued → 409;
//	(g) emit certification.issued.v1 (same publisher/shape as the manual handler
//	    in handlers.go), then 201 with a small DTO { certification_id, course_id,
//	    gcid }.
func handleOfferingIssueCertification(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	// (a) Wiring — fail-loud 503 (no stub).
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.Certifications == nil {
		writeError(w, http.StatusServiceUnavailable, "certifications store not wired")
		return
	}
	// (b) Identity + RBAC (enforces tenant 400 + gcid 401; then role 403). The
	//     role gate closes the gap that the legacy POST /api/certifications has
	//     NO role check at all.
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	// (c) Offering must exist for this tenant (same-DB soft FK; no Go FK).
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	// (d) Decode body; learner_gcid is required.
	var req issueOfferingCertReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	learnerGCID := strings.TrimSpace(req.LearnerGCID)
	if learnerGCID == "" {
		writeError(w, http.StatusBadRequest, "learner_gcid required")
		return
	}
	// (e) Resolve the course anchor. A provided course_id must belong to this
	//     offering; absent ⇒ the primary (first) course.
	courseID := o.PrimaryCourseID()
	if provided := strings.TrimSpace(req.CourseID); provided != "" {
		if !offeringHasCourse(o, provided) {
			writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
			return
		}
		courseID = provided
	}
	// (f) Issue. nil score — a manual issue records none (mirrors the legacy
	//     manual handler); accomplishments come from the offering's policy. The
	//     DB's UNIQUE(course_id, gcid) refuses a duplicate → ErrCertAlreadyIssued.
	accomplishments := delivery.CertificateAccomplishments(o.CompletionPolicy)
	cert, err := deps.Certifications.IssueCtx(ctx, tenantID, learnerGCID, courseID, nil, accomplishments)
	if err != nil {
		if errors.Is(err, delivery.ErrCertAlreadyIssued) {
			writeError(w, http.StatusConflict, "learner already holds a certificate for this course")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// (g) Emit certification.issued.v1 — same publisher + event shape as the
	//     manual handler in handlers.go (the transcript projection consumes it).
	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishCertificationIssued(events.CertificationIssued{
			TenantID:        cert.TenantID,
			GCID:            cert.LearnerID,
			CertificationID: cert.ID,
			CourseID:        cert.CourseID,
			LearnerGCID:     cert.LearnerID,
			Hash:            cert.Hash,
			Traceparent:     r.Header.Get("traceparent"),
		})
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"certification_id": cert.ID,
		"course_id":        cert.CourseID,
		"gcid":             cert.LearnerID,
	})
}
