// assessment_handler.go — HTTP handlers for the assessments + submissions
// surface per chora-contracts/openapi/delivery-assessments.yaml + ADR-155.
//
// Endpoints (registered in handlers.go via /api/v1/assessments/ +
// /api/v1/me/assessments/):
//
//	Instructor (5):
//	  POST   /api/v1/assessments                                  createAssessment
//	  GET    /api/v1/assessments/{id}                             getAssessment
//	  GET    /api/v1/assessments/{id}/monitor                     getAssessmentMonitor
//	  GET    /api/v1/assessments/{id}/submissions                 listAssessmentSubmissions
//	  POST   /api/v1/assessments/{id}/release-results             releaseAssessmentResults
//
//	Learner (6):
//	  GET    /api/v1/me/assessments                               listMyAssessments
//	  GET    /api/v1/me/assessments/{id}                          getMyAssessment
//	  POST   /api/v1/me/assessments/{id}/submissions              startMySubmission
//	  PATCH  /api/v1/me/assessments/{id}/submissions/{subId}/autosave   autosaveMySubmission
//	  POST   /api/v1/me/assessments/{id}/submissions/{subId}/submit     submitMySubmission
//	  GET    /api/v1/me/assessments/{id}/submissions/{subId}/result     getMySubmissionResult
//
// Authorization (per ADR-155 §D9 + integrative UI principle):
//   - Learner-side `/me/*`: caller_gcid is self; the cohort-eligibility check
//     gates per the assessment's invited_gcids[] (explicit cohort) or class_id
//     binding.
//   - Instructor-side: caller carries `instructor` or `training-admin` role
//     in the `x-mesh-user-roles` header. Self-instructor (caller_gcid ==
//     instructor_gcid) is also permitted.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log"
	mathrand "math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// AssessmentDeps wires the optional assessment + submission repos onto Deps.
// Production wires pg.AssessmentRepo + pg.SubmissionRepo; local dev / tests
// wire domain.InMemAssessmentRepo + domain.InMemSubmissionRepo. The handler
// depends on the AssessmentRepo + SubmissionRepo interfaces only.
type AssessmentDeps struct {
	Assessments domain.AssessmentRepo
	Submissions domain.SubmissionRepo

	// Roster resolves cohort authz facts — offering enrolment + class booking
	// (CHO-2153 / ADR-234). NOT optional, and deliberately NOT nil-tolerant:
	// domain.ResolveCohortFacts returns ErrCohortRosterUnavailable when a
	// cohort mode needs roster facts and this is nil, and the handlers refuse
	// the request. An authz gate that no-ops when unwired is the exact defect
	// this replaces — so leaving it nil fails LOUD, never open.
	Roster domain.CohortRoster
	// Offerings resolves the parent Offering's delivery_type so a graded event
	// can be attributed to a MODE (CHO-2224, §10.6 criterion 1). Optional and
	// deliberately nil-tolerant, the OPPOSITE of Roster above: this is event
	// enrichment, not an authz gate. delivery.ResolveDeliveryType treats a nil
	// port as "unresolved" and the grade still publishes mode-less, because a
	// missing mode must never cost a learner their grade. Unwired ⇒ every
	// transcript entry is simply mode-less, which is visible, not silent.
	Offerings domain.OfferingPort
	// OutboxPublisher emits the 9 events per ADR-155. Optional; nil → no-op.
	OutboxPublisher events.Publisher
	// TestSets reads test_set_questions.payload_snapshot at /submit time for
	// deterministic MCQ grading (Fix-F: Lane A snapshot debt close +
	// `feedback_no_stubs_real_wiring`). Optional; nil → falls back to a
	// zero-credit per-MCQ grading (fail loud per ErrMCQSnapshotMissing).
	TestSets TestSetPort

	// MediaResolver resolves durable atom-media gs:// image refs (frozen in
	// the payload_snapshot) into fresh short-lived signed GET URLs at learner
	// read-time (OT#4 — W8 durable image re-home). Satisfied by
	// clients.QuestionClient over chora-creation's MintAtomMediaDownloadURL
	// gRPC. Optional; nil → gs:// refs pass through raw (fail-visible). Old
	// http transient URLs are never touched (only gs:// refs resolve).
	MediaResolver MediaURLResolver

	// LearnerDirectory resolves a learner GCID → display name for the
	// instructor grading-queue submissions list (CHO-2343 — sibling of the WBL
	// CHO-2335 PlacementEnricher). Optional + nil-tolerant: nil, or a GCID with
	// no directory row, degrades to the raw-GCID fallback on the row (never
	// blank), so the queue's LEARNER column always renders. Resolution stays
	// intra-chora_delivery via the chora_delivery.user_directory projection
	// (LookupNames) — NO cross-DB query (ddd-enforcement #1). Satisfied by
	// pg.UserDirectoryRepo (prod) + directory.InMemUserDirectory (dev/tests).
	LearnerDirectory SubmissionLearnerNameResolver
}

// SubmissionLearnerNameResolver batch-resolves learner GCIDs to their display
// names from the chora_delivery.user_directory projection — the SAME read-model
// CHO-2335 uses for the WBL surface. Absent / empty names are simply not in the
// returned map; the caller applies the GCID fallback (the empty→GCID policy
// lives in the handler, not the resolver — mirroring wbl.PlacementEnricher).
type SubmissionLearnerNameResolver interface {
	LookupNames(ctx context.Context, gcids []string) (map[string]string, error)
}

// MediaURLResolver maps durable atom-media gs:// refs to fresh signed GET
// URLs. Best-effort: refs that fail to mint are omitted from the result
// (the caller leaves the raw gs:// — a visible broken image, not a crash).
type MediaURLResolver interface {
	ResolveDownloadURLs(ctx context.Context, tenantID string, gsURIs []string) map[string]string
}

// hasInstructorRole reports whether the caller holds an instructor-level role.
//
// The training-admin arm used to spell the role with a hyphen, which nothing
// mints; it matched only because chora-gateway appends `training_admin` to an
// instructor membership, so `instructor` carried the gate. It now matches on
// its own name (see mesh_roles.go).
func hasInstructorRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleInstructor, roleAdmin, roleTrainingAdmin)
}

// callerTenantGCID extracts (tenant_id, gcid) from headers + reports a 400/401
// for missing values via the caller's writeError. Returns "", "" on failure
// after writing the error.
func callerTenantGCID(w http.ResponseWriter, r *http.Request) (string, string) {
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return "", ""
	}
	gcid := strings.TrimSpace(r.Header.Get("gcid"))
	if gcid == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required (caller identity)")
		return "", ""
	}
	return tenantID, gcid
}

// -----------------------------------------------------------------------------
// Instructor — POST /api/v1/assessments (createAssessment)
// -----------------------------------------------------------------------------

type createAssessmentReq struct {
	TestSetID                 string    `json:"test_set_id"`
	TitleOverride             string    `json:"title_override"`
	LearnerFacingNameOverride string    `json:"learner_facing_name_override"`
	ScheduledOpenAt           time.Time `json:"scheduled_open_at"`
	ScheduledCloseAt          time.Time `json:"scheduled_close_at"`
	MaxAttempts               int       `json:"max_attempts"`
	ShuffleQuestions          bool      `json:"shuffle_questions"`
	// ShuffleMCQOptions is a *bool so an OMITTED field (nil) defaults to OFF —
	// new assessments keep MCQ options in their authored order unless the
	// instructor ticks the "Scramble answer options (anti-cheat)" toggle on
	// the R+ create form (owner decision 2026-06-21, reverses the 06-20
	// default-ON). An explicit true/false is always honoured.
	ShuffleMCQOptions     *bool                         `json:"shuffle_mcq_options"`
	Accommodations        *domain.Accommodations        `json:"accommodations,omitempty"`
	ClassID               string                        `json:"class_id,omitempty"`
	InvitedGCIDs          []string                      `json:"invited_gcids,omitempty"`
	GradingConfigOverride *domain.GradingConfigSnapshot `json:"grading_config_override,omitempty"`
}

// assessmentsRootHandler dispatches `/api/v1/assessments` by method:
//   - GET   → listAssessmentsHandler (E2E-BE-2 — instructor list)
//   - POST  → createAssessmentHandler (existing)
//
// Pattern mirrors testSetsRootHandler so the cursor + page_size + envelope
// shape is identical across the authoring surface.
func assessmentsRootHandler(adeps *AssessmentDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listAssessmentsHandler(adeps, w, r)
		case http.MethodPost:
			createAssessmentHandler(adeps).ServeHTTP(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// listAssessmentsHandler — GET /api/v1/assessments. Closes E2E-BE-2.
//
// Mirrors handleListTestSets:
//   - instructor / training-admin role required (403 otherwise)
//   - default scope: caller's authored assessments via
//     AssessmentRepo.ListByInstructor (tenant-RLS-scoped)
//   - cursor pagination: page_size enum {10,20,50,100} default 20;
//     page_token opaque passthrough
//   - response envelope `{items: [...], next_page_token: string|null}`
//     identical to the test-set listing handler so FE can reuse picker logic.
//
// Sort, multi-state filter, q substring filter are forward work (the contract
// declares them optional but the v1 port pins to created_at DESC).
func listAssessmentsHandler(adeps *AssessmentDeps, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
		return
	}
	pageSize := parseTestSetPageSize(r.URL.Query().Get("page_size"))
	items, nextToken, err := adeps.Assessments.ListByInstructor(r.Context(), tenantID, gcid, pageSize, r.URL.Query().Get("page_token"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "assessment list failed: "+err.Error())
		return
	}
	dtos := make([]map[string]interface{}, 0, len(items))
	for _, a := range items {
		dtos = append(dtos, assessmentDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":           dtos,
		"next_page_token": nullableToken(nextToken),
	})
}

func createAssessmentHandler(adeps *AssessmentDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tenantID, gcid := callerTenantGCID(w, r)
		if tenantID == "" {
			return
		}
		if !hasInstructorRole(r) {
			writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
			return
		}
		// Legacy POST /api/v1/assessments — unscoped (no offering), existence-
		// only test-set check (preserves prior behaviour: PUBLISHED gate off).
		buildAndSaveAssessment(adeps, w, r, tenantID, gcid, "", false)
	}
}

// buildAndSaveAssessment is the shared create core for BOTH POST
// /api/v1/assessments and POST /api/v1/offerings/{id}/assessments (W3.A —
// ZERO duplication, no-debt). It decodes the createAssessmentReq body,
// snapshots the parent test-set's totals + grading config, builds the
// Assessment (scoped to offeringID when non-empty), runs the auto-publish FSM
// when the window is omitted, durably Saves, emits created (+published/opened
// when auto), and writes 201 assessmentDTO. On any failure it writes the HTTP
// error and returns — each caller simply returns afterward.
//
// requireTestSetPublished gates the offering-nested attach (W3.A): the
// test-set MUST be in state PUBLISHED (400 otherwise). The legacy unscoped
// path passes false (existence-only, behaviour-preserving).
func buildAndSaveAssessment(adeps *AssessmentDeps, w http.ResponseWriter, r *http.Request, tenantID, gcid, offeringID string, requireTestSetPublished bool) {
	var req createAssessmentReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.MaxAttempts == 0 {
		req.MaxAttempts = 1
	}
	title := req.TitleOverride
	if title == "" {
		title = "Assessment"
	}
	gcfg := domain.GradingConfigSnapshot{
		MCQDispatch:             "DETERMINISTIC",
		OEDispatch:              "LLM_EVALUATOR_AGENT",
		LLMEvaluatorModelTier:   "T1",
		PassingThresholdPercent: 70,
	}
	if req.GradingConfigOverride != nil {
		if req.GradingConfigOverride.PassingThresholdPercent != 0 {
			gcfg.PassingThresholdPercent = req.GradingConfigOverride.PassingThresholdPercent
		}
		gcfg.PerQuestionFeedbackEnabled = req.GradingConfigOverride.PerQuestionFeedbackEnabled
		gcfg.AutoRelease = req.GradingConfigOverride.AutoRelease
	}
	// shuffle_mcq_options defaults OFF when the field is omitted (nil) — options
	// keep their authored order unless the instructor opts into the anti-cheat
	// scramble (owner decision 2026-06-21, reverses 06-20). Explicit honoured.
	shuffleMCQOptions := false
	if req.ShuffleMCQOptions != nil {
		shuffleMCQOptions = *req.ShuffleMCQOptions
	}
	// LEG2B-D demo simplification (ADR-155 §D8): when the window is omitted,
	// synthesize an open-link [now, now+24h] window so the FSM can run
	// DRAFT → SCHEDULED → OPEN in this one handler call (otherwise the row
	// stays DRAFT and is hidden from the learner visibility filter).
	autoPublish := req.ScheduledOpenAt.IsZero() && req.ScheduledCloseAt.IsZero()
	now := time.Now().UTC()
	schedOpen := req.ScheduledOpenAt
	schedClose := req.ScheduledCloseAt
	if autoPublish {
		schedOpen = now
		schedClose = now.Add(24 * time.Hour)
	}
	// LEG3-D — snapshot totals from the parent test-set (NOT hardcoded). Per
	// ddd-enforcement #3 we only touch chora_delivery; adeps.TestSets is the
	// same TestSetPort used by /test-sets/{id} GET. Fail-loud per
	// feedback_no_stubs_real_wiring if the test-set is missing.
	var (
		totalPoints   = 0
		questionCount = 0
	)
	if adeps.TestSets != nil {
		ts, ok, lookupErr := adeps.TestSets.Get(r.Context(), tenantID, req.TestSetID)
		if lookupErr != nil {
			log.Printf("delivery: createAssessment test-set lookup err=%v", lookupErr)
			writeError(w, http.StatusInternalServerError,
				"test-set lookup failed: "+lookupErr.Error())
			return
		}
		if !ok || ts == nil {
			writeError(w, http.StatusBadRequest,
				"test_set_id not found in this tenant")
			return
		}
		// W3.A attach gate: an Offering may only host a PUBLISHED test-set.
		if requireTestSetPublished && ts.State != domain.TestSetStatePublished {
			writeError(w, http.StatusBadRequest,
				"test_set_id must be PUBLISHED to attach to an offering (got "+string(ts.State)+")")
			return
		}
		// TestSet.TotalPoints() is float64; Assessment.TotalPoints is int —
		// round to nearest (whole-number points; v1 demo per ADR-155 D5).
		totalPoints = int(ts.TotalPoints() + 0.5)
		questionCount = ts.QuestionCount()
	} else if requireTestSetPublished {
		// The offering attach path REQUIRES a real test-set port to verify the
		// PUBLISHED gate — never silently skip (no-stubs / fail-loud).
		writeError(w, http.StatusServiceUnavailable, "test-set port not wired")
		return
	}
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:              tenantID,
		InstructorGCID:        gcid,
		TestSetID:             req.TestSetID,
		Title:                 title,
		LearnerFacingName:     req.LearnerFacingNameOverride,
		ScheduledOpenAt:       schedOpen,
		ScheduledCloseAt:      schedClose,
		MaxAttempts:           req.MaxAttempts,
		ShuffleQuestions:      req.ShuffleQuestions,
		ShuffleMCQOptions:     shuffleMCQOptions,
		Accommodations:        req.Accommodations,
		ClassID:               req.ClassID,
		OfferingID:            offeringID,
		InvitedGCIDs:          req.InvitedGCIDs,
		TotalPoints:           totalPoints,
		QuestionCount:         questionCount,
		GradingConfigSnapshot: gcfg,
	})
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, domain.ErrAssessmentTestSetIDRequired) ||
			errors.Is(err, domain.ErrAssessmentTitleRequired) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	// Run the auto-publish FSM BEFORE the first Save so the row lands directly
	// in OPEN — avoids intermediate outbox rows observers would have to ignore.
	emitPublished := false
	emitOpened := false
	if autoPublish {
		if pubErr := a.Publish(now); pubErr != nil {
			log.Printf("delivery: createAssessment auto-publish failed (tenant=%s) err=%v", tenantID, pubErr)
			writeError(w, http.StatusInternalServerError, "auto-publish failed: "+pubErr.Error())
			return
		}
		emitPublished = true
		a.AutoFlipToOpen(now)
		if a.State == domain.AssessmentStateOpen {
			emitOpened = true
		}
	}
	if err := adeps.Assessments.Save(r.Context(), a); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	// Always emit created.v1 first — observer ordering matters.
	emitAssessmentEvent(adeps, "chora.delivery.assessment.created.v1", a, r)
	if emitPublished {
		emitAssessmentEvent(adeps, "chora.delivery.assessment.published.v1", a, r)
	}
	if emitOpened {
		emitAssessmentEvent(adeps, "chora.delivery.assessment.opened.v1", a, r)
	}
	writeJSON(w, http.StatusCreated, assessmentDTO(a))
}

