// me_course_media_handler.go — ADR-185 learner-media signed-GET seam.
//
//	GET /v1/me/courses/{course_id}/content/media-urls
//
// Learner-scoped, ENROLMENT-GATED resolution of a course's uploaded-media
// gs:// refs to fresh short-lived V4 signed GET URLs, keyed by content item_id.
//
// WHY THIS EXISTS: the learner curriculum read (/api/v1/me/courses/{id}/content)
// is a chora-consumption projection that returns gs:// refs RAW — consumption
// cannot sign chora-delivery's private bucket (cross-DB forbidden; cross-domain
// reads are events-only). chora-delivery owns the bucket + GSA, so it signs;
// the BFF (chora-gateway MeCourseContent) fans this out in parallel with the
// projection read and swaps gs://→signed `ref` by item_id. The endpoint accepts
// NO caller-supplied refs (it reads its own course-content aggregate), so there
// is nothing to spoof; the signer's foreign-bucket refusal is a second guard.
//
// Identity: lowercase `gcid` (401 if missing) + X-Tenant-Id (tenantRequired).
package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	mediaadapter "github.com/apollo-chora/chora-delivery/internal/adapter/objectmedia"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// LearnerAssessmentLister is the narrow read this seam needs to resolve an
// `assessment` curriculum ref. domain.AssessmentRepo satisfies it structurally;
// depending on the narrow port keeps the media seam free of the full repo.
type LearnerAssessmentLister interface {
	ListVisibleToLearner(ctx context.Context, tenantID, learnerGCID string, pageSize int, pageToken string) ([]*domain.Assessment, string, error)
}

// learnerAssessmentScanCap bounds the learner-assessment scan used to match a
// curriculum test_set_id. The underlying repo does not implement a page token,
// so this is a single bounded read: a learner holding MORE than this many
// visible assessments could miss a match, which is logged rather than hidden.
const learnerAssessmentScanCap = 200

