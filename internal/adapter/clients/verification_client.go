// verification_client.go — outbound HTTP client to chora-identity's INTERNAL
// by-GCID verification-claim read (ADR-190 D2 admission gate).
//
// VerificationClient implements domain/exam.VerificationClaimReader by calling
// chora-identity over the mesh:
//
//	GET {SVC_IDENTITY_HTTP_URL}/internal/v1/identity/verification-status?gcid=<uuid>
//
// The client lives in the adapter layer per hexagonal architecture (the domain
// never sees net/http). Per ddd-enforcement #3 cross-DB queries are FORBIDDEN —
// this mesh HTTP call is the sanctioned mechanism for chora-delivery to consult
// the Identity-owned verification claim without joining against chora_identity.
//
// Per `feedback_no_inline_config`: the upstream URL is sourced from env
// (`SVC_IDENTITY_HTTP_URL`, e.g. http://chora-identity.identity.svc.cluster.local:8080
// in the mesh — mirrors the SVC_CREATION_GRPC_URL pattern). No URL is hardcoded.
//
// Fail-loud (`feedback_no_stubs_real_wiring`): a transport failure or any
// non-200 from chora-identity returns an error — NEVER a silent `false`. The
// admit handler surfaces that as a 502, so a broken verification path can never
// masquerade as an unverified candidate (which would be a false-negative
// admission that hides a compliance-critical outage).
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// verificationStatusResponse mirrors chora-identity's internal endpoint body.
type verificationStatusResponse struct {
	GCID     string `json:"gcid"`
	Verified bool   `json:"verified"`
	Status   string `json:"status"`
}

// VerificationClient adapts chora-identity's internal verification-status HTTP
// endpoint to the domain.exam.VerificationClaimReader port.
type VerificationClient struct {
	baseURL string
	http    *http.Client
}

// NewVerificationClient constructs a VerificationClient. baseURL is sourced from
// SVC_IDENTITY_HTTP_URL (no inline config). A nil httpClient defaults to a
// 5-second-timeout client. The caller owns the httpClient lifecycle.
func NewVerificationClient(baseURL string, httpClient *http.Client) *VerificationClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &VerificationClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		http:    httpClient,
	}
}

// IsVerified reports whether the given learner GCID currently holds a VERIFIED
// Identity verification claim, per ADR-190 D2. tenantID is threaded for trace
// attribution only — the claim is person-scoped (identity keys off gcid).
//
// Returns:
//   - (true, nil)  when chora-identity reports verified=true;
//   - (false, nil) when chora-identity reports verified=false (incl. no claim);
//   - (false, err) on any transport error or non-200 status — fail loud.
func (c *VerificationClient) IsVerified(ctx context.Context, tenantID, gcid string) (bool, error) {
	if c == nil || c.http == nil {
		return false, errors.New("verification client: http client nil")
	}
	if c.baseURL == "" {
		return false, errors.New("verification client: base URL empty (SVC_IDENTITY_HTTP_URL unset)")
	}
	if strings.TrimSpace(gcid) == "" {
		return false, errors.New("verification client: gcid required")
	}

	q := url.Values{}
	q.Set("gcid", strings.TrimSpace(gcid))
	endpoint := c.baseURL + "/internal/v1/identity/verification-status?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("verification client: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if tid := strings.TrimSpace(tenantID); tid != "" {
		req.Header.Set("X-Tenant-Id", tid)
	}
	// OTLP-everywhere: propagate the W3C traceparent to the identity span.
	tracing.Inject(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("verification client: identity call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return false, fmt.Errorf("verification client: identity returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out verificationStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, fmt.Errorf("verification client: decode identity response: %w", err)
	}
	return out.Verified, nil
}

// Compile-time port conformance.
var _ exam.VerificationClaimReader = (*VerificationClient)(nil)
