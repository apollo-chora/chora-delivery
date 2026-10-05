// Course Application HTTP routes — S6.1.
//
// Routes (per docs/design/ux_course_application.md):
//
//	GET  /v1/courses?paid=true                          — paid catalog browse (in v1_handlers.go)
//	GET  /v1/courses/{id}/application-form              — form schema (Singpass-prefilled if token supplied)
//	POST /v1/me/applications                            — submit (idempotent on course+gcid)
//	GET  /v1/me/applications                            — list user's applications
//	GET  /v1/me/applications/{id}                       — detail with state + history
//	POST /v1/me/applications/{id}/accept-offer          — accept → chora-payments Checkout Session
//	POST /v1/me/applications/{id}/withdraw              — withdraw with reason
//	GET  /v1/me/applications/{id}/invoice               — signed-URL to PDF (post-Paid only)
//
// Coordinates with:
//   - chora-payments PaymentService gRPC (deps.Payments) — ADR-164 Stage C
//   - Singpass MyInfo adapter (deps.Singpass)
//   - Tax-invoice issuer (deps.Invoice)
//   - Application repo (deps.Applications)
//   - Application event publisher (deps.Publisher.PublishApplicationStateChanged)
//
// Per ADR-164 Stage C (2026-05-24): the accept-offer flow MINTS a Stripe
// Checkout Session by calling chora-payments synchronously over gRPC. The
// async payment outcome arrives via Pub/Sub
// (`chora.payments.application_payment.payment_captured.v1`) and is
// materialised back by `events/payments_subscriber.go`. The previous
// inline Stripe SDK + `client_secret` Stripe Elements pattern is retired.
//
// Hexagonal: ADAPTER. Domain code never imports this file.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/invoice"
	"github.com/apollo-chora/chora-delivery/internal/adapter/payments"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// learnerCtx decorates the request context with the caller's tenant + gcid
// so the pg.ApplicationRepo's rls.ApplySession sets BOTH chora.tenant_id
// (tenant_isolation) and chora.user_gcid (user_isolation, FOR SELECT) —
// restricting the learner surface to the caller's own applications as
// defence-in-depth on top of the handler's owner-or-404 guard. The in-memory
// adapter ignores ctx. Per the live exam-handler pattern (tenantRequired
// validates the header but does NOT decorate ctx).
func learnerCtx(r *http.Request, tenantID, gcid string) context.Context {
	return tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
}

// -----------------------------------------------------------------------------
// /v1/courses/{id}/application-form
// -----------------------------------------------------------------------------