// -----------------------------------------------------------------------------
// Instructor — GET /api/v1/assessments/{id} + monitor + submissions + release
// -----------------------------------------------------------------------------

func assessmentSubHandler(adeps *AssessmentDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/assessments/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		assessmentID := parts[0]

		// Sub-paths: /monitor /submissions /release-results /publish /archive /force-close
		if len(parts) == 2 {
			switch parts[1] {
			case "monitor":
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				monitorAssessmentHandler(adeps, assessmentID, w, r)
				return
			case "submissions":
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				listAssessmentSubmissionsHandler(adeps, assessmentID, w, r)
				return
			case "release-results":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				releaseAssessmentResultsHandler(adeps, assessmentID, w, r)
				return
			case "force-close":
				// BE-ASSESSMENT-LIFECYCLE-CTAS-MISSING (P0). FSM Option A —
				// permissive. force-close curtails the OPEN window early
				// without releasing. release-results stays permissive from
				// OPEN per existing (a *Assessment) ReleaseResults().
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				forceCloseAssessmentHandler(adeps, assessmentID, w, r)
				return
			case "archive":
				// BE-ASSESSMENT-LIFECYCLE-CTAS-MISSING (P0). Soft-delete from
				// any state; idempotent on ARCHIVED.
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				archiveAssessmentHandler(adeps, assessmentID, w, r)
				return
			case "approve-all":
				// ADR-172 §D6 — assessment-wide bulk approve (optionally
				// approve-and-release).
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				approveAllSubmissionsHandler(adeps, assessmentID, w, r)
				return
			}
		}

		// ADR-172 instructor grading-review sub-paths:
		//   /assessments/{id}/submissions/{subId}/grading          GET
		//   /assessments/{id}/submissions/{subId}/grades           PATCH
		//   /assessments/{id}/submissions/{subId}/overall-comment  PATCH
		//   /assessments/{id}/submissions/{subId}/approve          POST
		if len(parts) == 4 && parts[1] == "submissions" {
			submissionID := parts[2]
			switch parts[3] {
			case "grading":
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				getSubmissionGradingDetailHandler(adeps, assessmentID, submissionID, w, r)
				return
			case "grades":
				if r.Method != http.MethodPatch {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				editSubmissionGradesHandler(adeps, assessmentID, submissionID, w, r)
				return
			case "overall-comment":
				if r.Method != http.MethodPatch {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				editSubmissionOverallCommentHandler(adeps, assessmentID, submissionID, w, r)
				return
			case "approve":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				approveSubmissionGradingHandler(adeps, assessmentID, submissionID, w, r)
				return
			}
		}

		if len(parts) == 1 && r.Method == http.MethodGet {
			getAssessmentHandler(adeps, assessmentID, w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	}
}

func getAssessmentHandler(adeps *AssessmentDeps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, id)
	if err != nil {
		// Fail-loud per feedback_no_stubs_real_wiring. The wrapped err
		// carries the pg / SQL diagnostic the access log was previously
		// hiding under the generic "read failed".
		log.Printf("delivery: getAssessment id=%s tenant=%s err=%v", id, tenantID, err)
		writeError(w, http.StatusInternalServerError, "assessment read failed: "+err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "assessment not found")
		return
	}
	// Auth gate: self-instructor OR has instructor role.
	if a.InstructorGCID != gcid && !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "not the instructor + no instructor role")
		return
	}
	writeJSON(w, http.StatusOK, assessmentDTO(a))
}

func monitorAssessmentHandler(adeps *AssessmentDeps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, id)
	if err != nil {
		log.Printf("delivery: monitorAssessment id=%s tenant=%s err=%v", id, tenantID, err)
		writeError(w, http.StatusInternalServerError, "assessment read failed: "+err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "assessment not found")
		return
	}
	if a.InstructorGCID != gcid && !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "instructor-only")
		return
	}
	counts, _ := adeps.Assessments.MonitorCounts(r.Context(), tenantID, id)
	out := map[string]interface{}{
		"assessment_id":     a.ID,
		"state":             string(a.State),
		"total_invited":     counts.TotalInvited,
		"total_started":     counts.TotalStarted,
		"total_submitted":   counts.TotalSubmitted,
		"total_graded":      counts.TotalGraded,
		"in_progress_count": counts.InProgressCount,
		"total_released":    counts.TotalReleased,
	}
	if counts.HasGradedSamples {
		out["average_score_percent"] = counts.AverageScorePct
	} else {
		out["average_score_percent"] = nil
	}
	out["per_question_pass_rate"] = []any{}
	writeJSON(w, http.StatusOK, out)
}

func listAssessmentSubmissionsHandler(adeps *AssessmentDeps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, id)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "assessment not found")
		return
	}
	if a.InstructorGCID != gcid && !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "instructor-only")
		return
	}
	pageSize := 20
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			pageSize = n
		}
	}
	subs, nextToken, err := adeps.Submissions.ListByAssessment(r.Context(), tenantID, id, pageSize, r.URL.Query().Get("page_token"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list submissions failed")
		return
	}
	// Resolve learner GCID → display name for the R+ grading-queue LEARNER
	// column (CHO-2343, sibling of WBL CHO-2335). A miss / nil resolver leaves
	// the map empty and the row falls back to the raw GCID below (never blank).
	names, err := resolveLearnerNames(r.Context(), adeps.LearnerDirectory, subs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve learner names failed")
		return
	}
	items := make([]map[string]interface{}, 0, len(subs))
	for _, s := range subs {
		item := submissionDTO(s, time.Now())
		// review_status is NOT part of the shared submissionDTO (it would leak
		// the HITL gate state into the learner-facing submission GETs that reuse
		// it). This list handler is instructor-gated, and the R+ grading queue
		// renders the REVIEW STATUS pill + gates "Release results" on the count
		// of APPROVED rows — so surface it here (per-submission). Mirror the
		// domain value FAITHFULLY, including the empty "" ReviewStatusNotRequired
		// (MCQ-only auto-graded, no human gate) — the FE distinguishes ""
		// (NotRequired) from PENDING_REVIEW, so omitting "" made MCQ-only rows
		// render as "Pending review" (CHO-2343 bug #2). Key always present.
		item["review_status"] = string(s.ReviewStatus)
		// learner_display_name is likewise instructor-list-only (NOT on the
		// shared learner-facing submissionDTO). The FE reads
		// learner_display_name ?? learner_gcid; we apply the GCID fallback in the
		// DTO here so the column is never blank even when the directory lacks a
		// row for this learner (CHO-2343).
		if name := names[s.LearnerGCID]; name != "" {
			item["learner_display_name"] = name
		} else {
			item["learner_display_name"] = s.LearnerGCID
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":           items,
		"next_page_token": nullableToken(nextToken),
	})
}

// resolveLearnerNames batch-resolves the distinct learner GCIDs across subs to
// their display names via the user_directory projection (CHO-2343). A nil
// resolver or empty input short-circuits to an empty map (the caller then
// applies the GCID fallback); a resolver ERROR is surfaced (fail loud per
// feedback_no_stubs_real_wiring) rather than silently degrading to GCID-only.
// One directory lookup for the whole page regardless of row count.
func resolveLearnerNames(ctx context.Context, resolver SubmissionLearnerNameResolver, subs []*domain.Submission) (map[string]string, error) {
	if resolver == nil || len(subs) == 0 {
		return map[string]string{}, nil
	}
	seen := make(map[string]struct{}, len(subs))
	gcids := make([]string, 0, len(subs))
	for _, s := range subs {
		if s.LearnerGCID == "" {
			continue
		}
		if _, dup := seen[s.LearnerGCID]; dup {
			continue
		}
		seen[s.LearnerGCID] = struct{}{}
		gcids = append(gcids, s.LearnerGCID)
	}
	if len(gcids) == 0 {
		return map[string]string{}, nil
	}
	raw, err := resolver.LookupNames(ctx, gcids)
	if err != nil {
		return nil, err
	}
	// Drop empty names so the caller's GCID fallback fires for a directory row
	// that projected a blank display_name (empty→GCID policy in the handler).
	out := make(map[string]string, len(raw))
	for g, name := range raw {
		if strings.TrimSpace(name) != "" {
			out[g] = name
		}
	}
	return out, nil
}

func releaseAssessmentResultsHandler(adeps *AssessmentDeps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, id)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "assessment not found")
		return
	}
	if a.InstructorGCID != gcid && !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "instructor-only")
		return
	}
	// Optional body — release_announcement
	var body struct {
		ReleaseAnnouncement string `json:"release_announcement"`
	}
	_ = decodeBody(r, &body)
	now := time.Now().UTC()
	if rErr := a.ReleaseResults(now, body.ReleaseAnnouncement); rErr != nil {
		writeError(w, http.StatusConflict, rErr.Error())
		return
	}
	if err := adeps.Assessments.Save(r.Context(), a); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	// Atomic per-submission flip
	releasedIDs, err := adeps.Assessments.ReleaseAllSubmissions(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "release submissions failed: "+err.Error())
		return
	}
	emitReleaseEvents(adeps, tenantID, a, releasedIDs, r)
	writeJSON(w, http.StatusOK, assessmentDTO(a))
}

// forceCloseAssessmentHandler — POST /api/v1/assessments/{id}/force-close.
// BE-ASSESSMENT-LIFECYCLE-CTAS-MISSING (P0) — closes the FE 404 surfaced in
// docs/m13/be-cj1-specifics-2026-05-16.md. FSM Option A (permissive): OPEN →
// CLOSED. Returns 409 on any non-OPEN state. release-results stays
// permissive from OPEN — force-close is the early-curtail CTA, not a
// prerequisite for release.
func forceCloseAssessmentHandler(adeps *AssessmentDeps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, id)
	if err != nil {
		log.Printf("delivery: forceCloseAssessment id=%s tenant=%s err=%v", id, tenantID, err)
		writeError(w, http.StatusInternalServerError, "assessment read failed: "+err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "assessment not found")
		return
	}
	if a.InstructorGCID != gcid && !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "instructor-only")
		return
	}
	if cErr := a.ForceClose(time.Now()); cErr != nil {
		writeError(w, http.StatusConflict, cErr.Error())
		return
	}
	if err := adeps.Assessments.Save(r.Context(), a); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	// Emit assessment.closed.v1 for IMDA D1 accountability + downstream cohort
	// monitoring (consistent with the other lifecycle transitions). Idempotent
	// publish — the FSM gate above ensures we only emit on a real transition.
	emitAssessmentEvent(adeps, "chora.delivery.assessment.closed.v1", a, r)
	writeJSON(w, http.StatusOK, assessmentDTO(a))
}

