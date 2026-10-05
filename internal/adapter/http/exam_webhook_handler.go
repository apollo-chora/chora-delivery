// exam_webhook_handler.go: the ADR-193 D1 inbound satellite exam-result HMAC
// webhook receiver (W5 bypass-free slice, CHO-2230).
//
// A separate-deployment satellite (its own platform project, possibly
// fully external to Chora) PUSHES exam results to chora-main. Direction is
// the INVERSE of the exam-administration design doc's section 7 outbound
// spec: the satellite signs, chora-main verifies. The crypto is that spec
// verbatim:
//
//	X-Chora-Signature: sha256=hex(HMAC-SHA256(secret, timestamp + "." + rawBody))
//	X-Chora-Timestamp: unix seconds; reject outside the 300 s replay window
//
// Structure clones chora-payments' Stripe receiver (webhook_handler.go, the
// ADR-188 pattern): raw body capped, HMAC over the RAW bytes, empty secret =
// 503 fail-loud, a dedup gate with RE-DISPATCH on exists-but-unprocessed (the
// WS-2 stranded-credit lesson: silently skipping an unprocessed duplicate
// strands the result forever). One refinement on top of the clone: the
// receipt records the durable exam_results row id (MarkResultRecorded) BEFORE
// the released-event publish, so a retried delivery RESUMES at the publish
// step instead of grading twice; one idempotency_key can never mint two
// result rows.
//
// The verified payload names the target tenant; the handler re-scopes every
// store call through tracing.WithTenantID so RLS binds the write to exactly
// that tenant, and the exam-form lookup (tenant + exam ownership check)
// refuses a payload whose form does not belong to the named tenant/exam. The
// shared secret authenticates the sender; the form check grounds the claim.
// Per-vendor secrets arrive with the ExamVendorProfile aggregate (W5 full
// build, ADR-193 O3); this slice carries ONE inbound secret via
// CHORA_EXAM_WEBHOOK_SECRET (Secret Manager-sourced env, no inline config).
//
// ADR-193 O4a binding honoured: nothing here branches on exam_owner_tenant_id
// (the retired routing proxy); this handler IS the separate_deployment lane
// and the vendor-profile deployment_topology switch lands with that aggregate.
//
// Grading path: the satellite sends a RAW SCORE, never a verdict. chora-main
// grades against its own revision-pinned, exposure-locked ExamForm cut-score
// (ADR-190 D2: the compliance primitive stays on chora-main), writes the
// durable exam_results row (source of truth), and tees the standard
// chora.delivery.exam_result.released.v1 outcome event into the outbox so the
// existing cert + transcript consumers fire unchanged.
package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
)

const (
	// ExamWebhookPath is the inbound receiver route. The gateway-side
	// WithExamWebhookPassthrough (deploy burst) forwards byte-identical to it,
	// mounted OUTSIDE the JWT gate: the satellite carries no Chora session.
	ExamWebhookPath = "/api/v1/webhooks/exam-results"

	// ExamWebhookSignatureHeader carries "sha256=<hex>" over ts + "." + body.
	ExamWebhookSignatureHeader = "X-Chora-Signature"

	// ExamWebhookTimestampHeader carries the signer's unix seconds.
	ExamWebhookTimestampHeader = "X-Chora-Timestamp"

	// ExamWebhookToleranceSeconds is the replay window (design doc section 7:
	// reject if the timestamp is older than 5 minutes; skew is symmetric so a
	// far-future stamp is refused too).
	ExamWebhookToleranceSeconds = 300

	// MaxExamWebhookBodyBytes caps the inbound body (payloads are small; 1MiB
	// is a generous safety cap, mirroring the Stripe receiver).
	MaxExamWebhookBodyBytes = 1 << 20
)

