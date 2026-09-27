package loadgen

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"whiteboard/internal/board"
	"whiteboard/internal/gateway"
	"whiteboard/internal/metrics"
)

func startNode(t *testing.T) string {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	boards := board.NewRegistry(board.Config{NodeID: "node-test", Store: board.NewMemoryStore(), Tick: 5 * time.Millisecond}, log, m)
	gw := gateway.New(gateway.Config{}, boards, log, m)
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestRunAgainstGateway(t *testing.T) {
	url := startNode(t)
	res := Run(context.Background(), Config{
		URL:          url,
		BoardID:      "demo",
		Clients:      50,
		Duration:     300 * time.Millisecond,
		Ramp:         100 * time.Millisecond,
		PingInterval: 50 * time.Millisecond,
	})
	if res.Failed != 0 || res.Connected != 50 {
		t.Fatalf("connected=%d failed=%d errors=%v", res.Connected, res.Failed, res.Errors)
	}
	if s := res.Summarize(); s.Count < 50 || s.P99 <= 0 || s.P50 > s.P99 || s.P99 > s.Max {
		t.Fatalf("implausible summary: %+v", s)
	}
}

func TestRunReportsServerRejection(t *testing.T) {
	url := startNode(t)
	res := Run(context.Background(), Config{
		URL: url, BoardID: "not a valid id", Clients: 3,
		Duration: 50 * time.Millisecond, PingInterval: 10 * time.Millisecond,
	})
	if res.Failed != 3 || res.Connected != 0 {
		t.Fatalf("connected=%d failed=%d, want all rejected", res.Connected, res.Failed)
	}
}

func TestSummarize(t *testing.T) {
	var r Result
	for i := 1; i <= 100; i++ {
		r.RTTs = append(r.RTTs, time.Duration(i)*time.Millisecond)
	}
	s := r.Summarize()
	if s.Count != 100 || s.P50 != 50*time.Millisecond || s.P99 != 99*time.Millisecond || s.Max != 100*time.Millisecond {
		t.Fatalf("got %+v", s)
	}
	if (Result{}).Summarize() != (Summary{}) {
		t.Fatal("empty result should summarize to zero")
	}
}