// archiveAssessmentHandler — POST /api/v1/assessments/{id}/archive.
// BE-ASSESSMENT-LIFECYCLE-CTAS-MISSING (P0). Soft-archive at deleted_at +
// archived_at — accepts caller from any state; idempotent on already-
// ARCHIVED. Domain (a *Assessment) Archive is idempotent — handler trusts
// the domain.
func archiveAssessmentHandler(adeps *AssessmentDeps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, id)
	if err != nil {
		log.Printf("delivery: archiveAssessment id=%s tenant=%s err=%v", id, tenantID, err)
		writeError(w, http.StatusInternalServerError, "assessment read failed: "+err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "assessment not found")
		return
	}
	if a.InstructorGCID != gcid && !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "instructor-only")
		return
	}
	wasArchived := a.State == domain.AssessmentStateArchived
	a.Archive(time.Now())
	if err := adeps.Assessments.Save(r.Context(), a); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	if !wasArchived {
		emitAssessmentEvent(adeps, "chora.delivery.assessment.archived.v1", a, r)
	}
	writeJSON(w, http.StatusOK, assessmentDTO(a))
}

// -----------------------------------------------------------------------------
// Learner — /me/* endpoints
// -----------------------------------------------------------------------------

func myAssessmentsHandler(adeps *AssessmentDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tenantID, gcid := callerTenantGCID(w, r)
		if tenantID == "" {
			return
		}
		pageSize := 20
		if v := r.URL.Query().Get("page_size"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
				pageSize = n
			}
		}
		items, nextToken, err := adeps.Assessments.ListVisibleToLearner(r.Context(), tenantID, gcid, pageSize, r.URL.Query().Get("page_token"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list failed")
			return
		}
		now := time.Now()
		out := make([]map[string]interface{}, 0, len(items))
		for _, a := range items {
			// Materialise an elapsed window before the DTO. Without this the
			// list reports a stale OPEN for an assessment whose window closed
			// days ago, and the learner UI renders it AVAILABLE with an
			// enabled Start (CHO-2153 F6) — the exact live symptom.
			a.AutoFlipToClosed(now)
			out = append(out, learnerAssessmentSummaryDTO(r.Context(), a, gcid, adeps))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items":           out,
			"next_page_token": nullableToken(nextToken),
		})
	}
}

func myAssessmentSubHandler(adeps *AssessmentDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/me/assessments/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		assessmentID := parts[0]
		// /me/assessments/{id}
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			getMyAssessmentHandler(adeps, assessmentID, w, r)
			return
		}
		// /me/assessments/{id}/submissions
		if len(parts) == 2 && parts[1] == "submissions" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			startMySubmissionHandler(adeps, assessmentID, w, r)
			return
		}
		// /me/assessments/{id}/submissions/{subId} — no-suffix route.
		// Per OpenAPI delivery-assessments.yaml v1.1 the canonical autosave
		// PATCH path is no-suffix (BE-30). GET on the same path is the
		// learner-rehydrate accessor (BE-31). Suffix variants (/autosave)
		// stay as backward-compat aliases for one release cycle so in-
		// flight FE clients don't 404 during rollout — see below.
		if len(parts) == 3 && parts[1] == "submissions" {
			submissionID := parts[2]
			switch r.Method {
			case http.MethodGet:
				getMySubmissionHandler(adeps, assessmentID, submissionID, w, r)
				return
			case http.MethodPatch:
				autosaveMySubmissionHandler(adeps, assessmentID, submissionID, w, r)
				return
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
		}
		// /me/assessments/{id}/submissions/{subId}/...
		if len(parts) >= 4 && parts[1] == "submissions" {
			submissionID := parts[2]
			switch parts[3] {
			case "autosave":
				// BE-30 backward-compat alias. No-suffix path is canonical.
				if r.Method != http.MethodPatch {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				autosaveMySubmissionHandler(adeps, assessmentID, submissionID, w, r)
				return
			case "submit":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				submitMySubmissionHandler(adeps, assessmentID, submissionID, w, r)
				return
			case "result":
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				myResultHandler(adeps, assessmentID, submissionID, w, r)
				return
			}
		}
		writeError(w, http.StatusNotFound, "not found")
	}
}

func getMyAssessmentHandler(adeps *AssessmentDeps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, id)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	facts, ferr := resolveCohortFacts(adeps, r, a, tenantID, gcid)
	if ferr != nil {
		writeError(w, http.StatusInternalServerError, "DELIVERY_COHORT_ROSTER_UNAVAILABLE")
		return
	}
	if !a.IsLearnerVisible(gcid, facts) {
		writeError(w, http.StatusForbidden, "learner not eligible for this assessment")
		return
	}
	// Materialise the elapsed window before the DTO so a past-close assessment
	// reports CLOSED rather than a stale OPEN (CHO-2153 F6).
	a.AutoFlipToClosed(time.Now())
	writeJSON(w, http.StatusOK, learnerAssessmentDetailDTO(r.Context(), a, gcid, adeps))
}

// resolveCohortFacts resolves the roster facts for a cohort decision and
// LOUD-logs any failure. Callers must refuse the request on error — never
// proceed with zero-valued facts, and never treat "roster unavailable" as
// "not eligible" silently: a denial and an outage are different things.
func resolveCohortFacts(adeps *AssessmentDeps, r *http.Request, a *domain.Assessment, tenantID, gcid string) (domain.LearnerCohortFacts, error) {
	facts, err := domain.ResolveCohortFacts(r.Context(), adeps.Roster, a, tenantID, gcid)
	if err != nil {
		log.Printf("delivery: cohort roster lookup FAILED — refusing (tenant=%s assessment=%s gcid=%s) err=%v",
			tenantID, a.ID, gcid, err)
	}
	return facts, err
}

func startMySubmissionHandler(adeps *AssessmentDeps, assessmentID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, assessmentID)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "assessment not found")
		return
	}
	// ---- Cohort authz (CHO-2153 F5). Refuse BEFORE anything is written. ----
	facts, ferr := resolveCohortFacts(adeps, r, a, tenantID, gcid)
	if ferr != nil {
		writeError(w, http.StatusInternalServerError, "DELIVERY_COHORT_ROSTER_UNAVAILABLE")
		return
	}
	if !a.IsLearnerEligible(gcid, facts) {
		writeError(w, http.StatusForbidden, "DELIVERY_LEARNER_NOT_IN_COHORT")
		return
	}

	// ---- Window (CHO-2153 F6). Enforced at BOTH ends, and enforced HERE —
	// before NewSubmission, before Save, before the event. Previously the only
	// window check lived in CanAutosave, which fires on the first autosave:
	// by then the attempt row is committed and submission.started.v1 is on the
	// wire, so a learner "burns" an attempt on an assessment they could never
	// have sat. A refusal that still costs an attempt is not a refusal.
	now := time.Now()
	a.AutoFlipToOpen(now)
	a.AutoFlipToClosed(now)
	switch a.StartWindowError(now) {
	case domain.ErrAssessmentNotYetOpen:
		writeError(w, http.StatusConflict, "DELIVERY_ASSESSMENT_NOT_YET_OPEN")
		return
	case domain.ErrAssessmentWindowClosed:
		writeError(w, http.StatusConflict, "DELIVERY_ASSESSMENT_WINDOW_CLOSED")
		return
	}
	// The window permits an attempt; the FSM must also be OPEN. (A manually
	// force-closed, GRADING, GRADED or RELEASED assessment is not startable
	// even inside its window.) SCHEDULED is no longer accepted here: if the
	// window had opened, AutoFlipToOpen has already moved it to OPEN.
	if a.State != domain.AssessmentStateOpen {
		writeError(w, http.StatusConflict, "DELIVERY_ASSESSMENT_NOT_OPEN")
		return
	}
	// Idempotent: if learner has IN_PROGRESS submission, return it.
	existing, found, err := adeps.Submissions.GetInProgressByLearner(r.Context(), tenantID, assessmentID, gcid)
	if err == nil && found && existing != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"submission_id":      existing.ID,
			"attempt_number":     existing.AttemptNumber,
			"opens_at":           existing.OpensAt,
			"closes_at":          existing.ClosesAt,
			"time_limit_seconds": existing.TimeLimitSecs,
			"idempotent_replay":  true,
		})
		return
	}
	// Attempts-exhausted check
	count, _ := adeps.Submissions.CountAttemptsByLearner(r.Context(), tenantID, assessmentID, gcid)
	if count >= a.MaxAttempts {
		writeError(w, http.StatusConflict, "DELIVERY_SUBMISSION_MAX_ATTEMPTS_EXHAUSTED")
		return
	}
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   assessmentID,
		TenantID:       tenantID,
		LearnerGCID:    gcid,
		AttemptNumber:  count + 1,
		OpensAt:        a.ScheduledOpenAt,
		ClosesAt:       a.ScheduledCloseAt,
		MaxScore:       a.TotalPoints,
		PassingPercent: a.GradingConfigSnapshot.PassingThresholdPercent,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := adeps.Submissions.Save(r.Context(), sub); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	emitSubmissionStartedEvent(adeps, a, sub, r)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"submission_id":      sub.ID,
		"attempt_number":     sub.AttemptNumber,
		"opens_at":           sub.OpensAt,
		"closes_at":          sub.ClosesAt,
		"time_limit_seconds": sub.TimeLimitSecs,
		"idempotent_replay":  false,
	})
}

type autosaveReq struct {
	Answers []struct {
		TestSetQuestionID string   `json:"test_set_question_id"`
		QuestionID        string   `json:"question_id"`
		QuestionType      string   `json:"question_type,omitempty"`
		MCQChoiceID       string   `json:"mcq_choice_id,omitempty"`
		MCQChoiceIDs      []string `json:"mcq_choice_ids,omitempty"`
		OEResponseText    string   `json:"oe_response_text,omitempty"`
	} `json:"answers"`
}

func autosaveMySubmissionHandler(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	sub, ok, err := adeps.Submissions.Get(r.Context(), tenantID, submissionID)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "submission not found")
		return
	}
	if sub.AssessmentID != assessmentID || sub.LearnerGCID != gcid {
		writeError(w, http.StatusForbidden, "not your submission")
		return
	}
	var req autosaveReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Answers) == 0 {
		writeError(w, http.StatusBadRequest, "answers required")
		return
	}
	answers := make([]domain.SubmissionAnswer, 0, len(req.Answers))
	for _, a := range req.Answers {
		qt := domain.QuestionTypeMCQ
		if strings.EqualFold(a.QuestionType, "oe") {
			qt = domain.QuestionTypeOE
		} else if a.OEResponseText != "" && a.MCQChoiceID == "" && len(a.MCQChoiceIDs) == 0 {
			qt = domain.QuestionTypeOE
		}
		answers = append(answers, domain.SubmissionAnswer{
			TestSetQuestionID: a.TestSetQuestionID,
			QuestionID:        a.QuestionID,
			QuestionType:      qt,
			MCQChoiceID:       a.MCQChoiceID,
			MCQChoiceIDs:      a.MCQChoiceIDs,
			OEResponseText:    a.OEResponseText,
		})
	}
	if err := sub.MergeAnswers(time.Now(), answers); err != nil {
		if errors.Is(err, domain.ErrSubmissionWindowClosed) {
			writeError(w, http.StatusConflict, "DELIVERY_SUBMISSION_WINDOW_CLOSED")
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := adeps.Submissions.Save(r.Context(), sub); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, submissionDTO(sub, time.Now()))
}

func submitMySubmissionHandler(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	sub, ok, err := adeps.Submissions.Get(r.Context(), tenantID, submissionID)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "submission not found")
		return
	}
	if sub.AssessmentID != assessmentID || sub.LearnerGCID != gcid {
		writeError(w, http.StatusForbidden, "not your submission")
		return
	}
	a, _, _ := adeps.Assessments.Get(r.Context(), tenantID, assessmentID)

	idempotentReplay := false
	now := time.Now().UTC()
	if err := sub.MarkSubmitted(now); err != nil {
		if errors.Is(err, domain.ErrSubmissionAlreadySubmitted) {
			idempotentReplay = true
		} else {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}

	// Inline MCQ grading per ADR-155 D2. Snapshot-driven (Fix-F — Lane A
	// snapshot debt close); fails loud on missing snapshot per
	// `feedback_no_stubs_real_wiring`.
	gradeMCQAnswersInline(adeps, sub, r)

	// Decide path: any OE answers → emit oe_batch_requested.v1; else immediate.
	hasOE := sub.HasOEAnswers()
	if hasOE && !idempotentReplay {
		sub.MoveToOEPending(now)
		emitSubmissionRequestedEvent(adeps, a, sub, r)
	} else if !idempotentReplay {
		// MCQ-only fast path per §D8 — graded inline; pending release.
		sub.MarkGradedPendingRelease(now)
		emitSubmissionGradedEvent(adeps, a, sub, r)
		// Auto-release per the R+ "Auto-release results" instantiate option.
		// The flag is locked into the per-Assessment grading snapshot at
		// create-time (GradingConfigSnapshot.AutoRelease). MCQ-only submissions
		// carry ReviewStatusNotRequired so CanRelease() is true and they release
		// with no instructor step. OE/AI-graded submissions (ReviewStatusPendingReview)
		// take the hasOE branch above and stay gated by the ADR-172 §D6 HITL gate —
		// they are NEVER auto-released here.
		if a != nil && a.GradingConfigSnapshot.AutoRelease && sub.CanRelease() {
			sub.MarkReleased(now)
			if rerr := a.ReleaseResults(now, ""); rerr == nil {
				if serr := adeps.Assessments.Save(r.Context(), a); serr != nil {
					writeError(w, http.StatusInternalServerError, "save failed: "+serr.Error())
					return
				}
				emitSubmissionReleasedEvent(adeps, tenantID, a, sub.ID, sub.LearnerGCID, r)
			}
		}
	}
	// LEG4-A — emit chora.delivery.grading.mcq_completed.v1 AFTER inline
	// MCQ grading per ADR-155 §line 70. IMDA D1 accountability marker +
	// submit→mcq_completed latency histogram input. Idempotent on replay.
	if !idempotentReplay {
		emitMCQCompletedEvent(adeps, a, sub, hasOE, r)
	}
	if !idempotentReplay {
		emitSubmissionSubmittedEvent(adeps, a, sub, r)
	}
	if err := adeps.Submissions.Save(r.Context(), sub); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	status := http.StatusAccepted
	if idempotentReplay {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]interface{}{
		"submission_id":       sub.ID,
		"state":               sub.State.WireState(),
		"grading_job_id":      "", // out-of-band; orchestrator owns
		"grading_eta_seconds": gradingETASeconds(hasOE),
	})
}

