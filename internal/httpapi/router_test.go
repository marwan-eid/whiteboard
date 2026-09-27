package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"whiteboard/internal/metrics"
)

func newRouter(ready error) http.Handler {
	return NewRouter(Deps{
		Gateway: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		Metrics: metrics.New(),
		Ready:   func(context.Context) error { return ready },
	})
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func TestHealthAndReadiness(t *testing.T) {
	ok := newRouter(nil)
	if code, _ := get(t, ok, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d", code)
	}
	if code, _ := get(t, ok, "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz = %d", code)
	}

	down := newRouter(errors.New("postgres down"))
	if code, _ := get(t, down, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz must not depend on Postgres, got %d", code)
	}
	code, body := get(t, down, "/readyz")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "postgres down") {
		t.Fatalf("/readyz = %d %q", code, body)
	}
}

func TestMetricsExposed(t *testing.T) {
	code, body := get(t, newRouter(nil), "/metrics")
	if code != http.StatusOK || !strings.Contains(body, "ws_connections") {
		t.Fatalf("/metrics = %d, missing ws_connections", code)
	}
}

func TestWSRoutedToGateway(t *testing.T) {
	if code, _ := get(t, newRouter(nil), "/ws"); code != http.StatusTeapot {
		t.Fatalf("/ws = %d, want gateway response", code)
	}
}
