// logging_hijack_test.go — regression for the ADR-168 live-classroom WS bug:
// the logging middleware wraps the ResponseWriter in *statusWriter, which must
// delegate http.Hijacker so golang.org/x/net/websocket can upgrade. Before the
// fix, every /ws upgrade panicked with "*statusWriter is not http.Hijacker"
// (caught by the cross-pod smoke).
package httpapi

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// hijackableRecorder is an httptest.ResponseRecorder that also satisfies
// http.Hijacker (the real net/http server ResponseWriter does).
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

func TestStatusWriter_DelegatesHijack(t *testing.T) {
	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	sw := &statusWriter{ResponseWriter: rec}

	// statusWriter MUST present as an http.Hijacker so the WS upgrade path
	// type-asserts successfully instead of panicking.
	hj, ok := any(sw).(http.Hijacker)
	if !ok {
		t.Fatal("statusWriter does not implement http.Hijacker — WS upgrade will panic")
	}
	if _, _, err := hj.Hijack(); err != nil {
		t.Fatalf("Hijack delegate returned error: %v", err)
	}
	if !rec.hijacked {
		t.Fatal("Hijack did not delegate to the underlying ResponseWriter")
	}
}

func TestStatusWriter_HijackUnsupported_FailsClean(t *testing.T) {
	// A plain recorder is NOT an http.Hijacker (e.g. HTTP/2) → delegate must
	// return ErrNotSupported, not panic.
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	if _, _, err := sw.Hijack(); err != http.ErrNotSupported {
		t.Fatalf("want http.ErrNotSupported for non-hijackable writer, got %v", err)
	}
}