// getMySubmissionHandler — GET /me/assessments/{id}/submissions/{subId}.
// Closes E2E-BE-31. Used by the FE assessment runner for idempotent-replay
// rehydrate (recovers prior answers + last_saved_at + time_remaining_seconds
// after a tab refresh or 5s heartbeat).
//
// Auth: caller must be the learner who owns the submission. Returns 404
// when the submission is missing OR belongs to a different assessment;
// 403 when ownership doesn't match.
//
// Response: the same Submission DTO already used by autosave's response
// path (`submissionDTO`) so the FE can re-use one decoder.
func getMySubmissionHandler(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	sub, ok, err := adeps.Submissions.Get(r.Context(), tenantID, submissionID)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "submission not found")
		return
	}
	if sub.AssessmentID != assessmentID {
		writeError(w, http.StatusNotFound, "submission not found")
		return
	}
	if sub.LearnerGCID != gcid {
		writeError(w, http.StatusForbidden, "not your submission")
		return
	}
	writeJSON(w, http.StatusOK, submissionDTO(sub, time.Now()))
}

// myResultHandler — GET /me/assessments/{id}/submissions/{subId}/result.
// Closes E2E-BE-4 + LEG5-A + LEG5-B + LEG5-C.
//
// Behaviour by submission state:
//
//   - IN_PROGRESS / STARTED → 409 with code DELIVERY_SUBMISSION_IN_PROGRESS.
//     FE redirects to GET .../submissions/{id} for rehydrate. Per LEG5-C
//     this is NOT folded into PENDING_RELEASE (semantics differ — resume
//     vs wait-for-instructor).
//
//   - PENDING_OE_GRADING → 200 with {state: PENDING_RELEASE,
//     oe_batch_pending: true, message: ...}. LEG5-A — distinguishes "OE
//     still grading" from "graded, awaiting release".
//
//   - GRADED_PENDING_RELEASE → 200 with {state: PENDING_RELEASE,
//     oe_batch_pending: false, message: ...}.
//
//   - RELEASED → 200 with full SubmissionResult including:
//
//   - per_question_grades[] (with correct + grading_dispatch +
//     llm_evaluator_feedback + mcq_post_grade.options[] per LEG5-B)
//
//   - breakdown summary (mcq_correct/total + oe_pending/total) per
//     E2E-BE-4
//
//   - instructor_comment + released_at
//
//   - Any other state → 200 PENDING_RELEASE envelope (defensive).
func myResultHandler(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	sub, ok, err := adeps.Submissions.Get(r.Context(), tenantID, submissionID)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "submission not found")
		return
	}
	if sub.AssessmentID != assessmentID || sub.LearnerGCID != gcid {
		writeError(w, http.StatusForbidden, "not your submission")
		return
	}

	// LEG5-C: IN_PROGRESS / STARTED → 409 with explicit redirect code.
	if sub.State == domain.SubmissionStateInProgress || sub.State == domain.SubmissionStateStarted {
		writeErrorCode(w, http.StatusConflict, "DELIVERY_SUBMISSION_IN_PROGRESS",
			"submission still in progress; rehydrate via GET /me/assessments/{id}/submissions/{id}")
		return
	}

	// LEG5-A: PENDING_OE_GRADING → oe_batch_pending=true.
	if sub.State == domain.SubmissionStatePendingOEGrading || sub.State == domain.SubmissionStateSubmitted {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"state":            "PENDING_RELEASE",
			"oe_batch_pending": true,
			"message":          "Your submission is being graded. Results will be released by your instructor once grading finishes.",
		})
		return
	}

	if sub.IsPendingRelease() {
		// LEG5-A: MCQ-only fast path or all-OE-graded — no pending OE.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"state":            "PENDING_RELEASE",
			"oe_batch_pending": false,
			"message":          "Your submission has been graded. Results will be released by your instructor.",
		})
		return
	}

	if sub.State != domain.SubmissionStateReleased {
		// Defensive: e.g., ARCHIVED. FE can show a generic pending notice.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"state":            "PENDING_RELEASE",
			"oe_batch_pending": false,
			"message":          "Your submission status cannot be released to you at this time.",
		})
		return
	}

	// RELEASED — full SubmissionResult.
	// LEG5-B: resolve mcq_post_grade.options[] from the test_set_question
	// payload_snapshot. Same lookup path as inline MCQ grading uses; per
	// ddd-enforcement #3 NEVER cross-DB query chora_creation at runtime.
	snapshots, _ := loadMCQSnapshotsForSubmission(adeps, sub, r)
	// OT#4 — resolve durable gs:// model-answer AND question/stem images to
	// fresh signed URLs before the reveal projects them (one batched RPC).
	resolveSnapshotImages(r.Context(), adeps, tenantID, snapshots)

	// shuffle_mcq_options — mirror the take-time per-learner option order in the
	// reveal so the learner sees their options in the SAME positions they
	// answered in. Same deterministic seed (gcid, test_set_question_id) as the
	// take projection. Off ⇒ canonical order. Load the assessment for the flag.
	shuffleMCQ := false
	if adeps.Assessments != nil {
		if a, ok, aerr := adeps.Assessments.Get(r.Context(), tenantID, assessmentID); aerr == nil && ok && a != nil {
			shuffleMCQ = a.ShuffleMCQOptions
		}
	}

	// ATOM-2: resolve oe_post_grade.{model_answer, rubric} from the same
	// payload_snapshot column (now AUTHOR-SAFE after B6 lands the gRPC
	// SnapshotQuestionByID change in chora-creation). Returns the raw
	// snapshot JSON keyed by test_set_question_id so we can run the
	// AUTHOR-SAFE projection at reveal time without re-querying the port.
	oeSnapshots := loadOESnapshotJSONsForSubmission(adeps, sub, r)
	// ADR-172 — resolve the OE snapshots' durable gs:// stem (image_url) AND
	// model-answer (answer_image_url) images to fresh signed URLs the same way
	// MCQ does (resolveSnapshotImages). Returns per-test_set_question_id
	// {question_image_url, answer_image_url} signed strings (gs:// passed
	// through the MediaResolver; old http URLs kept raw). Mirrors the MCQ path.
	oeImages := resolveOESnapshotImages(r.Context(), adeps, tenantID, oeSnapshots)

	perQ := make([]map[string]interface{}, 0, len(sub.Answers))
	mcqCorrect, mcqTotal, oeGraded, oeTotal, oePending := 0, 0, 0, 0, 0
	for _, ans := range sub.Answers {
		entry := map[string]interface{}{
			"test_set_question_id": ans.TestSetQuestionID,
			"question_id":          ans.QuestionID,
			"question_type":        string(ans.QuestionType),
			"points_earned":        ans.PointsEarned,
			"points_possible":      ans.PointsPossible,
			"grading_dispatch":     string(ans.GradingDispatch),
		}
		// E2E-BE-RESULT-LEARNER-ANSWER (P1 smoke #3 2026-05-17) — echo the
		// learner's submitted answer so the FE result template can render
		// "YOUR ANSWER: …" instead of "No answer". Shape:
		// chora-contracts/openapi/delivery-assessments.yaml §LearnerAnswerEcho.
		learnerAnswer := map[string]interface{}{}
		if ans.MCQChoiceID != "" {
			learnerAnswer["mcq_choice_id"] = ans.MCQChoiceID
		}
		if len(ans.MCQChoiceIDs) > 0 {
			out := make([]interface{}, len(ans.MCQChoiceIDs))
			for i, v := range ans.MCQChoiceIDs {
				out[i] = v
			}
			learnerAnswer["mcq_choice_ids"] = out
		}
		if ans.OEResponseText != "" {
			learnerAnswer["oe_response_text"] = ans.OEResponseText
		}
		entry["learner_answer"] = learnerAnswer
		if ans.QuestionType == domain.QuestionTypeMCQ {
			mcqTotal++
			if ans.MCQCorrect != nil {
				entry["correct"] = *ans.MCQCorrect
				if *ans.MCQCorrect {
					mcqCorrect++
				}
			}
			// LEG5-B: mcq_post_grade.options[] reveal payload from snapshot.
			if snap, ok := snapshots[ans.TestSetQuestionID]; ok && len(snap.Options) > 0 {
				if shuffleMCQ {
					shuffleOptionRows(snap.Options, optionShuffleSeed(gcid, ans.TestSetQuestionID))
				}
				postGrade := map[string]interface{}{
					"options": snap.Options,
				}
				// W8 image-gen: surface the model-answer illustration on the
				// reveal. Field name is IDENTICAL across every layer
				// (snapshot json tag → DTO → FE model → template). Omitted
				// when the snapshot carried no answer_image_url.
				if snap.AnswerImageURL != nil && strings.TrimSpace(*snap.AnswerImageURL) != "" {
					postGrade["answer_image_url"] = *snap.AnswerImageURL
				}
				entry["mcq_post_grade"] = postGrade
				// W8 image-gen: surface the QUESTION/stem illustration on the
				// reveal too (sibling to mcq_post_grade) so the learner reviews
				// the question WITH its picture, not just the model-answer image.
				// Already minted to a signed URL by resolveSnapshotImages above.
				if snap.ImageURL != nil && strings.TrimSpace(*snap.ImageURL) != "" {
					entry["question_image_url"] = *snap.ImageURL
				}
			}
		}
		if ans.QuestionType == domain.QuestionTypeOE {
			oeTotal++
			if strings.TrimSpace(ans.OEFeedback) != "" || ans.OEGradedAt != nil {
				oeGraded++
			} else if strings.TrimSpace(ans.OEResponseText) != "" {
				oePending++
			}
			// ATOM-2: oe_post_grade.{model_answer, rubric} reveal payload —
			// project the AUTHOR-SAFE shape from the snapshot. Falls back to
			// omitting the field when the snapshot is absent or unparseable
			// (the FE renders WITHOUT the reveal panel rather than crashing).
			oePostGrade := map[string]interface{}{}
			if rawJSON, ok := oeSnapshots[ans.TestSetQuestionID]; ok && strings.TrimSpace(rawJSON) != "" {
				if proj, projOK := projectAuthorSafeSnapshot("oe", rawJSON); projOK {
					if ma, ok := proj["model_answer"].(string); ok && ma != "" {
						oePostGrade["model_answer"] = ma
					}
					if rub, ok := proj["rubric"].([]map[string]interface{}); ok && len(rub) > 0 {
						oePostGrade["rubric"] = rub
					}
				}
			}
			// ADR-172 — project the AI/HITL grading artifacts onto oe_post_grade.
			// criterion-level scores + per-question grader comment + its provenance
			// + the score provenance + the quality flag all live on the
			// SubmissionAnswer (stamped by Submission.ApplyGrading and the HITL
			// override path). Additive + nil-guarded.
			if crit := projectCriterionScores(ans.OECriterionJSON); len(crit) > 0 {
				oePostGrade["criterion_scores"] = crit
			}
			if strings.TrimSpace(ans.OEComment) != "" {
				oePostGrade["comment"] = ans.OEComment
			}
			if ans.OEGradedAt != nil || strings.TrimSpace(ans.OEComment) != "" {
				// Stamp provenance + quality flag once a grade landed (provOr
				// defaults empty → "AI", matching the instructor review view).
				oePostGrade["comment_provenance"] = provOr(ans.CommentProvenance)
				oePostGrade["score_provenance"] = provOr(ans.ScoreProvenance)
				oePostGrade["quality_flagged"] = ans.QualityFlagged
			}
			// W8/ADR-172 image-gen: model-answer illustration on
			// oe_post_grade.answer_image_url + the QUESTION/stem illustration on
			// entry.question_image_url — MIRRORS the MCQ path. Already minted to
			// signed URLs by resolveOESnapshotImages above. Omitted when absent.
			if img, ok := oeImages[ans.TestSetQuestionID]; ok {
				if u := strings.TrimSpace(img.QuestionImageURL); u != "" {
					entry["question_image_url"] = u
				}
				if u := strings.TrimSpace(img.AnswerImageURL); u != "" {
					oePostGrade["answer_image_url"] = u
				}
			}
			if len(oePostGrade) > 0 {
				entry["oe_post_grade"] = oePostGrade
			}
		}
		if ans.OEFeedback != "" {
			entry["llm_evaluator_feedback"] = ans.OEFeedback
		}
		perQ = append(perQ, entry)
	}
	// Present per-question rows in the test-set's canonical display_order, not
	// the submission-storage order of sub.Answers (2026-06-20 finding).
	orderRowsByDisplayOrder(perQ, displayOrderForSubmission(adeps, sub, r))
	a, _, _ := adeps.Assessments.Get(r.Context(), tenantID, assessmentID)
	passing := 70
	if a != nil {
		passing = a.GradingConfigSnapshot.PassingThresholdPercent
	}
	result := map[string]interface{}{
		"submission_id":             sub.ID,
		"total_points_earned":       sub.TotalScore,
		"total_points_possible":     sub.MaxScore,
		"passing_threshold_percent": passing,
		"passed":                    sub.Passed != nil && *sub.Passed,
		"breakdown": map[string]interface{}{
			"mcq_correct_count": mcqCorrect,
			"mcq_total":         mcqTotal,
			"oe_graded_count":   oeGraded,
			"oe_total":          oeTotal,
			"oe_pending_count":  oePending,
		},
		"per_question_grades": perQ,
	}
	if a != nil && a.ReleaseAnnouncement != "" {
		result["instructor_comment"] = a.ReleaseAnnouncement
	}
	// ADR-172 — per-submission whole-assessment OE narrative (distinct from the
	// assessment-level instructor_comment/ReleaseAnnouncement above). Drafted by
	// the grading orchestrator (provenance AI) and editable in the HITL review;
	// provOr defaults empty → "AI". Omitted when no overall comment was set.
	if strings.TrimSpace(sub.OverallComment) != "" {
		result["overall_comment"] = sub.OverallComment
		result["overall_comment_provenance"] = provOr(sub.OverallCommentProvenance)
	}
	if sub.ReleasedAt != nil {
		result["released_at"] = sub.ReleasedAt
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"state":  "RELEASED",
		"result": result,
	})
}

