// course_content_handler.go — REST surface for the heterogeneous course
// curriculum (CHO-1612). A Course COLLECTS ordered, typed content items
// (atom/video/youtube/document/live_classroom/assessment); it never owns
// atoms. Every mutation persists + emits chora.delivery.course.content_composed.v1.
//
//	GET    /api/v1/courses/{course_id}/content             list
//	POST   /api/v1/courses/{course_id}/content             append item
//	POST   /api/v1/courses/{course_id}/content/reorder     reorder (permutation)
//	DELETE /api/v1/courses/{course_id}/content/{item_id}   remove item
//
// Identity: X-Tenant-Id header (required). Mounted by the courses dispatcher.
// RBAC (CHO-2233): every mutation branch (append / reorder / upload-url /
// remove) requires an instructor-level role via hasInstructorOrAdmin
// (x-mesh-user-roles, fail-closed). The read path stays open: the learner
// read-path is chora-consumption's projection, and this GET serves the
// instructor editor plus tenant-admin go-live checks.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	mediaadapter "github.com/apollo-chora/chora-delivery/internal/adapter/objectmedia"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

// CourseMediaSigner mints V4 signed PUT URLs for course-content media uploads.
// Implemented by *mediaadapter.CourseMediaSigner; nil when COURSE_MEDIA_BUCKET is
// unset (the upload-url route then 503s — fail-loud, no stub).
type CourseMediaSigner interface {
	SignUpload(ctx context.Context, courseID, tenantID string, in mediaadapter.SignUploadInput) (mediaadapter.SignUploadOutput, error)
	MaxBytes() int64
}

// CourseContentDeps wires the course-content service. Nil-safe: when Svc is nil
// the routes are not mounted (the caller skips registration). Signer +
// MediaResolver are optional — when nil, the upload-url route 503s and gs://
// refs are returned raw on the read-path.
type CourseContentDeps struct {
	Svc *cc.Service
	// Signer mints upload URLs for POST .../content/upload-url.
	Signer CourseMediaSigner
	// MediaResolver swaps gs:// content refs for fresh signed GET URLs on the
	// instructor read-path so the editor can preview uploaded media. The
	// learner read-path is served by chora-consumption (a separate projection)
	// — consumption cannot sign delivery's bucket, tracked as a named follow-up.
	MediaResolver MediaURLResolver
}