func handleApplicationForm(deps Deps, courseID string, w http.ResponseWriter, r *http.Request) {
	pc, ok, err := deps.Catalogue.Get(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue read failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}

	gross := int64(pc.PriceSGDCents)
	funding := []application.FundingLine{}
	if pc.SFEligible {
		funding = append(funding, application.FundingLine{
			Type:              application.FundingTypeSkillsFutureCredits,
			MaxAmountSGDCents: 50000, // SGD 500 default skeleton; real eligibility deferred to M17.
			Eligible:          false, // Eligibility decided post-Singpass + funding-application form.
		})
	}

	prefillToken := strings.TrimSpace(r.URL.Query().Get("prefill_token"))
	schema := application.FormSchemaFor(application.FormSchemaInput{
		CourseTitle:       pc.Title,
		PriceSGDCents:     int64(pc.PriceSGDCents),
		TuitionGrossCents: gross,
		SFEligible:        pc.SFEligible,
		SingpassPrefill:   prefillToken != "",
		FundingLines:      funding,
	})

	out := map[string]interface{}{
		"course_id":             courseID,
		"course_title":          schema.CourseTitle,
		"singpass_prefill":      schema.SingpassPrefill,
		"tuition_gross_cents":   schema.TuitionGrossCents,
		"gst_amount_cents":      schema.GSTAmountCents,
		"gst_rate_basis_points": schema.GSTRateBasisPoints,
		"funding_lines":         schema.FundingLines,
		"net_amount_cents":      schema.NetAmountCents,
		"currency":              schema.Currency,
		"sf_eligible":           pc.SFEligible,
	}

	// Optional Singpass MyInfo prefill — when prefill_token supplied + adapter
	// available, retrieve verified profile and embed under singpass_profile.
	if prefillToken != "" && deps.Singpass != nil {
		res, err := deps.Singpass.RetrieveMyInfo(r.Context(), prefillToken)
		if err == nil && res != nil {
			out["singpass_profile"] = map[string]interface{}{
				"session_id":         res.SessionID,
				"legal_name":         res.Profile.LegalName,
				"preferred_name":     res.Profile.PreferredName,
				"nric_last4":         res.Profile.NRICLast4,
				"date_of_birth":      res.Profile.DateOfBirth.Format("2006-01-02"),
				"gender":             res.Profile.Gender,
				"nationality":        res.Profile.Nationality,
				"residential_street": res.Profile.ResidentialStreet,
				"postal_code":        res.Profile.PostalCode,
				"employment_status":  res.Profile.EmploymentStatus,
				"verified":           res.Verified,
			}
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// -----------------------------------------------------------------------------
// /v1/me/applications — list (GET) + submit (POST, idempotent)
// -----------------------------------------------------------------------------

type submitApplicationReq struct {
	CourseID string `json:"course_id"`
	ClassID  string `json:"class_id"`
}

func meApplicationsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleApplicationSubmit(deps, w, r)
		case http.MethodGet:
			handleApplicationList(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleApplicationSubmit(deps Deps, w http.ResponseWriter, r *http.Request) {
	gcid := strings.TrimSpace(r.Header.Get("gcid"))
	if gcid == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required")
		return
	}
	var req submitApplicationReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.CourseID) == "" {
		writeError(w, http.StatusBadRequest, "course_id required")
		return
	}
	tenantID := r.Header.Get("X-Tenant-Id")

	// Verify the course exists in this tenant before creating an application.
	if pc, ok, err := deps.Catalogue.Get(r.Context(), req.CourseID); err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue read failed")
		return
	} else if !ok {
		writeError(w, http.StatusNotFound, "course not found")
		return
	} else if pc.TenantID != tenantID && pc.Visibility != domain.VisibilityPublic {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}

	app, created, err := deps.Applications.SubmitOrGet(learnerCtx(r, tenantID, gcid), application.SubmitInput{
		TenantID: tenantID,
		CourseID: req.CourseID,
		ClassID:  req.ClassID,
		GCID:     gcid,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if created && deps.Publisher != nil {
		_, _ = deps.Publisher.PublishApplicationStateChanged(app, r.Header.Get("traceparent"))
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, applicationDTO(app))
}

func handleApplicationList(deps Deps, w http.ResponseWriter, r *http.Request) {
	gcid := strings.TrimSpace(r.Header.Get("gcid"))
	if gcid == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required")
		return
	}
	tenantID := r.Header.Get("X-Tenant-Id")
	offset, limit := paging(r)
	items, total, err := deps.Applications.ListByGCID(learnerCtx(r, tenantID, gcid), tenantID, gcid, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, app := range items {
		out = append(out, applicationDTO(app))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
		"total": total,
	})
}

// -----------------------------------------------------------------------------
// /v1/me/applications/{id} — detail / accept-offer / withdraw / invoice
// -----------------------------------------------------------------------------

func meApplicationsSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gcid := strings.TrimSpace(r.Header.Get("gcid"))
		if gcid == "" {
			writeError(w, http.StatusUnauthorized, "gcid header required")
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/v1/me/applications/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		appID := parts[0]
		tenantID := r.Header.Get("X-Tenant-Id")

		app, ok, err := deps.Applications.Get(learnerCtx(r, tenantID, gcid), tenantID, appID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Owner-or-404: cross-user lookups (different gcid in same tenant) are
		// returned as 404 (not 403) to avoid disclosure.
		if !ok || app.GCID != gcid {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}

		// Sub-routes.
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			writeJSON(w, http.StatusOK, applicationDetailDTO(app))
			return
		}
		if len(parts) == 2 {
			switch parts[1] {
			case "accept-offer":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleAcceptOffer(deps, app, w, r)
				return
			case "withdraw":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleWithdraw(deps, app, w, r)
				return
			case "invoice":
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleInvoice(deps, app, w, r)
				return
			}
		}
		writeError(w, http.StatusNotFound, "not found")
	}
}

// handleAcceptOffer transitions OfferMade → Accepted and mints a Stripe
// Checkout Session by calling chora-payments via gRPC (ADR-164 Stage C).
// Returns the hosted Checkout URL the FE redirects to via window.location.
// The async payment outcome arrives via Pub/Sub
// (`chora.payments.application_payment.payment_captured.v1`) and drives
// the Application → Paid → Enrolled transitions.
func handleAcceptOffer(deps Deps, app *application.Application, w http.ResponseWriter, r *http.Request) {
	if app.Status != application.StatusOfferMade {
		writeError(w, http.StatusConflict, "offer-accept only allowed when status is offer_made")
		return
	}
	if err := app.Transition(application.StatusAccepted); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Compute amount from form-schema (SGD cents incl. GST minus eligible funding).
	// A catalogue read error degrades to a nil pc — the schema fallback below
	// handles it (the offer-accept flow does not hard-depend on the course).
	pc, _, _ := deps.Catalogue.Get(r.Context(), app.CourseID)
	var schema application.FormSchema
	if pc != nil {
		schema = application.FormSchemaFor(application.FormSchemaInput{
			CourseTitle:       pc.Title,
			PriceSGDCents:     int64(pc.PriceSGDCents),
			TuitionGrossCents: int64(pc.PriceSGDCents),
			FundingLines:      app.FundingLines,
		})
	}
	if schema.NetAmountCents <= 0 {
		writeError(w, http.StatusBadRequest, "net payable must be positive — funding configuration error")
		return
	}

	// chora-payments PaymentService gRPC — mint Checkout Session.
	if deps.Payments == nil {
		writeError(w, http.StatusServiceUnavailable, "payments client not configured")
		return
	}
	successURL, cancelURL := applicationCheckoutURLs(deps, app.ID)
	out, err := deps.Payments.CreateApplicationCheckoutSession(r.Context(), payments.CreateApplicationCheckoutInput{
		TenantID:      app.TenantID,
		LearnerGCID:   app.GCID,
		ApplicationID: app.ID,
		CourseID:      app.CourseID,
		AmountCents:   schema.NetAmountCents,
		Currency:      "SGD",
		SuccessURL:    successURL,
		CancelURL:     cancelURL,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("payments: %v", err))
		return
	}
	// Stash the Stripe session id as the Application's payment handle so
	// the existing audit / invoice flow can cross-correlate pre-capture.
	// The canonical Stripe PaymentIntent / Charge ids arrive later on the
	// chora.payments.application_payment.payment_captured.v1 event and
	// override this stash via payments_subscriber.go's AttachPaymentIntent
	// call.
	app.AttachPaymentIntent(out.StripeSessionID)
	if err := deps.Applications.Save(learnerCtx(r, app.TenantID, app.GCID), app); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishApplicationStateChanged(app, r.Header.Get("traceparent"))
	}

	resp := applicationDTO(app)
	resp["purchase_id"] = out.PurchaseID
	resp["session_id"] = out.StripeSessionID
	resp["checkout_url"] = out.StripeCheckoutURL
	resp["amount_cents"] = schema.NetAmountCents
	resp["currency"] = "SGD"
	resp["payment_state"] = out.State
	writeJSON(w, http.StatusOK, resp)
}