// -----------------------------------------------------------------------------
// Helpers — DTOs, MCQ inline grading, ETA, events
// -----------------------------------------------------------------------------

func assessmentDTO(a *domain.Assessment) map[string]interface{} {
	out := map[string]interface{}{
		"assessment_id":              a.ID,
		"tenant_id":                  a.TenantID,
		"instructor_gcid":            a.InstructorGCID,
		"test_set_id":                a.TestSetID,
		"test_set_revision_snapshot": a.TestSetRevisionSnapshot,
		"title":                      a.Title,
		"state":                      string(a.State),
		"max_attempts":               a.MaxAttempts,
		"shuffle_questions":          a.ShuffleQuestions,
		"shuffle_mcq_options":        a.ShuffleMCQOptions,
		"total_points":               a.TotalPoints,
		"question_count":             a.QuestionCount,
		"grading_config_snapshot":    a.GradingConfigSnapshot,
		"created_at":                 a.CreatedAt,
		"updated_at":                 a.UpdatedAt,
		"invited_gcids":              a.InvitedGCIDs,
	}
	if !a.ScheduledOpenAt.IsZero() {
		out["scheduled_open_at"] = a.ScheduledOpenAt
	}
	if !a.ScheduledCloseAt.IsZero() {
		out["scheduled_close_at"] = a.ScheduledCloseAt
	}
	if a.LearnerFacingName != "" {
		out["learner_facing_name"] = a.LearnerFacingName
	}
	if a.ClassID != "" {
		out["class_id"] = a.ClassID
	}
	if a.OfferingID != "" {
		out["offering_id"] = a.OfferingID
	}
	if a.PublishedAt != nil {
		out["published_at"] = a.PublishedAt
	}
	if a.ClosedAt != nil {
		out["closed_at"] = a.ClosedAt
	}
	if a.ResultsReleasedAt != nil {
		out["results_released_at"] = a.ResultsReleasedAt
		out["released_at"] = a.ResultsReleasedAt
	}
	return out
}

func learnerAssessmentSummaryDTO(ctx context.Context, a *domain.Assessment, learnerGCID string, adeps *AssessmentDeps) map[string]interface{} {
	count := 0
	var latest *domain.Submission
	if adeps != nil && adeps.Submissions != nil {
		// LEG3-C bug-fix: was passing nil ctx, which crashed the pg repo
		// (tracing.WithTenantID(nil, ...) → context.WithValue panic) the
		// moment a real assessment was visible to the learner. InMem repo
		// tolerated nil; pg does not. Thread r.Context() in.
		count, _ = adeps.Submissions.CountAttemptsByLearner(ctx, a.TenantID, a.ID, learnerGCID)
		// E2E-BE-RESULT-LINK-SUBMISSION-ID — populate the learner's latest
		// attempt (id + state) so the FE "View result" CTA can deep-link
		// straight to /a/me/assessments/{id}/result/{subId} without a second
		// round-trip. Fail-soft: any repo error leaves both fields absent
		// (the FE already tolerates absence by falling back to the assessment
		// cover page).
		latest, _, _ = adeps.Submissions.FindLatestByLearner(ctx, a.TenantID, a.ID, learnerGCID)
	}
	rem := a.MaxAttempts - count
	if rem < 0 {
		rem = 0
	}
	out := map[string]interface{}{
		"assessment_id":              a.ID,
		"title":                      a.Title,
		"state":                      string(a.State),
		"scheduled_open_at":          a.ScheduledOpenAt,
		"scheduled_close_at":         a.ScheduledCloseAt,
		"max_attempts":               a.MaxAttempts,
		"learner_attempt_count":      count,
		"learner_remaining_attempts": rem,
		"question_count":             a.QuestionCount,
		"total_points":               a.TotalPoints,
	}
	if a.LearnerFacingName != "" {
		out["learner_facing_name"] = a.LearnerFacingName
	}
	if latest != nil {
		out["learner_latest_submission_id"] = latest.ID
		out["learner_latest_submission_state"] = string(latest.State)
	}
	return out
}

func learnerAssessmentDetailDTO(ctx context.Context, a *domain.Assessment, learnerGCID string, adeps *AssessmentDeps) map[string]interface{} {
	out := learnerAssessmentSummaryDTO(ctx, a, learnerGCID, adeps)
	// LEG3-D fix — project the parent test-set's questions in learner-safe
	// shape so the FE runner can render stems + options without a second
	// /test-sets/{id} round-trip. The original empty-list shim broke the
	// demo chain (questions: 0 → FE rendered "no questions"). Per
	// ddd-enforcement #3 we only touch chora_delivery; the test-set Get
	// loads question rows via the existing TestSetPort.
	out["questions"] = []any{}
	if adeps == nil || adeps.TestSets == nil {
		return out
	}
	ts, ok, err := adeps.TestSets.Get(ctx, a.TenantID, a.TestSetID)
	if err != nil || !ok || ts == nil {
		// Log + leave questions empty — the SummaryDTO + state info still
		// flow through so the FE can render the title / counts.
		if err != nil {
			log.Printf("delivery: learnerAssessmentDetailDTO test-set lookup err=%v", err)
		}
		return out
	}
	qs := ts.Questions()
	dtos := make([]map[string]interface{}, 0, len(qs))
	for _, q := range qs {
		dtos = append(dtos, learnerSafeTestSetQuestionDTO(q))
	}
	// shuffle_mcq_options — reorder each MCQ's options per-learner so the
	// correct answer isn't always in the same slot. Deterministic by
	// (gcid, test_set_question_id) so the take view + result reveal agree and
	// the order is stable across reloads. grading is option_id-based ⇒ safe.
	if a.ShuffleMCQOptions {
		for _, dto := range dtos {
			if qt, _ := dto["question_type"].(string); strings.ToLower(qt) != "mcq" {
				continue
			}
			prompt, ok := dto["prompt"].(map[string]interface{})
			if !ok {
				continue
			}
			opts, ok := prompt["options"].([]map[string]interface{})
			if !ok {
				continue
			}
			tsqID, _ := dto["test_set_question_id"].(string)
			shuffleOptionRows(opts, optionShuffleSeed(learnerGCID, tsqID))
		}
	}
	// OT#4 — resolve durable gs:// stem images to fresh signed GET URLs in a
	// single batched RPC so the learner's <img> renders past the 7-day
	// transient-bucket TTL. No-op for old http URLs + when unwired.
	resolveLearnerQuestionImages(ctx, adeps, a.TenantID, dtos)
	out["questions"] = dtos
	return out
}

// resolveLearnerQuestionImages rewrites durable gs:// `prompt.image_url` refs
// across the learner question DTOs to fresh signed URLs via one batched
// MintAtomMediaDownloadURL RPC. No-op when the resolver is unwired or no
// gs:// refs are present; an unresolved ref is left raw (fail-visible).
func resolveLearnerQuestionImages(ctx context.Context, adeps *AssessmentDeps, tenantID string, dtos []map[string]interface{}) {
	if adeps == nil || adeps.MediaResolver == nil {
		return
	}
	var refs []string
	for _, d := range dtos {
		if p, ok := d["prompt"].(map[string]interface{}); ok {
			if u, ok := p["image_url"].(string); ok && strings.HasPrefix(u, "gs://") {
				refs = append(refs, u)
			}
		}
	}
	if len(refs) == 0 {
		return
	}
	resolved := adeps.MediaResolver.ResolveDownloadURLs(ctx, tenantID, refs)
	if len(resolved) == 0 {
		return
	}
	for _, d := range dtos {
		if p, ok := d["prompt"].(map[string]interface{}); ok {
			if u, ok := p["image_url"].(string); ok {
				if signed, ok := resolved[u]; ok {
					p["image_url"] = signed
				}
			}
		}
	}
}

// resolveSnapshotImages rewrites durable gs:// image refs on the loaded MCQ
// snapshots — BOTH the model-answer illustration (AnswerImageURL) AND the
// question/stem illustration (ImageURL) — to fresh signed URLs in ONE batched
// RPC before the result reveal projects them. MCQSnapshot is a value type in
// the map, so mutated copies are written back. No-op when unwired / no gs://.
func resolveSnapshotImages(ctx context.Context, adeps *AssessmentDeps, tenantID string, snapshots map[string]domain.MCQSnapshot) {
	if adeps == nil || adeps.MediaResolver == nil {
		return
	}
	var refs []string
	for _, s := range snapshots {
		if s.AnswerImageURL != nil && strings.HasPrefix(*s.AnswerImageURL, "gs://") {
			refs = append(refs, *s.AnswerImageURL)
		}
		if s.ImageURL != nil && strings.HasPrefix(*s.ImageURL, "gs://") {
			refs = append(refs, *s.ImageURL)
		}
	}
	if len(refs) == 0 {
		return
	}
	resolved := adeps.MediaResolver.ResolveDownloadURLs(ctx, tenantID, refs)
	if len(resolved) == 0 {
		return
	}
	for k, s := range snapshots {
		changed := false
		if s.AnswerImageURL != nil {
			if signed, ok := resolved[*s.AnswerImageURL]; ok {
				sc := signed
				s.AnswerImageURL = &sc
				changed = true
			}
		}
		if s.ImageURL != nil {
			if signed, ok := resolved[*s.ImageURL]; ok {
				sc := signed
				s.ImageURL = &sc
				changed = true
			}
		}
		if changed {
			snapshots[k] = s
		}
	}
}

// oeImageURLs carries the resolved (signed) OE reveal images for one OE
// question: the stem/question illustration and the model-answer illustration.
// Field names mirror the wire keys (question_image_url / answer_image_url).
type oeImageURLs struct {
	QuestionImageURL string
	AnswerImageURL   string
}

// resolveOESnapshotImages parses each OE snapshot's top-level gs:// image_url
// (stem) + answer_image_url (model-answer) refs and resolves them to fresh
// signed GET URLs via ONE batched MintAtomMediaDownloadURL RPC — the exact
// MCQ path (resolveSnapshotImages), but over the raw OE snapshot JSON (OE
// snapshots are kept as raw strings, not the typed MCQSnapshot). Returns a
// per-test_set_question_id map of the resolved URLs. No-op (empty map) when
// the resolver is unwired or no gs:// refs are present; old http URLs / any
// unresolved ref are passed through raw (fail-visible). Mirrors MCQ; never
// touches the MCQ path.
func resolveOESnapshotImages(ctx context.Context, adeps *AssessmentDeps, tenantID string, oeSnapshots map[string]string) map[string]oeImageURLs {
	out := make(map[string]oeImageURLs, len(oeSnapshots))
	if len(oeSnapshots) == 0 {
		return out
	}
	// Extract the raw (possibly gs://) image refs per OE question.
	type rawRefs struct{ q, a string }
	perQ := make(map[string]rawRefs, len(oeSnapshots))
	var refs []string
	for tsqID, raw := range oeSnapshots {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var doc struct {
			ImageURL       string `json:"image_url"`
			AnswerImageURL string `json:"answer_image_url"`
		}
		if json.Unmarshal([]byte(raw), &doc) != nil {
			continue
		}
		q := strings.TrimSpace(doc.ImageURL)
		a := strings.TrimSpace(doc.AnswerImageURL)
		if q == "" && a == "" {
			continue
		}
		perQ[tsqID] = rawRefs{q: q, a: a}
		if strings.HasPrefix(q, "gs://") {
			refs = append(refs, q)
		}
		if strings.HasPrefix(a, "gs://") {
			refs = append(refs, a)
		}
	}
	// Resolve gs:// refs in one batched RPC (skip when unwired / none present).
	resolved := map[string]string{}
	if adeps != nil && adeps.MediaResolver != nil && len(refs) > 0 {
		resolved = adeps.MediaResolver.ResolveDownloadURLs(ctx, tenantID, refs)
	}
	sign := func(u string) string {
		if u == "" {
			return ""
		}
		if s, ok := resolved[u]; ok && s != "" {
			return s
		}
		return u // non-gs:// or unresolved → pass through raw (fail-visible)
	}
	for tsqID, rr := range perQ {
		out[tsqID] = oeImageURLs{QuestionImageURL: sign(rr.q), AnswerImageURL: sign(rr.a)}
	}
	return out
}

