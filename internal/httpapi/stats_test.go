package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"whiteboard/internal/metrics"
)

func TestStats(t *testing.T) {
	m := metrics.New()
	m.WSConnections.Set(3)
	m.BoardsActive.Set(2)
	h := NewRouter(Deps{Gateway: http.NotFoundHandler(), Metrics: m, Ready: func(context.Context) error { return nil }, NodeID: "node-9"})

	code, body := get(t, h, "/api/stats")
	var s Stats
	if code != http.StatusOK || json.Unmarshal([]byte(body), &s) != nil || s.Node != "node-9" || s.Connections != 3 || s.Boards != 2 || s.WindowS != 10 {
		t.Fatalf("/api/stats = %d %s", code, body)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stats/stream", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	// Two events, a second apart.
	sc := bufio.NewScanner(resp.Body)
	events := 0
	for events < 2 && sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			if json.Unmarshal([]byte(data), &s) != nil || s.Node != "node-9" {
				t.Fatalf("event %q", data)
			}
			events++
		}
	}
	if events != 2 {
		t.Fatalf("got %d events: %v", events, sc.Err())
	}
}
