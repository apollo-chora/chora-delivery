// verification_client_extra_test.go — tops up IsVerified branches not
// exercised by verification_client_test.go: the nil-receiver guard and the
// malformed-JSON (200) decode failure.
package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerificationClient_IsVerified_NilReceiver(t *testing.T) {
	t.Parallel()
	var c *VerificationClient
	if _, err := c.IsVerified(context.Background(), "tenant-1", "learner-1"); err == nil {
		t.Fatal("nil receiver: want error")
	}
}

func TestVerificationClient_IsVerified_MalformedJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"verified": tru`)) // truncated JSON
	}))
	defer srv.Close()

	c := NewVerificationClient(srv.URL, srv.Client())
	if _, err := c.IsVerified(context.Background(), "tenant-1", "learner-1"); err == nil {
		t.Fatal("malformed 200 JSON: want decode error")
	}
}