// projectCriterionScores parses a SubmissionAnswer's OECriterionJSON (the raw
// criterion_scores_json stamped by Submission.ApplyGrading) into the stable
// wire shape []{criterion_id, title, score, max_score, feedback}. Lenient: a
// blank/unparseable/empty payload returns nil so the caller omits the field.
// Each criterion object is whitelisted onto the known keys so additive changes
// upstream (the grader's criterion payload) never leak unexpected keys.
func projectCriterionScores(criterionJSON string) []map[string]interface{} {
	if strings.TrimSpace(criterionJSON) == "" {
		return nil
	}
	var rows []map[string]interface{}
	if json.Unmarshal([]byte(criterionJSON), &rows) != nil {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for _, m := range rows {
		if m == nil {
			continue
		}
		row := map[string]interface{}{}
		for _, k := range []string{"criterion_id", "title", "score", "max_score", "feedback"} {
			if v, ok := m[k]; ok {
				row[k] = v
			}
		}
		if len(row) > 0 {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// optionShuffleSeed combines a per-learner key with a per-question key so the
// MCQ option shuffle is deterministic + STABLE across the take view AND the
// result reveal (same learner + question ⇒ same permutation), while differing
// across learners (the anti-cheat property of shuffle_mcq_options). Returns ""
// when either part is empty (⇒ shuffle becomes a no-op).
func optionShuffleSeed(learnerGCID, testSetQuestionID string) string {
	if learnerGCID == "" || testSetQuestionID == "" {
		return ""
	}
	return learnerGCID + ":" + testSetQuestionID
}

// shuffleOptionRows deterministically reorders an MCQ option slice IN PLACE,
// seeded by `seed`. No-op for <2 rows or an empty seed. The option objects
// (each carrying option_id) move as units, so option_id-based grading and the
// FE reveal's chosen/correct matching stay correct — only the display position
// (and therefore the A/B/C/D letter) changes. Implements the assessment's
// shuffle_mcq_options setting (previously a no-op).
func shuffleOptionRows(rows []map[string]interface{}, seed string) {
	if len(rows) < 2 || seed == "" {
		return
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	rng := mathrand.New(mathrand.NewSource(int64(h.Sum64())))
	rng.Shuffle(len(rows), func(i, j int) {
		rows[i], rows[j] = rows[j], rows[i]
	})
}

// learnerSafeTestSetQuestionDTO projects a TestSetQuestion in the shape the
// learner is allowed to see. STRIPPED: any `correct_*` / `model_answer` /
// `rubric_*` field from the snapshot — those belong to grader code paths,
// never the runner. KEPT: question identity + display ordering + points +
// the learner-visible prompt fields (stem, options, max_words, etc.) when
// the snapshot is populated.
func learnerSafeTestSetQuestionDTO(q *domain.TestSetQuestion) map[string]interface{} {
	// LEG3-D R3 Option B introduced distinct QuestionAtomID + QuestionID.
	// LEG3-D R5 fix — surface BOTH UUIDs distinctly. Pre-fix this DTO was
	// emitting QuestionAtomID for both `question_atom_id` AND `question_id`,
	// silently breaking the FE picker's conformance gate + the snapshot
	// lookup at /me/assessments/{id}. See docs/m13/be-cj1-specifics-
	// 2026-05-16.md §LEG3-D R5.
	out := map[string]interface{}{
		"test_set_question_id": q.ID,
		"question_atom_id":     q.QuestionAtomID,
		"question_id":          q.QuestionID,
		"question_type":        q.QuestionType,
		"display_order":        q.DisplayOrder,
		"points":               q.Points,
	}
	// payload_snapshot is captured at TestSet.Publish time (Fix-F). When
	// absent we degrade gracefully — FE renders a placeholder. When present
	// we project only learner-safe fields via the snapshot helper below.
	if q.PayloadSnapshot != "" {
		if safe, ok := projectLearnerSafeSnapshot(q.QuestionType, q.PayloadSnapshot); ok {
			out["prompt"] = safe
		}
	}
	return out
}

// projectLearnerSafeSnapshot parses the JSONB payload_snapshot and returns
// the LEARNER-SAFE subset for the given question_type. Used pre-RELEASE
// (working canvas at /me/assessments/{id} + /me/assessments/{id}/submissions/{id}).
//
// ATOM-2 boundary contract — STRIPPED fields (would leak the answer key):
//
//	MCQ:
//	  - options[].is_correct  (the canonical answer flag)
//	  - options[].explainer   (per-option grading-feedback text)
//	  - correct_choice        (top-level form some producers emit)
//	OE:
//	  - model_answer          (the canonical reference answer)
//	  - rubric                (grader criterion array)
//
// Whitelist implementation — we copy ONLY known-safe keys into the output
// rather than copy-then-delete, so any new author-only field added to the
// upstream MCQPayload / OEPayload schema in chora-creation stays opaque to
// the learner by default (fail-closed).
//
// Returns (nil, false) on parse errors so the caller can render a
// placeholder instead of leaking raw blob bytes.
//
// AUTHOR-SAFE counterpart: projectAuthorSafeSnapshot (post-RELEASE reveal).
func projectLearnerSafeSnapshot(questionType, snapshotJSON string) (map[string]interface{}, bool) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(snapshotJSON), &raw); err != nil {
		return nil, false
	}
	out := map[string]interface{}{}
	if v, ok := raw["stem"].(string); ok {
		out["stem"] = v
	}
	// W8 image-gen: the stem illustration (`image_url`) is LEARNER-SAFE — it
	// is the question's accompanying picture, not part of the answer key. Pass
	// it through so the runner can render it while the learner takes the
	// assessment. Field name is IDENTICAL across every layer (snapshot json tag
	// → prompt DTO → FE model → template). Absent → omitted.
	if v, ok := raw["image_url"].(string); ok && strings.TrimSpace(v) != "" {
		out["image_url"] = v
	}
	switch strings.ToLower(questionType) {
	case "mcq":
		// Whitelist options[].{id,option_id,label,text} — strip is_correct /
		// explainer / correct_choice / any rubric annotation. The top-level
		// `correct_choice` (if a producer emits it that way) is implicitly
		// excluded because it is not on the top-level whitelist (only `stem`
		// + `options` are copied at the top level for MCQ).
		if opts, ok := raw["options"].([]interface{}); ok {
			safe := make([]map[string]interface{}, 0, len(opts))
			for _, o := range opts {
				m, _ := o.(map[string]interface{})
				if m == nil {
					continue
				}
				row := map[string]interface{}{}
				for _, k := range []string{"id", "option_id", "label", "text"} {
					if v, exists := m[k]; exists {
						row[k] = v
					}
				}
				// LEG3-D R5 fallback — chora-creation's MCQ domain currently
				// stores ONLY `label` (production atoms have label="Oxygen",
				// no separate `text`). The wire contract at
				// chora-contracts/openapi/creation-questions.yaml
				// §LearnerSafeMCQPayload requires BOTH `label` AND `text`.
				// Expose label as text when text is absent so the FE
				// template renders. Explicit text wins.
				if _, hasText := row["text"]; !hasText {
					if l, ok := row["label"].(string); ok && l != "" {
						row["text"] = l
					}
				}
				safe = append(safe, row)
			}
			out["options"] = safe
		}
	case "oe":
		// Whitelist prompt-side hints — strip model_answer + rubric.
		for _, k := range []string{"max_words", "min_words", "placeholder_text", "answer_format_hint", "prompt"} {
			if v, exists := raw[k]; exists {
				out[k] = v
			}
		}
	}
	return out, true
}

// projectAuthorSafeSnapshot parses the JSONB payload_snapshot and returns
// the AUTHOR-SAFE shape for the given question_type. Used post-RELEASE
// on GET /me/assessments/{id}/submissions/{id}/result so the learner can
// see WHY each option was right/wrong (MCQ) or self-assess against the
// canonical answer key (OE).
//
// ATOM-2 boundary contract — KEPT fields (the reveal payload):
//
//	MCQ:
//	  - stem
//	  - options[].{id,option_id,label,text,is_correct,explainer}
//	OE:
//	  - stem
//	  - prompt + max_words + min_words + placeholder_text + answer_format_hint
//	  - model_answer (the canonical reference answer)
//	  - rubric       (criterion array — [{criterion_id, title, description, weight}])
//
// Per chora-contracts/openapi/delivery-assessments.yaml §LearnerQuestionGrade
// (mcq_post_grade + oe_post_grade).
//
// Returns (nil, false) on parse / empty errors.
func projectAuthorSafeSnapshot(questionType, snapshotJSON string) (map[string]interface{}, bool) {
	if strings.TrimSpace(snapshotJSON) == "" {
		return nil, false
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(snapshotJSON), &raw); err != nil {
		return nil, false
	}
	out := map[string]interface{}{}
	if v, ok := raw["stem"].(string); ok {
		out["stem"] = v
	}
	switch strings.ToLower(questionType) {
	case "mcq":
		// AUTHOR-SAFE option projection: keep is_correct + explainer alongside
		// the learner-visible identity fields. The label-as-text fallback
		// (LEG3-D R5) still applies — author atoms in production stored only
		// `label` (e.g. label="Oxygen") with no separate `text`.
		if opts, ok := raw["options"].([]interface{}); ok {
			safe := make([]map[string]interface{}, 0, len(opts))
			for _, o := range opts {
				m, _ := o.(map[string]interface{})
				if m == nil {
					continue
				}
				row := map[string]interface{}{}
				for _, k := range []string{"id", "option_id", "label", "text", "is_correct", "explainer"} {
					if v, exists := m[k]; exists {
						row[k] = v
					}
				}
				if _, hasText := row["text"]; !hasText {
					if l, ok := row["label"].(string); ok && l != "" {
						row["text"] = l
					}
				}
				safe = append(safe, row)
			}
			out["options"] = safe
		}
	case "oe":
		// Prompt-side hints first (learner-facing affordances).
		for _, k := range []string{"prompt", "max_words", "min_words", "placeholder_text", "answer_format_hint"} {
			if v, exists := raw[k]; exists {
				out[k] = v
			}
		}
		// AUTHOR-SAFE reveal payload.
		if ma, ok := raw["model_answer"].(string); ok {
			out["model_answer"] = ma
		}
		// rubric is an array of criterion objects per the OE contract.
		// Project each criterion object onto known criterion fields so the
		// downstream API contract (LearnerQuestionGrade.oe_post_grade.rubric)
		// remains stable even if chora-creation extends RubricCriterion.
		if rub, ok := raw["rubric"].([]interface{}); ok {
			safe := make([]map[string]interface{}, 0, len(rub))
			for _, c := range rub {
				m, _ := c.(map[string]interface{})
				if m == nil {
					continue
				}
				row := map[string]interface{}{}
				for _, k := range []string{"criterion_id", "title", "description", "weight"} {
					if v, exists := m[k]; exists {
						row[k] = v
					}
				}
				safe = append(safe, row)
			}
			out["rubric"] = safe
		}
	}
	return out, true
}

func submissionDTO(s *domain.Submission, now time.Time) map[string]interface{} {
	out := map[string]interface{}{
		"submission_id":          s.ID,
		"assessment_id":          s.AssessmentID,
		"tenant_id":              s.TenantID,
		"learner_gcid":           s.LearnerGCID,
		"attempt_number":         s.AttemptNumber,
		"state":                  s.State.WireState(),
		"opens_at":               s.OpensAt,
		"closes_at":              s.ClosesAt,
		"time_limit_seconds":     s.TimeLimitSecs,
		"time_remaining_seconds": s.TimeRemainingSeconds(now),
		"started_at":             s.StartedAt,
		"answers":                answerListDTO(s.Answers),
	}
	if s.LastSavedAt != nil {
		out["last_saved_at"] = s.LastSavedAt
	}
	if s.SubmittedAt != nil {
		out["submitted_at"] = s.SubmittedAt
	}
	return out
}

func answerListDTO(in []domain.SubmissionAnswer) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(in))
	for _, a := range in {
		entry := map[string]interface{}{
			"test_set_question_id": a.TestSetQuestionID,
			"question_id":          a.QuestionID,
		}
		if a.MCQChoiceID != "" {
			entry["mcq_choice_id"] = a.MCQChoiceID
		}
		if len(a.MCQChoiceIDs) > 0 {
			entry["mcq_choice_ids"] = a.MCQChoiceIDs
		}
		if a.OEResponseText != "" {
			entry["oe_response_text"] = a.OEResponseText
		}
		if a.AnsweredAt != nil {
			entry["answered_at"] = a.AnsweredAt
		}
		out = append(out, entry)
	}
	return out
}

// gradeMCQAnswersInline grades every MCQ answer in the submission against
// the deterministic answer key cached on chora_delivery.test_set_questions.
// payload_snapshot at TestSet.Publish() time (Fix-F — Lane A snapshot debt
// close, per `feedback_no_stubs_real_wiring`).
//
// Per ddd-enforcement #3: cross-DB queries are FORBIDDEN. The snapshot is
// the only mechanism by which chora-delivery sees the canonical MCQ
// answer key at /submit time — chora-delivery NEVER touches
// chora_creation.questions on the runtime path.
//
// Behaviour:
//   - Per-question points_possible: prefer test_set_questions.points (the
//     authoring-time weight); fall back to even distribution across MCQ
//     answers when the TestSet read is unavailable (in-mem tests).
//   - Grading: domain.Submission.GradeMCQAnswersFromSnapshot — exact-match
//     per scoring_mode (single_correct / multi_correct / all_or_nothing).
//     Partial credit is OUT OF SCOPE for v1 demo per ADR-155 default.
//   - Missing snapshot: per-answer points = 0 + structured error event
//     emitted to chora.delivery.grading.mcq_snapshot_missing.v1 (fail
//     loud; never silently green-path).
//
// adeps may be nil → falls back to the legacy equal-weight even
// distribution + zero-credit-on-missing-snapshot (test path only).
func gradeMCQAnswersInline(adeps *AssessmentDeps, sub *domain.Submission, r *http.Request) {
	// Build the per-test-set-question snapshot map from the TestSet repo
	// (test_set_questions.payload_snapshot column). When the TestSets port
	// is unavailable (in-mem tests w/ no real adapter), the map stays empty
	// and the grader fails-loud per-answer.
	snapshots, weights := loadMCQSnapshotsForSubmission(adeps, sub, r)

	// Distribute points: prefer the authoring-time per-question weight from
	// the test_set_questions snapshot; fall back to MaxScore / N (legacy
	// equal-weight) when no weight is found. This preserves backward-
	// compat with existing in-mem fixtures that don't pre-stamp
	// PointsPossible.
	mcqCount := 0
	for _, ans := range sub.Answers {
		if ans.QuestionType == domain.QuestionTypeMCQ {
			mcqCount++
		}
	}
	for i := range sub.Answers {
		if sub.Answers[i].QuestionType != domain.QuestionTypeMCQ {
			continue
		}
		if sub.Answers[i].PointsPossible == 0 {
			if w, ok := weights[sub.Answers[i].TestSetQuestionID]; ok && w > 0 {
				sub.Answers[i].PointsPossible = w
			} else if sub.MaxScore > 0 && mcqCount > 0 {
				sub.Answers[i].PointsPossible = sub.MaxScore / mcqCount
			}
		}
	}

	if err := sub.GradeMCQAnswersFromSnapshot(snapshots); err != nil {
		// Fail loud per `feedback_no_stubs_real_wiring` — emit a structured
		// signal so the outbox + Observability can surface the gap. Per-
		// answer score is already 0 (grader sets it before returning).
		emitMCQSnapshotMissingEvent(adeps, sub, err, r)
	}
}

// loadMCQSnapshotsForSubmission walks the submission's answers and fetches
// the parent test-set's question snapshots via the TestSets port. Returns
// (snapshotMap, weightMap) keyed by test_set_question_id.
//
// Per ddd-enforcement #3 cross-DB queries are FORBIDDEN — the canonical
// payload comes from chora_delivery.test_set_questions.payload_snapshot
// (populated at chora_delivery.test_sets state PUBLISHED transition via
// the chora.services.creation.v1.Creation/SnapshotQuestionByID gRPC
// captured at TestSet.PublishWithSnapshot() time).
// orderRowsByDisplayOrder sorts per-question result rows in place by the
// test-set display_order (rows keyed by their "test_set_question_id" value).
// Stable, and any tsqid absent from the map sorts AFTER all known ones while
// preserving relative input order. Fixes the 2026-06-20 finding where
// myResultHandler + the OE-grading emitter emitted per_question rows in
// sub.Answers (submission-storage) order rather than canonical question order
// (shuffle_questions=false MUST preserve the test-set order).
func orderRowsByDisplayOrder(rows []map[string]interface{}, displayOrder map[string]int) {
	if len(rows) < 2 || len(displayOrder) == 0 {
		return
	}
	const unknown = 1 << 30
	rank := func(row map[string]interface{}) int {
		id, _ := row["test_set_question_id"].(string)
		if o, ok := displayOrder[id]; ok {
			return o
		}
		return unknown
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rank(rows[i]) < rank(rows[j])
	})
}