// ExamWebhookDeps wires the receiver.
type ExamWebhookDeps struct {
	// Secret is the shared inbound HMAC secret (whsec_...). Sourced ONLY from
	// the CHORA_EXAM_WEBHOOK_SECRET env var (Secret Manager-backed). Empty
	// short-circuits every delivery to 503: no trust anchor, no processing.
	Secret string

	// Events is the durable dedup gate (chora_delivery.exam_webhook_events).
	Events examwebhook.Repo

	// Forms resolves the revision-pinned ExamForm the raw score is graded
	// against; Results persists the durable outcome row.
	Forms   exam.ExamFormStore
	Results exam.ExamResultStore

	// Publish tees ExamResultReleased into the outbox (the ADR-190 D1 outcome
	// seam; cert + transcript consumers ride it). Optional in minimal wiring;
	// prod always sets it.
	Publish ExamResultEventPublisher

	// Now overrides time.Now for deterministic tests.
	Now func() time.Time
}

// RegisterExamWebhookRoutes mounts the receiver on mux. Deliberately NOT
// wrapped in tenantRequired: the satellite sends no X-Tenant-Id header; the
// tenant is named INSIDE the HMAC-signed payload and verified against the
// exam form it targets.
func RegisterExamWebhookRoutes(mux *http.ServeMux, d ExamWebhookDeps) {
	mux.HandleFunc("POST "+ExamWebhookPath, logging(examWebhookHandler(d)))
}

// satelliteExamResultReq is the signed inbound payload.
type satelliteExamResultReq struct {
	EventID        string `json:"event_id"`        // envelope UUIDv7 minted by the satellite
	IdempotencyKey string `json:"idempotency_key"` // producer dedup key (the idempotency gate)
	TenantID       string `json:"tenant_id"`       // target chora-main tenant
	ExamID         string `json:"exam_id"`         // the Exam the form belongs to
	ExamFormID     string `json:"exam_form_id"`    // the revision-pinned form graded against
	CandidateRef   string `json:"candidate_ref"`   // a REAL chora-main GCID (ADR-190 D2; D2 resolve-or-register is ON HOLD)
	RawScore       int    `json:"raw_score"`       // raw score; chora-main computes PASS/FAIL
}

// validateSatelliteReq normalises + validates the payload. Returns the name
// of the offending field ("" = valid) so the 400 names the culprit exactly.
func validateSatelliteReq(req *satelliteExamResultReq) string {
	req.EventID = strings.ToLower(strings.TrimSpace(req.EventID))
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.TenantID = strings.ToLower(strings.TrimSpace(req.TenantID))
	req.ExamID = strings.ToLower(strings.TrimSpace(req.ExamID))
	req.ExamFormID = strings.ToLower(strings.TrimSpace(req.ExamFormID))
	req.CandidateRef = strings.ToLower(strings.TrimSpace(req.CandidateRef))
	switch {
	case !isCanonicalUUIDString(req.EventID):
		return "event_id"
	case req.IdempotencyKey == "":
		return "idempotency_key"
	case !isCanonicalUUIDString(req.TenantID):
		return "tenant_id"
	case !isCanonicalUUIDString(req.ExamID):
		return "exam_id"
	case !isCanonicalUUIDString(req.ExamFormID):
		return "exam_form_id"
	case !isCanonicalUUIDString(req.CandidateRef):
		return "candidate_ref"
	}
	return ""
}

