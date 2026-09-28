// Command loadgen runs simulated editors against a node and reports
// sender-to-receiver sync latency. It exits non-zero if any editor fails to
// connect, so CI can use it as a smoke test.
//
//	go run ./cmd/loadgen -url ws://localhost:8080/ws -editors 200 -duration 30s
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"whiteboard/internal/loadgen"
)

func main() {
	os.Exit(run())
}

func run() int {
	var cfg loadgen.Config
	var out string
	flag.StringVar(&cfg.URL, "url", "ws://localhost:8080/ws", "WebSocket URL of a node (or Caddy)")
	flag.StringVar(&cfg.BoardID, "board", fmt.Sprintf("load-%d", time.Now().Unix()), "board id")
	flag.IntVar(&cfg.Editors, "editors", 100, "simulated editors")
	flag.DurationVar(&cfg.Ramp, "ramp", 5*time.Second, "spread connects over this period")
	flag.DurationVar(&cfg.Warmup, "warmup", 2*time.Second, "excluded from measurement, after the ramp")
	flag.DurationVar(&cfg.Duration, "duration", 30*time.Second, "editing time after the ramp")
	flag.Float64Var(&cfg.OpsPerSec, "ops", 1, "move batches per editor per second")
	flag.Float64Var(&cfg.CursorHz, "cursor-hz", 15, "cursor updates per editor per second")
	flag.Float64Var(&cfg.Hotspot, "hotspot", 0.5, "fraction of editors viewing the shared center region")
	flag.Float64Var(&cfg.Area, "area", 20_000, "side of the square board area editors spread over")
	flag.Uint64Var(&cfg.Seed, "seed", uint64(time.Now().UnixNano()), "random seed")
	flag.StringVar(&out, "json", "", "also write the summary as JSON to this file")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	res := loadgen.Run(ctx, cfg)
	s := res.Summarize()
	fmt.Printf("editors: %d connected, %d failed; %d ops sent in %.1fs\n", s.Connected, s.Failed, s.OpsSent, s.ElapsedS)
	if s.Late > 0 {
		fmt.Printf("  %d editors connected only after the warmup (a server limit? see MAX_CONNS_PER_IP): the run did not have its full load\n", s.Late)
	}
	for msg, n := range res.Errors {
		fmt.Printf("  %dx %s\n", n, msg)
	}
	fmt.Printf("sync latency (sender to receiver, %d samples): p50=%.1fms p90=%.1fms p99=%.1fms p99.9=%.1fms max=%.1fms\n",
		s.Samples, s.P50ms, s.P90ms, s.P99ms, s.P999ms, s.MaxMs)
	if out != "" {
		data, _ := json.MarshalIndent(map[string]any{"config": cfg, "summary": s}, "", "  ")
		if err := os.WriteFile(out, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if s.Failed > 0 || s.Late > 0 || s.Connected == 0 {
		return 1
	}
	return 0
}