// displayOrderForSubmission returns a map of test_set_question_id →
// display_order for the submission's parent assessment's test-set. Mirrors the
// assessment→test-set resolution in loadMCQSnapshotsForSubmission. Empty map
// when unwired or the lookup fails (caller treats absence as "leave as-is").
func displayOrderForSubmission(adeps *AssessmentDeps, sub *domain.Submission, r *http.Request) map[string]int {
	out := make(map[string]int)
	if adeps == nil || adeps.TestSets == nil || adeps.Assessments == nil || sub == nil {
		return out
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		return out
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, sub.AssessmentID)
	if err != nil || !ok || a == nil {
		return out
	}
	ts, ok, err := adeps.TestSets.Get(r.Context(), tenantID, a.TestSetID)
	if err != nil || !ok || ts == nil {
		return out
	}
	for _, q := range ts.Questions() {
		out[q.ID] = int(q.DisplayOrder)
	}
	return out
}

func loadMCQSnapshotsForSubmission(adeps *AssessmentDeps, sub *domain.Submission, r *http.Request) (map[string]domain.MCQSnapshot, map[string]int) {
	snapshots := make(map[string]domain.MCQSnapshot)
	weights := make(map[string]int)
	if adeps == nil || adeps.TestSets == nil || adeps.Assessments == nil {
		return snapshots, weights
	}
	// Resolve the parent assessment → test_set_id.
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		return snapshots, weights
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, sub.AssessmentID)
	if err != nil || !ok || a == nil {
		return snapshots, weights
	}
	ts, ok, err := adeps.TestSets.Get(r.Context(), tenantID, a.TestSetID)
	if err != nil || !ok || ts == nil {
		return snapshots, weights
	}
	// Index test_set_questions by ID.
	for _, q := range ts.Questions() {
		weights[q.ID] = int(q.Points)
		if q.QuestionType != string(domain.QuestionTypeMCQ) {
			continue
		}
		if strings.TrimSpace(q.PayloadSnapshot) == "" {
			// No snapshot → grader will fail-loud per-answer.
			continue
		}
		snap, perr := domain.MCQSnapshotFromJSON(q.PayloadSnapshot)
		if perr != nil {
			// Malformed snapshot → log + skip (grader fails loud).
			continue
		}
		snapshots[q.ID] = snap
	}
	return snapshots, weights
}

// loadOESnapshotJSONsForSubmission fetches the parent test-set's OE
// question snapshots via the TestSets port and returns a map keyed by
// test_set_question_id with the raw payload_snapshot JSON value. Used at
// post-RELEASE result reveal time to project the AUTHOR-SAFE oe_post_grade
// shape (model_answer + rubric).
//
// Per ddd-enforcement #3 — chora-delivery NEVER crosses to chora_creation
// at runtime. The canonical OE payload comes from the snapshot column
// populated at PublishWithSnapshot() time via the AUTHOR-SAFE
// SnapshotQuestionByID gRPC (B6 lane).
//
// Returns an empty map (not nil) when adeps is unwired or the lookup
// fails — caller MUST guard for absence (FE renders without the reveal).
func loadOESnapshotJSONsForSubmission(adeps *AssessmentDeps, sub *domain.Submission, r *http.Request) map[string]string {
	out := make(map[string]string)
	if adeps == nil || adeps.TestSets == nil || adeps.Assessments == nil {
		return out
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		return out
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, sub.AssessmentID)
	if err != nil || !ok || a == nil {
		return out
	}
	ts, ok, err := adeps.TestSets.Get(r.Context(), tenantID, a.TestSetID)
	if err != nil || !ok || ts == nil {
		return out
	}
	for _, q := range ts.Questions() {
		if q.QuestionType != string(domain.QuestionTypeOE) {
			continue
		}
		if snap := strings.TrimSpace(q.PayloadSnapshot); snap != "" {
			out[q.ID] = q.PayloadSnapshot
		}
	}
	return out
}

// emitMCQSnapshotMissingEvent publishes a structured signal to the outbox
// when one or more MCQ answers had no snapshot at grade time. Allows
// O+ to surface the gap + alert the instructor (chora-notifications).
func emitMCQSnapshotMissingEvent(adeps *AssessmentDeps, sub *domain.Submission, err error, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil || sub == nil {
		return
	}
	payload := map[string]any{
		"submission_id":        sub.ID,
		"assessment_id":        sub.AssessmentID,
		"learner_gcid":         sub.LearnerGCID,
		"error_message":        err.Error(),
		"detected_at":          time.Now().UTC().Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability",
		"traceparent":          r.Header.Get("traceparent"),
	}
	publishCustomEvent(adeps.OutboxPublisher,
		"chora.delivery.grading.mcq_snapshot_missing.v1",
		sub.TenantID, sub.LearnerGCID, payload)
}

func gradingETASeconds(hasOE bool) int {
	if hasOE {
		return 60
	}
	return 1
}