// resolveLearnerAssessments fills `into` with item_id -> assessment_id for every
// `assessment` curriculum item whose test_set_id ref matches an assessment the
// learner is in the cohort for (CHO-2351).
//
// Contract notes:
//   - No assessment items ⇒ no query at all (an atom-only course pays nothing).
//   - A lister outage or an unmatched test-set leaves the ref RAW. The A+ CTA
//     then still fails visibly rather than resolving to another learner's
//     assessment, which is the worse failure.
//   - Matching is strictly on test_set_id. "First visible assessment wins" would
//     hand the learner an unrelated paper.
func resolveLearnerAssessments(
	ctx context.Context,
	items []*cc.ContentItem,
	tenantID, gcid string,
	assessments LearnerAssessmentLister,
	into map[string]string,
) {
	if assessments == nil {
		return
	}
	wanted := make(map[string]struct{})
	for _, it := range items {
		if it.Kind == cc.KindAssessment && strings.TrimSpace(it.Ref) != "" {
			wanted[it.Ref] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return // nothing to resolve; do not query
	}

	list, _, err := assessments.ListVisibleToLearner(ctx, tenantID, gcid, learnerAssessmentScanCap, "")
	if err != nil {
		log.Printf("meCourseMedia: learner-assessment lookup failed (tenant=%s) - leaving %d assessment ref(s) raw: %v",
			tenantID, len(wanted), err)
		return
	}
	if len(list) >= learnerAssessmentScanCap {
		log.Printf("meCourseMedia: learner-assessment scan hit the %d cap; a curriculum assessment ref may be left unresolved",
			learnerAssessmentScanCap)
	}

	// test_set_id -> assessment_id. The repo returns newest-first, so the first
	// hit for a test-set is the most recent assessment built from it.
	byTestSet := make(map[string]string, len(list))
	for _, a := range list {
		if a == nil || a.TestSetID == "" {
			continue
		}
		if _, seen := byTestSet[a.TestSetID]; !seen {
			byTestSet[a.TestSetID] = a.ID
		}
	}
	for _, it := range items {
		if it.Kind != cc.KindAssessment {
			continue
		}
		if aid, ok := byTestSet[it.Ref]; ok {
			into[it.ItemID] = aid
		}
	}
}

// meCourseMediaHandler serves GET /v1/me/courses/{course_id}/content/media-urls.
// enroll gates on the caller's active enrolments; content holds delivery's own
// course-content service + the course-media signer (MediaResolver).
//
// CHO-2351 - it ALSO resolves `assessment` items. Their ref is a TEST_SET_ID by
// design (see course_content.go: "atom_id / test_set_id / classroom_id / url"),
// but the A+ curriculum CTA routes it as /a/me/assessments/{ref}, which expects
// an ASSESSMENT_ID, so the learner hit "We couldn't find this assessment."
// Resolution belongs here, not in the FE: only chora-delivery owns assessments,
// the learner's own assessment list does not expose test_set_id, and the choice
// is learner-scoped (which offering's assessment THIS learner may open). Both
// resolutions share the one `resolved` map so the already-deployed BFF merge
// (chora-gateway MeCourseContent, swap ref by item_id) needs no change.
func meCourseMediaHandler(
	enroll domain.EnrollmentPort,
	content *CourseContentDeps,
	assessments LearnerAssessmentLister,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Path: /v1/me/courses/{course_id}/content/media-urls
		rest := strings.TrimPrefix(r.URL.Path, "/v1/me/courses/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		if len(parts) != 3 || parts[0] == "" || parts[1] != "content" || parts[2] != "media-urls" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		courseID := parts[0]

		// Identity — the BFF stamps the lowercase `gcid`; accept the canonical
		// X-Chora-GCID fallback for parity with the content handler.
		gcid := strings.TrimSpace(r.Header.Get("gcid"))
		if gcid == "" {
			gcid = strings.TrimSpace(r.Header.Get("X-Chora-GCID"))
		}
		if gcid == "" {
			writeError(w, http.StatusUnauthorized, "gcid header required")
			return
		}
		tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
		ctx := tracing.WithTenantID(r.Context(), tenantID)

		// Enrolment gate (authz before infra) — only learners enrolled in this
		// course may sign its media. Matters for paid courses.
		entries, err := enroll.ListByGCID(ctx, tenantID, gcid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "enrollment lookup failed")
			return
		}
		enrolled := false
		for _, e := range entries {
			if e.CourseID == courseID {
				enrolled = true
				break
			}
		}
		if !enrolled {
			writeError(w, http.StatusForbidden, "not enrolled in this course")
			return
		}

		// Signer must be wired — fail loud (mirrors the upload-url 503).
		if content == nil || content.MediaResolver == nil || content.Svc == nil {
			writeError(w, http.StatusServiceUnavailable, "course-media signer not wired (set COURSE_MEDIA_BUCKET)")
			return
		}

		// Read delivery's OWN course-content aggregate. A course with no
		// curriculum yet is an empty resolution, not an error.
		got, err := content.Svc.Get(ctx, tenantID, courseID)
		if err != nil {
			if errors.Is(err, cc.ErrNotFound) {
				writeJSON(w, http.StatusOK, map[string]any{"resolved": map[string]string{}})
				return
			}
			writeError(w, http.StatusInternalServerError, "course content read failed")
			return
		}

		// Collect gs:// refs, sign them in one batch, key the result by item_id.
		var gsRefs []string
		for _, it := range got.Items {
			if strings.HasPrefix(it.Ref, "gs://") {
				gsRefs = append(gsRefs, it.Ref)
			}
		}
		resolved := make(map[string]string, len(gsRefs))
		signedAny := false
		if len(gsRefs) > 0 {
			byRef := content.MediaResolver.ResolveDownloadURLs(ctx, tenantID, gsRefs)
			for _, it := range got.Items {
				if signed, ok := byRef[it.Ref]; ok {
					resolved[it.ItemID] = signed
					signedAny = true
				}
			}
		}

		// CHO-2351 - map each `assessment` item's test_set_id ref to the id of
		// the assessment this learner may actually open. Best-effort, exactly
		// like the media path: on an outage or a miss the ref is left RAW so the
		// CTA fails visibly, never silently pointing at the wrong assessment.
		resolveLearnerAssessments(ctx, got.Items, tenantID, gcid, assessments, resolved)

		out := map[string]any{"resolved": resolved}
		if signedAny {
			// Refetch hint - applies to the SIGNED media only; a resolved
			// assessment id is a durable UUID and never expires.
			out["expires_at"] = time.Now().UTC().Add(mediaadapter.DefaultCourseMediaTTL()).Format(time.RFC3339)
		}
		writeJSON(w, http.StatusOK, out)
	}
}