func examWebhookHandler(d ExamWebhookDeps) http.HandlerFunc {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Wiring honesty: an unconfigured receiver refuses loudly; it never
		// pretends (fail-loud, the payments 503 precedent).
		if strings.TrimSpace(d.Secret) == "" {
			log.Printf("delivery: exam webhook 503: CHORA_EXAM_WEBHOOK_SECRET unset (no HMAC trust anchor)")
			writeError(w, http.StatusServiceUnavailable, "exam webhook secret not configured")
			return
		}
		if d.Events == nil || d.Forms == nil || d.Results == nil {
			log.Printf("delivery: exam webhook 503: dependencies not wired (events=%v forms=%v results=%v)",
				d.Events != nil, d.Forms != nil, d.Results != nil)
			writeError(w, http.StatusServiceUnavailable, "exam webhook receiver not fully wired")
			return
		}

		// Raw body: the HMAC signs the exact bytes, so read before any parse.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxExamWebhookBodyBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "read body: "+err.Error())
			return
		}

		// Replay window FIRST (cheap), then the MAC over ts + "." + body.
		tsRaw := strings.TrimSpace(r.Header.Get(ExamWebhookTimestampHeader))
		ts, terr := strconv.ParseInt(tsRaw, 10, 64)
		if tsRaw == "" || terr != nil {
			writeError(w, http.StatusUnauthorized, ExamWebhookTimestampHeader+" must be unix seconds")
			return
		}
		if skew := now().Unix() - ts; skew > ExamWebhookToleranceSeconds || skew < -ExamWebhookToleranceSeconds {
			writeError(w, http.StatusUnauthorized,
				fmt.Sprintf("timestamp outside the %d s replay window", ExamWebhookToleranceSeconds))
			return
		}

		sigHeader := strings.TrimSpace(r.Header.Get(ExamWebhookSignatureHeader))
		const prefix = "sha256="
		if !strings.HasPrefix(sigHeader, prefix) {
			writeError(w, http.StatusUnauthorized, ExamWebhookSignatureHeader+" must be sha256=<hex>")
			return
		}
		provided, herr := hex.DecodeString(strings.TrimPrefix(sigHeader, prefix))
		if herr != nil || len(provided) == 0 {
			writeError(w, http.StatusUnauthorized, "signature is not valid hex")
			return
		}
		mac := hmac.New(sha256.New, []byte(d.Secret))
		mac.Write([]byte(tsRaw + "."))
		mac.Write(body)
		// hmac.Equal is the constant-time comparison; string == would leak a
		// timing oracle on the shared secret.
		if !hmac.Equal(mac.Sum(nil), provided) {
			id, key := bestEffortSatelliteMeta(body)
			log.Printf("delivery: exam webhook signature verify FAILED (unverified event_id=%q idempotency_key=%q): "+
				"the satellite will retry then dead-letter on its side; check for a stale or duplicate whsec_ on either end (ADR-188 lesson)", id, key)
			writeError(w, http.StatusUnauthorized, "signature verification failed")
			return
		}

		// The payload is authenticated from here on. Parse strictly; a caller
		// fault is a 400 NAMING the field: the satellite must fix, not retry.
		var req satelliteExamResultReq
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if derr := dec.Decode(&req); derr != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON payload: "+derr.Error())
			return
		}
		if field := validateSatelliteReq(&req); field != "" {
			writeError(w, http.StatusBadRequest, field+": required and must be well-formed (canonical UUID where applicable)")
			return
		}

		// Dedup gate: INSERT the receipt; a duplicate splits into processed
		// replay (return the ORIGINAL outcome) vs unprocessed re-dispatch.
		receipt, nerr := examwebhook.New(req.EventID, req.IdempotencyKey, req.TenantID, now())
		if nerr != nil {
			// Unreachable after validation; surface loudly rather than mask.
			writeError(w, http.StatusBadRequest, nerr.Error())
			return
		}
		fresh := true
		if ierr := d.Events.Insert(r.Context(), receipt); ierr != nil {
			if !errors.Is(ierr, examwebhook.ErrDuplicate) {
				log.Printf("delivery: exam webhook dedup insert failed key=%s: %v", req.IdempotencyKey, ierr)
				writeError(w, http.StatusInternalServerError, "dedup insert failed")
				return
			}
			existing, ok, gerr := d.Events.GetByIdempotencyKey(r.Context(), req.IdempotencyKey)
			if gerr != nil {
				// Deliberate deviation from the payments receiver (which skips on
				// an unknown state): OUR read failed, so OUR status is 5xx and the
				// satellite retries. A skip here could silently strand a result.
				log.Printf("delivery: exam webhook dedup state lookup failed key=%s: %v", req.IdempotencyKey, gerr)
				writeError(w, http.StatusInternalServerError, "dedup state lookup failed")
				return
			}
			if !ok {
				log.Printf("delivery: exam webhook dedup row vanished after duplicate insert key=%s", req.IdempotencyKey)
				writeError(w, http.StatusInternalServerError, "dedup state inconsistent")
				return
			}
			if existing.IsProcessed() {
				// Idempotent replay: return the ORIGINAL outcome, write nothing.
				tctx := tracing.WithTenantID(r.Context(), existing.TenantID)
				res, rok, rerr := d.Results.Get(tctx, existing.ResultID)
				if rerr != nil {
					log.Printf("delivery: exam webhook replay result load failed key=%s result=%s: %v",
						req.IdempotencyKey, existing.ResultID, rerr)
					writeError(w, http.StatusInternalServerError, "replay result lookup failed")
					return
				}
				if !rok || res == nil {
					log.Printf("delivery: exam webhook INCONSISTENT state: processed receipt key=%s names result %s but no durable row exists",
						req.IdempotencyKey, existing.ResultID)
					writeError(w, http.StatusInternalServerError, "processed receipt has no durable result")
					return
				}
				log.Printf("delivery: exam webhook replay key=%s: returning original result %s (%s)",
					req.IdempotencyKey, res.ID, res.Outcome)
				writeJSON(w, http.StatusOK, examWebhookResultDTO(res, true))
				return
			}
			// Exists but unprocessed: a prior dispatch died. Re-dispatch; never
			// strand a result behind an "already received" skip.
			log.Printf("delivery: exam webhook key=%s exists unprocessed (prior error: %q): re-dispatching",
				req.IdempotencyKey, existing.ProcessingError)
			receipt = existing
			fresh = false
		}

		dispatchExamWebhook(w, r, d, receipt, &req, fresh, now)
	}
}