// courseContentHandler dispatches the /content subtree under a course.
func courseContentHandler(deps *CourseContentDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
		if tenantID == "" {
			writeError(w, http.StatusBadRequest, "X-Tenant-Id header required")
			return
		}
		// Expect: {course_id}/content[/reorder | /{item_id}]
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/courses/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		// parts[0]=course_id, parts[1]="content", parts[2]=reorder|item_id?
		if len(parts) < 2 || parts[1] != "content" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		courseID := parts[0]
		if courseID == "" {
			writeError(w, http.StatusBadRequest, "course_id required")
			return
		}

		// Mutations carry an actor gcid (the composing instructor) — stamped
		// onto the content_composed event envelope (UUID-typed in the outbox).
		gcid := strings.TrimSpace(r.Header.Get("gcid"))
		if gcid == "" {
			gcid = strings.TrimSpace(r.Header.Get("X-Chora-GCID"))
		}

		// CHO-2233: curriculum mutations are instructor-level writes. The FE
		// surface guard is not an authorization control — this gate must hold
		// on its own (live-proven 2026-07-17: a pure learner appended and
		// deleted curriculum items while bounced off the R+ SPA).
		requireInstructor := func() bool {
			if hasInstructorOrAdmin(r) {
				return true
			}
			writeError(w, http.StatusForbidden, "caller lacks instructor/admin role")
			return false
		}

		switch {
		case len(parts) == 2: // /content
			switch r.Method {
			case http.MethodGet:
				listContent(w, r, deps, tenantID, courseID)
			case http.MethodPost:
				if gcid == "" {
					writeError(w, http.StatusBadRequest, "gcid header required")
					return
				}
				if !requireInstructor() {
					return
				}
				addContent(w, r, deps, tenantID, courseID, gcid)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		case len(parts) == 3 && parts[2] == "reorder": // /content/reorder
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			if gcid == "" {
				writeError(w, http.StatusBadRequest, "gcid header required")
				return
			}
			if !requireInstructor() {
				return
			}
			reorderContent(w, r, deps, tenantID, courseID, gcid)
		case len(parts) == 3 && parts[2] == "upload-url": // /content/upload-url
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			if gcid == "" {
				writeError(w, http.StatusBadRequest, "gcid header required")
				return
			}
			if !requireInstructor() {
				return
			}
			mintContentUploadURL(w, r, deps, tenantID, courseID)
		case len(parts) == 3: // /content/{item_id}
			if r.Method != http.MethodDelete {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			if gcid == "" {
				writeError(w, http.StatusBadRequest, "gcid header required")
				return
			}
			if !requireInstructor() {
				return
			}
			removeContent(w, r, deps, tenantID, courseID, gcid, parts[2])
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}

func listContent(w http.ResponseWriter, r *http.Request, deps *CourseContentDeps, tenantID, courseID string) {
	got, err := deps.Svc.Get(r.Context(), tenantID, courseID)
	if err != nil {
		// A course with no curriculum yet is an EMPTY collection, not an error —
		// return 200 { items: [] } so the instructor editor renders the empty
		// "add item" state rather than a hard error. (The content aggregate is a
		// sub-resource of an existing course; "not found" here means "no items
		// yet", and the content handler is course-existence-agnostic by design.)
		if errors.Is(err, cc.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"course_id": courseID, "items": []any{}})
			return
		}
		writeContentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contentDTO(r.Context(), deps, tenantID, got))
}

// mintContentUploadURL handles POST .../content/upload-url: mints a V4 signed
// PUT URL the FE uploads a video/PDF/image to, returning the gs:// object_ref
// the FE then attaches as a content item. Fail-loud 503 when the signer is
// unwired (COURSE_MEDIA_BUCKET unset).
func mintContentUploadURL(w http.ResponseWriter, r *http.Request, deps *CourseContentDeps, tenantID, courseID string) {
	if deps.Signer == nil {
		writeError(w, http.StatusServiceUnavailable, "course-media signer not wired (set COURSE_MEDIA_BUCKET)")
		return
	}
	var req struct {
		MIME      string `json:"mime"`
		SizeBytes int64  `json:"size_bytes"`
		Filename  string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.MIME) == "" {
		writeError(w, http.StatusBadRequest, "mime is required")
		return
	}
	if req.SizeBytes <= 0 {
		writeError(w, http.StatusBadRequest, "size_bytes must be positive")
		return
	}
	if _, err := mediaadapter.ExtensionForCourseMIME(req.MIME); err != nil {
		writeError(w, http.StatusUnsupportedMediaType, "mime not supported for course media")
		return
	}
	// 413 BEFORE we touch the signer (saves a wasted Sign call).
	if req.SizeBytes > deps.Signer.MaxBytes() {
		writeError(w, http.StatusRequestEntityTooLarge, "size_bytes exceeds the course-media cap")
		return
	}
	out, err := deps.Signer.SignUpload(r.Context(), courseID, tenantID, mediaadapter.SignUploadInput{
		MIME: req.MIME, SizeBytes: req.SizeBytes, Filename: req.Filename,
	})
	if err != nil {
		switch {
		case errors.Is(err, mediaadapter.ErrCourseMediaUnsupportedMIME):
			writeError(w, http.StatusUnsupportedMediaType, "mime not supported for course media")
		case errors.Is(err, mediaadapter.ErrCourseMediaSignerNotWired):
			writeError(w, http.StatusServiceUnavailable, "course-media signer not wired")
		default:
			log.Printf("course_content: mint upload url failed: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to mint course-media upload url")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"upload_url":     out.UploadURL,
		"object_ref":     out.ObjectRef,
		"expires_at":     out.ExpiresAt.UTC().Format(time.RFC3339Nano),
		"max_size_bytes": out.MaxSizeBytes,
	})
}

func addContent(w http.ResponseWriter, r *http.Request, deps *CourseContentDeps, tenantID, courseID, gcid string) {
	var req struct {
		Kind  string `json:"kind"`
		Ref   string `json:"ref"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	out, err := deps.Svc.AddItem(r.Context(), tenantID, courseID, gcid, cc.AddItemParams{
		Kind: cc.Kind(req.Kind), Ref: req.Ref, Title: req.Title,
	})
	if err != nil {
		writeContentErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, contentDTO(r.Context(), deps, tenantID, out))
}

func reorderContent(w http.ResponseWriter, r *http.Request, deps *CourseContentDeps, tenantID, courseID, gcid string) {
	var req struct {
		OrderedItemIDs []string `json:"ordered_item_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	out, err := deps.Svc.Reorder(r.Context(), tenantID, courseID, gcid, req.OrderedItemIDs)
	if err != nil {
		writeContentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contentDTO(r.Context(), deps, tenantID, out))
}

func removeContent(w http.ResponseWriter, r *http.Request, deps *CourseContentDeps, tenantID, courseID, gcid, itemID string) {
	out, err := deps.Svc.RemoveItem(r.Context(), tenantID, courseID, gcid, itemID)
	if err != nil {
		writeContentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contentDTO(r.Context(), deps, tenantID, out))
}

// contentDTO projects the aggregate onto the OpenAPI CourseContent shape,
// resolving gs:// refs to fresh signed GET URLs via the MediaResolver so the
// instructor editor can preview uploaded media. The stored gs:// ref is
// preserved under `object_ref` so a re-save round-trips the durable URI (the
// resolved signed URL is ephemeral and must NOT be persisted).
func contentDTO(ctx context.Context, deps *CourseContentDeps, tenantID string, c *cc.CourseContent) map[string]any {
	// Collect gs:// refs and resolve them in one batched call.
	var gsRefs []string
	for _, it := range c.Items {
		if strings.HasPrefix(it.Ref, "gs://") {
			gsRefs = append(gsRefs, it.Ref)
		}
	}
	var resolved map[string]string
	if len(gsRefs) > 0 && deps != nil && deps.MediaResolver != nil {
		resolved = deps.MediaResolver.ResolveDownloadURLs(ctx, tenantID, gsRefs)
	}

	items := make([]map[string]any, 0, len(c.Items))
	for _, it := range c.Items {
		row := map[string]any{
			"item_id":  it.ItemID,
			"kind":     string(it.Kind),
			"ref":      it.Ref,
			"title":    it.Title,
			"position": it.Position,
		}
		if signed, ok := resolved[it.Ref]; ok {
			// Surface the playable/downloadable signed URL as `ref`, keep the
			// durable gs:// URI under `object_ref` for round-tripping.
			row["object_ref"] = it.Ref
			row["ref"] = signed
		}
		items = append(items, row)
	}
	return map[string]any{"course_id": c.CourseID, "items": items}
}

// writeContentErr maps domain sentinels to HTTP status codes.
func writeContentErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cc.ErrNotFound), errors.Is(err, cc.ErrItemNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, cc.ErrInvalidArgument),
		errors.Is(err, cc.ErrDuplicateItem),
		errors.Is(err, cc.ErrCapExceeded),
		errors.Is(err, cc.ErrInvalidReorder),
		errors.Is(err, cc.ErrDeleted):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("course_content: unexpected error → 500: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