// applicationCheckoutURLs derives the success/cancel redirect targets,
// substituting the {APPLICATION_ID} placeholder. Falls back to chora.site
// defaults when Deps templates are empty so an unwired dev still produces
// usable URLs.
func applicationCheckoutURLs(deps Deps, applicationID string) (success, cancel string) {
	successTpl := strings.TrimSpace(deps.AppCheckoutSuccessURLTemplate)
	if successTpl == "" {
		successTpl = "https://chora.site/a/applications/{APPLICATION_ID}/paid?session_id={CHECKOUT_SESSION_ID}"
	}
	cancelTpl := strings.TrimSpace(deps.AppCheckoutCancelURLTemplate)
	if cancelTpl == "" {
		cancelTpl = "https://chora.site/a/applications/{APPLICATION_ID}?checkout_cancelled=1"
	}
	success = strings.ReplaceAll(successTpl, "{APPLICATION_ID}", applicationID)
	cancel = strings.ReplaceAll(cancelTpl, "{APPLICATION_ID}", applicationID)
	return success, cancel
}

// handleWithdraw transitions to Withdrawn with an optional reason.
func handleWithdraw(deps Deps, app *application.Application, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = decodeBody(r, &req) // body optional

	if err := app.TransitionWithReason(application.StatusWithdrawn, req.Reason); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := deps.Applications.Save(learnerCtx(r, app.TenantID, app.GCID), app); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishApplicationStateChanged(app, r.Header.Get("traceparent"))
	}
	writeJSON(w, http.StatusOK, applicationDTO(app))
}