// dispatchExamWebhook runs (or resumes) the verified delivery:
// load form -> grade -> reserve result id on the receipt -> save row ->
// publish released event -> mark processed. Every step failure marks the
// receipt failed WITH its reason and returns a status honest about the fault:
// 4xx caller (fix the payload), 5xx ours (retry will re-dispatch).
func dispatchExamWebhook(w http.ResponseWriter, r *http.Request, d ExamWebhookDeps,
	receipt *examwebhook.WebhookEvent, req *satelliteExamResultReq, fresh bool, now func() time.Time) {

	key := receipt.IdempotencyKey
	tctx := tracing.WithTenantID(r.Context(), req.TenantID)

	failWith := func(status int, reason string) {
		if merr := d.Events.MarkFailed(r.Context(), key, reason); merr != nil {
			log.Printf("delivery: exam webhook mark-failed failed key=%s: %v (original: %s)", key, merr, reason)
		}
		log.Printf("delivery: exam webhook dispatch failed key=%s status=%d: %s", key, status, reason)
		writeError(w, status, reason)
	}

	// The form grounds the payload: it must exist, belong to the named tenant
	// AND exam, and be live. A cross-tenant or cross-exam probe reads as
	// not-found (never a distinguishable 403).
	form, ok, ferr := d.Forms.Get(tctx, req.ExamFormID)
	if ferr != nil {
		failWith(http.StatusInternalServerError, "exam form lookup failed: "+ferr.Error())
		return
	}
	if !ok || form == nil || form.TenantID != req.TenantID || form.ExamID != req.ExamID || form.DeletedAt != nil {
		failWith(http.StatusNotFound, "exam_form_id: no live exam form for this tenant and exam")
		return
	}

	// Resume path: the durable result already exists from a prior attempt
	// whose publish (or bookkeeping) failed. NEVER grade twice: one
	// idempotency_key = at most one exam_results row.
	var res *exam.ExamResult
	if receipt.ResultID != "" {
		existing, rok, rerr := d.Results.Get(tctx, receipt.ResultID)
		if rerr != nil {
			failWith(http.StatusInternalServerError, "resume result lookup failed: "+rerr.Error())
			return
		}
		if rok && existing != nil {
			res = existing
			log.Printf("delivery: exam webhook key=%s resuming with durable result %s (no re-grade)", key, res.ID)
		}
		// A reserved id with no durable row means the crash landed between
		// reserve and save: nothing was written, so a fresh grade is safe.
	}

	var released exam.ExamResultReleased
	if res == nil {
		graded, rel, gerr := form.Grade(req.CandidateRef, req.RawScore)
		if gerr != nil {
			failWith(examFormStatus(gerr), gerr.Error())
			return
		}
		// Reserve BEFORE save: once the row is durable the receipt already
		// names it, so a retry resumes instead of re-grading (the no-duplicate
		// guarantee). At this point nothing is written, so a reserve failure is
		// safely retryable.
		if merr := d.Events.MarkResultRecorded(r.Context(), key, graded.ID); merr != nil {
			failWith(http.StatusInternalServerError, "reserve result id failed: "+merr.Error())
			return
		}
		if serr := d.Results.Save(tctx, graded); serr != nil {
			failWith(http.StatusInternalServerError, "exam result write failed: "+serr.Error())
			return
		}
		res = graded
		released = rel
	} else {
		// Rebuild the released value from the durable row + the form's cut
		// (the cut is exposure-locked, so PassMark is stable across retries).
		released = exam.ExamResultReleased{
			ResultID:     res.ID,
			TenantID:     res.TenantID,
			ExamID:       res.ExamID,
			ExamFormID:   res.ExamFormID,
			CandidateRef: res.CandidateRef,
			RawScore:     res.RawScore,
			MaxScore:     res.MaxScore,
			PassMark:     form.Cut.PassMark(),
			Outcome:      res.Outcome,
			OccurredAt:   res.ScoredAt,
		}
	}

	// Outcome seam (ADR-190 D1): tee released into the outbox so the cert +
	// transcript consumers fire. A publish failure is OUR fault: the row is
	// durable, the receipt remembers it, the satellite's retry resumes here.
	if d.Publish != nil {
		// Actor gcid is empty: a federation delivery is system-attributed (the
		// envelope contract allows an empty actor for system-emitted events).
		if perr := d.Publish.PublishExamResultReleased(tctx, "", released); perr != nil {
			failWith(http.StatusInternalServerError, "exam result recorded but outcome-event publish failed: "+perr.Error())
			return
		}
	}

	if merr := d.Events.MarkProcessed(r.Context(), key, res.ID, now()); merr != nil {
		// The dispatch IS complete and durable; only bookkeeping failed. Do not
		// 5xx (a retry would change nothing downstream: resume re-publishes
		// idempotently and re-marks). Loud log so ops sees the drift.
		log.Printf("delivery: exam webhook mark-processed failed key=%s result=%s: %v", key, res.ID, merr)
	}

	status := http.StatusOK
	if fresh {
		status = http.StatusCreated
	}
	log.Printf("delivery: exam webhook ACCEPTED key=%s result=%s outcome=%s tenant=%s form=%s candidate=%s",
		key, res.ID, res.Outcome, res.TenantID, res.ExamFormID, res.CandidateRef)
	writeJSON(w, status, examWebhookResultDTO(res, false))
}

