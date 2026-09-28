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

func TestEditorsMeasureSyncLatency(t *testing.T) {
	res := Run(context.Background(), Config{
		URL:       startNode(t),
		BoardID:   "load",
		Editors:   20,
		Ramp:      200 * time.Millisecond,
		Warmup:    300 * time.Millisecond,
		Duration:  1500 * time.Millisecond,
		OpsPerSec: 10,
		CursorHz:  15,
		Hotspot:   1, // everyone watches the same region, so everyone receives everything
		Seed:      1,
	})
	s := res.Summarize()
	if s.Failed != 0 || s.Late != 0 || s.Connected != 20 {
		t.Fatalf("connected=%d failed=%d errors=%v", s.Connected, s.Failed, res.Errors)
	}
	if s.OpsSent == 0 || s.Samples == 0 {
		t.Fatalf("no traffic measured: %+v", s)
	}
	// Each op is received by up to 19 other editors.
	if s.Samples > s.OpsSent*19 {
		t.Fatalf("%d samples for %d ops", s.Samples, s.OpsSent)
	}
	if s.P50ms <= 0 || s.P50ms > s.P99ms || s.P99ms > s.MaxMs {
		t.Fatalf("implausible percentiles: %+v", s)
	}
}

func TestSpreadEditorsReceiveLess(t *testing.T) {
	url := startNode(t)
	run := func(board string, hotspot float64) Summary {
		return Run(context.Background(), Config{
			URL: url, BoardID: board, Editors: 10, Duration: time.Second,
			OpsPerSec: 10, Hotspot: hotspot, Area: 1e6, Seed: 2,
		}).Summarize()
	}
	together, apart := run("together", 1), run("apart", 0)
	// Viewport interest: editors far apart receive (almost) none of each other's ops.
	if apart.Samples*5 > together.Samples {
		t.Fatalf("spread editors received %d samples vs %d together", apart.Samples, together.Samples)
	}
}