// handleInvoice issues (or fetches existing) the tax invoice PDF and returns
// a signed URL. Only valid post-Paid; before that the invoice is not
// generated yet (409).
func handleInvoice(deps Deps, app *application.Application, w http.ResponseWriter, r *http.Request) {
	if app.Status != application.StatusPaid && app.Status != application.StatusEnrolled {
		writeError(w, http.StatusConflict, "invoice generated only after payment")
		return
	}
	if deps.Invoice == nil {
		writeError(w, http.StatusServiceUnavailable, "invoice adapter not configured")
		return
	}
	pc, _, _ := deps.Catalogue.Get(r.Context(), app.CourseID)
	if pc == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	tenantName := pc.InstructorName // skeleton — full tenant lookup in M12+.
	if tenantName == "" {
		tenantName = "Chora Tenant"
	}

	invNo := app.InvoiceID
	if invNo == "" {
		invNo = invoiceNumberFor(app)
	}

	// Build pricing breakdown from the course.
	gross := int64(pc.PriceSGDCents)
	gst := (gross * 900) / 10000 // SG GST 9% (basis points 900).
	fundingLines := []invoice.FundingLine{}
	for _, fl := range app.FundingLines {
		if !fl.Eligible {
			continue
		}
		fundingLines = append(fundingLines, invoice.FundingLine{
			Type:           string(fl.Type),
			Description:    string(fl.Type),
			AmountSGDCents: fl.MaxAmountSGDCents,
		})
	}
	var fundedSum int64
	for _, fl := range fundingLines {
		fundedSum += fl.AmountSGDCents
	}
	net := gross + gst - fundedSum
	if net < 0 {
		net = 0
	}

	res, err := deps.Invoice.Issue(r.Context(), invoice.Input{
		InvoiceNumber:        invNo,
		IssueDate:            time.Now().UTC(),
		TenantName:           tenantName,
		LearnerName:          "Learner", // S6.2 will pull from chora-identity profile.
		LearnerGCID:          app.GCID,
		CourseTitle:          pc.Title,
		TuitionGrossSGDCents: gross,
		GSTSGDCents:          gst,
		GSTRateBasisPoints:   900,
		FundingLines:         fundingLines,
		NetSGDCents:          net,
		StripeChargeID:       app.StripePaymentIntentID,
		PaymentMethod:        "card",
		Currency:             "SGD",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	app.AttachInvoice(invNo)
	_ = deps.Applications.Save(learnerCtx(r, app.TenantID, app.GCID), app)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"invoice_number": invNo,
		"signed_url":     res.SignedURL,
		"object_uri":     res.URI,
		"size_bytes":     res.SizeBytes,
		"ttl_seconds":    int64(60 * 60), // 60-min TTL per S6.1 brief.
	})
}

// -----------------------------------------------------------------------------
// DTOs
// -----------------------------------------------------------------------------

func applicationDTO(app *application.Application) map[string]interface{} {
	out := map[string]interface{}{
		"id":         app.ID,
		"tenant_id":  app.TenantID,
		"course_id":  app.CourseID,
		"gcid":       app.GCID,
		"status":     string(app.Status),
		"created_at": app.CreatedAt.Format(time.RFC3339Nano),
		"updated_at": app.UpdatedAt.Format(time.RFC3339Nano),
	}
	if app.ClassID != "" {
		out["class_id"] = app.ClassID
	}
	if !app.OfferExpiresAt.IsZero() {
		out["offer_expires_at"] = app.OfferExpiresAt.Format(time.RFC3339Nano)
	}
	if app.StripePaymentIntentID != "" {
		out["stripe_payment_intent_id"] = app.StripePaymentIntentID
	}
	if app.InvoiceID != "" {
		out["invoice_id"] = app.InvoiceID
	}
	if app.SingpassSessionID != "" {
		out["singpass_session_id"] = app.SingpassSessionID
	}
	if app.RejectedReason != "" {
		out["rejected_reason"] = app.RejectedReason
	}
	if app.WithdrawnReason != "" {
		out["withdrawn_reason"] = app.WithdrawnReason
	}
	return out
}

func applicationDetailDTO(app *application.Application) map[string]interface{} {
	out := applicationDTO(app)
	hist := app.History()
	rows := make([]map[string]interface{}, 0, len(hist))
	for _, h := range hist {
		row := map[string]interface{}{
			"from": string(h.From),
			"to":   string(h.To),
			"at":   h.At.Format(time.RFC3339Nano),
		}
		if h.Reason != "" {
			row["reason"] = h.Reason
		}
		rows = append(rows, row)
	}
	out["history"] = rows
	if len(app.FundingLines) > 0 {
		out["funding_lines"] = app.FundingLines
	}
	return out
}

// invoiceNumberFor returns a deterministic invoice number for an application.
func invoiceNumberFor(app *application.Application) string {
	short := app.ID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("CHO-INV-%s-%s", time.Now().UTC().Format("20060102"), short)
}

// -----------------------------------------------------------------------------
// Compile-time imports — keep this file's imports honest.
// -----------------------------------------------------------------------------

var _ = errors.New // silence unused if the file shrinks.