// examWebhookResultDTO is the receiver's response shape. Deliberately lean:
// the satellite needs the ack + verdict + correlation id, nothing more.
func examWebhookResultDTO(res *exam.ExamResult, replayed bool) map[string]interface{} {
	return map[string]interface{}{
		"received":  true,
		"replayed":  replayed,
		"result_id": res.ID,
		"outcome":   string(res.Outcome),
		"raw_score": res.RawScore,
		"max_score": res.MaxScore,
	}
}

// bestEffortSatelliteMeta extracts event_id + idempotency_key from a raw (and
// possibly UNVERIFIED) body for forensic logging ONLY: a signature-rejected
// delivery stays traceable without ever being trusted (payments precedent).
func bestEffortSatelliteMeta(body []byte) (eventID, idempotencyKey string) {
	var meta struct {
		EventID        string `json:"event_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	_ = json.Unmarshal(body, &meta)
	return meta.EventID, meta.IdempotencyKey
}

// isCanonicalUUIDString reports whether s is a 36-char lowercase hyphenated
// UUID (8-4-4-4-12). The exam tables cast these values to uuid in SQL, so the
// boundary refuses what Postgres would 22P02 on (parse at the boundary; never
// use pg as the validator).
func isCanonicalUUIDString(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
			if !isHex {
				return false
			}
		}
	}
	return true
}