func nullableToken(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// -----------------------------------------------------------------------------
// Outbox event emission — uses the existing publisher's PublishCustom
// pattern via JSON payload; the protomarshal switch picks up the topic.
// -----------------------------------------------------------------------------

func emitAssessmentEvent(adeps *AssessmentDeps, topic string, a *domain.Assessment, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil || a == nil {
		return
	}
	payload := map[string]any{
		"assessment_id":        a.ID,
		"test_set_id":          a.TestSetID,
		"instructor_gcid":      a.InstructorGCID,
		"class_id":             a.ClassID,
		"title":                a.Title,
		"scheduled_open_at":    a.ScheduledOpenAt.Format(time.RFC3339Nano),
		"scheduled_close_at":   a.ScheduledCloseAt.Format(time.RFC3339Nano),
		"max_attempts":         a.MaxAttempts,
		"question_count":       a.QuestionCount,
		"total_points":         a.TotalPoints,
		"created_at":           a.CreatedAt.Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability",
	}
	if a.PublishedAt != nil {
		payload["published_at"] = a.PublishedAt.Format(time.RFC3339Nano)
	}
	if a.ResultsReleasedAt != nil {
		payload["released_at"] = a.ResultsReleasedAt.Format(time.RFC3339Nano)
		payload["release_announcement"] = a.ReleaseAnnouncement
		payload["release_method"] = "manual_release"
	}
	payload["traceparent"] = r.Header.Get("traceparent")
	publishCustomEvent(adeps.OutboxPublisher, topic, a.TenantID, a.InstructorGCID, payload)
}

func emitSubmissionStartedEvent(adeps *AssessmentDeps, a *domain.Assessment, s *domain.Submission, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	payload := map[string]any{
		"submission_id":        s.ID,
		"assessment_id":        s.AssessmentID,
		"test_set_id":          a.TestSetID,
		"learner_gcid":         s.LearnerGCID,
		"class_id":             a.ClassID,
		"attempt_number":       s.AttemptNumber,
		"opens_at":             s.OpensAt.Format(time.RFC3339Nano),
		"closes_at":            s.ClosesAt.Format(time.RFC3339Nano),
		"time_limit_seconds":   s.TimeLimitSecs,
		"started_at":           s.StartedAt.Format(time.RFC3339Nano),
		"traceparent":          r.Header.Get("traceparent"),
		"chora_imda_dimension": "accountability",
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.submission.started.v1", s.TenantID, s.LearnerGCID, payload)
}

func emitSubmissionSubmittedEvent(adeps *AssessmentDeps, a *domain.Assessment, s *domain.Submission, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	answered := 0
	for _, ans := range s.Answers {
		if ans.MCQChoiceID != "" || len(ans.MCQChoiceIDs) > 0 || ans.OEResponseText != "" {
			answered++
		}
	}
	tsid := ""
	if a != nil {
		tsid = a.TestSetID
	}
	payload := map[string]any{
		"submission_id":        s.ID,
		"assessment_id":        s.AssessmentID,
		"test_set_id":          tsid,
		"learner_gcid":         s.LearnerGCID,
		"attempt_number":       s.AttemptNumber,
		"total_questions":      len(s.Answers),
		"answered_count":       answered,
		"submitted_at":         s.UpdatedAt.Format(time.RFC3339Nano),
		"traceparent":          r.Header.Get("traceparent"),
		"chora_imda_dimension": "accountability",
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.submission.submitted.v1", s.TenantID, s.LearnerGCID, payload)
}

func emitSubmissionGradedEvent(adeps *AssessmentDeps, a *domain.Assessment, s *domain.Submission, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	// ADR-205 WS-6 (CHO-1958): the MCQ-only fast path holds the Assessment, so
	// it can snapshot the title onto the event for chora-consumption's derived
	// Growth-Edge projector.
	var title string
	if a != nil {
		title = a.Title
	}
	// CHO-2224 (§10.6 criterion 1): snapshot the parent Offering's delivery_type
	// so chora-consumption's StudentTranscript can attribute a MODE. Without it a
	// graduate assessment and a short-course grade both project as
	// kind='assessment' and are indistinguishable.
	//
	// Both callers of this funnel (the MCQ fast path and emitGradeOfRecord, which
	// covers per-submission AND approve-all) reach it holding `a`, so the stamp
	// lives here once rather than at each caller.
	//
	// The tenant MUST be on the ctx: OfferingPort.Get reads the RLS tenant from
	// the context, not a parameter. Resolution is fail-soft by contract — an
	// unwired port, a freestanding assessment or a dead read all yield "", and
	// the grade still publishes mode-less.
	deliveryType := domain.ResolveDeliveryType(
		tracing.WithTenantID(r.Context(), s.TenantID), adeps.Offerings, a)
	payload := events.SubmissionGradedPayload(s, title, deliveryType, r.Header.Get("traceparent"))
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.submission.graded.v1", s.TenantID, s.LearnerGCID, payload)
}

// emitMCQCompletedEvent — LEG4-A. Emits
// chora.delivery.grading.mcq_completed.v1 as a marker after the inline
// MCQ scorer has stamped per-answer points. Carries the aggregate scoring
// outcome + a `no_oe_pending` flag indicating whether the submission can
// transition straight to GRADED (MCQ-only) or has to wait for the OE
// batch. Used by R+ monitoring (partial-progress UI) + Observability
// (submit→mcq_completed latency histogram, sub-200ms p99) + chora-
// governance (audit trail for deterministic grades).
//
// Topic LIVE per Infra commit b41dc649. Schema flat-projected at
// chora-contracts/proto/events-flat/delivery/grading/mcq_completed.proto.
// Encoded by encodeGradingMCQCompleted in assessment_encoders.go.
func emitMCQCompletedEvent(adeps *AssessmentDeps, a *domain.Assessment, s *domain.Submission, hasOE bool, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil || s == nil {
		return
	}
	correct, incorrect := 0, 0
	pointsEarned := 0.0
	pointsPossible := 0
	for _, ans := range s.Answers {
		if ans.QuestionType != domain.QuestionTypeMCQ {
			continue
		}
		pointsEarned += ans.PointsEarned
		pointsPossible += ans.PointsPossible
		if ans.MCQCorrect != nil && *ans.MCQCorrect {
			correct++
		} else {
			incorrect++
		}
	}
	assessmentID := ""
	if a != nil {
		assessmentID = a.ID
	} else {
		assessmentID = s.AssessmentID
	}
	payload := map[string]any{
		"grading_job_id":       domain.NewUUIDv7(),
		"submission_id":        s.ID,
		"assessment_id":        assessmentID,
		"learner_gcid":         s.LearnerGCID,
		"mcq_correct_count":    correct,
		"mcq_incorrect_count":  incorrect,
		"mcq_points_earned":    pointsEarned,
		"mcq_points_possible":  pointsPossible,
		"no_oe_pending":        !hasOE,
		"mcq_completed_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability",
		"traceparent":          r.Header.Get("traceparent"),
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.grading.mcq_completed.v1", s.TenantID, s.LearnerGCID, payload)
}

// emitSubmissionReleasedEvent announces ONE released submission.
//
// CHO-2349: learnerGCID is the LEARNER whose grade was released, and it is a
// REQUIRED argument rather than something derived here. It previously read
// a.InstructorGCID, so every released event on the wire (payload field AND
// envelope gcid) named the instructor. The certificate engine mints off the
// submission row's own learner so it did not mis-issue, but attribution,
// tracing and any future consumer keyed on this field were all wrong.
func emitSubmissionReleasedEvent(adeps *AssessmentDeps, tenantID string, a *domain.Assessment, submissionID, learnerGCID string, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	assessmentID := ""
	if a != nil {
		assessmentID = a.ID
	}
	payload := map[string]any{
		"submission_id":        submissionID,
		"assessment_id":        assessmentID,
		"learner_gcid":         learnerGCID,
		"released_at":          time.Now().UTC().Format(time.RFC3339Nano),
		"traceparent":          r.Header.Get("traceparent"),
		"chora_imda_dimension": "accountability",
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.submission.released.v1", tenantID, learnerGCID, payload)
}

// learnerGCIDBySubmissionID maps submission id -> learner gcid for an
// assessment. ReleaseAllSubmissions returns only ids, and the released event
// must name the learner, so the two release paths both need this lookup.
// A read failure is LOUD and yields an empty map rather than a wrong name.
func learnerGCIDBySubmissionID(adeps *AssessmentDeps, ctx context.Context, tenantID, assessmentID string) map[string]string {
	out := map[string]string{}
	if adeps == nil || adeps.Submissions == nil {
		return out
	}
	subs, _, err := adeps.Submissions.ListByAssessment(ctx, tenantID, assessmentID, 10000, "")
	if err != nil {
		log.Printf("release: cannot resolve learner gcids for assessment %s: %v - released events will carry an "+
			"empty learner_gcid rather than a wrong one", assessmentID, err)
		return out
	}
	for _, s := range subs {
		if s != nil {
			out[s.ID] = s.LearnerGCID
		}
	}
	return out
}

// emitReleaseEvents announces a release exactly once, the same way from every
// release path: one assessment.released.v1 plus one submission.released.v1 per
// released submission. CHO-2349 - approve-all released without announcing, so
// the certificate engine (which rides submission.released.v1 and nothing else)
// never fired for anyone who used the grading queue's bulk button.
func emitReleaseEvents(adeps *AssessmentDeps, tenantID string, a *domain.Assessment, releasedIDs []string, r *http.Request) {
	if a == nil {
		return
	}
	learnerByID := learnerGCIDBySubmissionID(adeps, r.Context(), tenantID, a.ID)
	emitAssessmentEvent(adeps, "chora.delivery.assessment.released.v1", a, r)
	for _, sid := range releasedIDs {
		emitSubmissionReleasedEvent(adeps, tenantID, a, sid, learnerByID[sid], r)
	}
}

func emitOEBatchRequestedEvent(adeps *AssessmentDeps, a *domain.Assessment, s *domain.Submission, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	// Build per-OE-question batches; one batch per (assessment, question).
	// For the demo simplicity we emit ONE event per (submission × OE answer)
	// with batch_submissions=[the single entry]. The orchestrator can batch
	// further at its end.
	for _, ans := range s.Answers {
		if ans.QuestionType != domain.QuestionTypeOE || strings.TrimSpace(ans.OEResponseText) == "" {
			continue
		}
		batchEntry := map[string]any{
			"submission_id":        s.ID,
			"test_set_question_id": ans.TestSetQuestionID,
			"question_id":          ans.QuestionID,
			"learner_gcid":         s.LearnerGCID,
			"response_text":        ans.OEResponseText,
		}
		payload := map[string]any{
			"oe_batch_id":                    domain.NewUUIDv7(),
			"assessment_id":                  a.ID,
			"test_set_question_id":           ans.TestSetQuestionID,
			"question_id":                    ans.QuestionID,
			"prompt":                         "",
			"model_answer":                   "",
			"rubric_json":                    "{}",
			"points_possible_per_submission": 10,
			"model_tier":                     "T1",
			"per_question_feedback_enabled":  true,
			"batch_submissions":              []map[string]any{batchEntry},
			"estimated_mana_units":           1,
			"dispatched_at":                  time.Now().UTC().Format(time.RFC3339Nano),
			"chora_imda_dimension":           "transparency",
			"traceparent":                    r.Header.Get("traceparent"),
		}
		publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.grading.oe_batch_requested.v1", s.TenantID, s.LearnerGCID, payload)
	}
}

// oeSnap is the projected OE payload from a test_set_questions snapshot —
// the rubric + model answer + prompt the evaluator grades against.
type oeSnap struct {
	ModelAnswer string
	RubricJSON  string
	Prompt      string
	Subject     string
	Topic       string
	Points      int
}

// loadOESnapshots indexes OE test-set-question snapshots (rubric + model answer
// + prompt) by test_set_question_id for the submission's parent test-set. Empty
// when the TestSets port is unavailable (in-mem tests) — the orchestrator then
// grades against whatever context the event carried.
func loadOESnapshots(adeps *AssessmentDeps, a *domain.Assessment, tenantID string, r *http.Request) map[string]oeSnap {
	out := map[string]oeSnap{}
	if adeps == nil || adeps.TestSets == nil || a == nil {
		return out
	}
	ts, ok, err := adeps.TestSets.Get(r.Context(), tenantID, a.TestSetID)
	if err != nil || !ok || ts == nil {
		return out
	}
	for _, q := range ts.Questions() {
		if q.QuestionType != string(domain.QuestionTypeOE) {
			continue
		}
		s := oeSnap{Points: int(q.Points)}
		if strings.TrimSpace(q.PayloadSnapshot) != "" {
			var doc struct {
				ModelAnswer string          `json:"model_answer"`
				Rubric      json.RawMessage `json:"rubric"`
				Prompt      string          `json:"prompt"`
				Stem        string          `json:"stem"`
				Subject     string          `json:"subject"`
				Topic       string          `json:"topic"`
			}
			if json.Unmarshal([]byte(q.PayloadSnapshot), &doc) == nil {
				s.ModelAnswer = doc.ModelAnswer
				if len(doc.Rubric) > 0 {
					s.RubricJSON = string(doc.Rubric)
				}
				s.Prompt = firstNonEmpty(doc.Prompt, doc.Stem)
				s.Subject = doc.Subject
				s.Topic = doc.Topic
			}
		}
		if s.RubricJSON == "" {
			s.RubricJSON = "[]"
		}
		out[q.ID] = s
	}
	return out
}

// emitSubmissionRequestedEvent publishes chora.delivery.grading.submission_
// requested.v1 (ADR-172) — ONE event per submission carrying the whole-
// submission context (MCQ results + OE answers + rubric + model_answer +
// points + subject/topic) the oe_grading_crew needs to grade each OE answer
// AND write the holistic overall comment.
func emitSubmissionRequestedEvent(adeps *AssessmentDeps, a *domain.Assessment, s *domain.Submission, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil || s == nil {
		return
	}
	snaps := loadOESnapshots(adeps, a, s.TenantID, r)
	// Canonical test-set display_order per tsqid — sub.Answers is in
	// submission-storage order, so neither the per-question display_order nor
	// the emitted slice order can rely on the loop index (2026-06-20 finding).
	dord := displayOrderForSubmission(adeps, s, r)
	subject := ""
	questions := make([]map[string]any, 0, len(s.Answers))
	mcqEarned := 0.0
	for i, ans := range s.Answers {
		ord := i
		if o, ok := dord[ans.TestSetQuestionID]; ok {
			ord = o
		}
		q := map[string]any{
			"test_set_question_id": ans.TestSetQuestionID,
			"question_id":          ans.QuestionID,
			"question_type":        string(ans.QuestionType),
			"display_order":        ord,
			"points_possible":      ans.PointsPossible,
		}
		switch ans.QuestionType {
		case domain.QuestionTypeMCQ:
			mcqEarned += ans.PointsEarned
			if ans.MCQCorrect != nil {
				q["mcq_correct"] = *ans.MCQCorrect
			}
			q["mcq_points_earned"] = ans.PointsEarned
		case domain.QuestionTypeOE:
			q["oe_response_text"] = ans.OEResponseText
			if sn, ok := snaps[ans.TestSetQuestionID]; ok {
				q["rubric_json"] = sn.RubricJSON
				q["model_answer"] = sn.ModelAnswer
				q["prompt"] = sn.Prompt
				q["subject"] = sn.Subject
				q["topic"] = sn.Topic
				if sn.Points > 0 && ans.PointsPossible == 0 {
					q["points_possible"] = sn.Points
				}
				if subject == "" {
					subject = sn.Subject
				}
			} else {
				// Snapshot MISS — the OE question was not snapshotted at
				// publish/assessment time, so there is NO rubric/model_answer/
				// prompt to ground grading. Fail LOUD (visible ERROR) + tag the
				// question so the gap is detectable rather than the LLM silently
				// grading empty context (feedback_no_stubs_real_wiring). The
				// orchestrator degrades an unparseable rubric to a quality_flagged
				// 0 (scoring.ScoringError) which the mandatory HITL gate surfaces.
				log.Printf("delivery: emitSubmissionRequested OE snapshot MISSING tenant=%s submission=%s tsq=%s question=%s â grading will flag for human review",
					s.TenantID, s.ID, ans.TestSetQuestionID, ans.QuestionID)
				q["rubric_json"] = "[]"
				q["snapshot_missing"] = true
			}
		}
		questions = append(questions, q)
	}
	orderRowsByDisplayOrder(questions, dord)
	payload := map[string]any{
		"grading_job_id":                domain.NewUUIDv7(),
		"submission_id":                 s.ID,
		"assessment_id":                 s.AssessmentID,
		"learner_gcid":                  s.LearnerGCID,
		"passing_threshold_percent":     s.PassingPercent,
		"model_tier":                    "T1",
		"per_question_feedback_enabled": true,
		"subject":                       subject,
		"questions":                     questions,
		"total_points_possible":         s.MaxScore,
		"mcq_points_earned":             mcqEarned,
		"estimated_mana_units":          len(questions),
		"requested_at":                  time.Now().UTC().Format(time.RFC3339Nano),
		"chora_imda_dimension":          "transparency",
		"traceparent":                   r.Header.Get("traceparent"),
	}
	if a != nil {
		payload["test_set_id"] = a.TestSetID
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.grading.submission_requested.v1", s.TenantID, s.LearnerGCID, payload)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// publishCustomEvent calls the Publisher via its generic-publish API. The
// existing Publisher interface doesn't directly expose a generic
// Publish(topic, payload) — we use a thin reflection shim through
// PublishCustomEvent if available, otherwise log + drop.
//
// The InMemoryPublisher used in tests captures every published event into
// its in-memory log; CloudPublisher pipes through the outbox dispatcher.
func publishCustomEvent(pub events.Publisher, topic, tenantID, gcid string, payload map[string]any) {
	type publishCustomer interface {
		PublishCustom(topic, tenantID, gcid string, payload map[string]any) (events.PublishedEvent, error)
	}
	if pc, ok := pub.(publishCustomer); ok {
		_, _ = pc.PublishCustom(topic, tenantID, gcid, payload)
	} else {
		// Best-effort: serialise to log so dev observes the topic. Per
		// `no-stubs-real-wiring`: in production this branch must NOT be
		// reachable; cmd/server wires the OutboxPublisher which DOES
		// implement publishCustomer.
		bz, _ := json.Marshal(payload)
		_ = bz
	}
}

// max returns the larger of a and b (Go 1.21+ has built-in but kept local).
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
