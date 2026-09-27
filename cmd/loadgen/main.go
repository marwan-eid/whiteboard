// Command loadgen opens many simulated clients against a node and reports
// round-trip latency. Exits non-zero if any client fails, so CI can use it
// as a smoke test.
package main

import (
	"context"
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
	flag.StringVar(&cfg.URL, "url", "ws://localhost:8080/ws", "WebSocket URL of a node (or Caddy)")
	flag.StringVar(&cfg.BoardID, "board", "demo", "board id to join")
	flag.IntVar(&cfg.Clients, "clients", 100, "number of simulated clients")
	flag.DurationVar(&cfg.Duration, "duration", 10*time.Second, "how long each client stays connected")
	flag.DurationVar(&cfg.Ramp, "ramp", 2*time.Second, "spread client connects over this period")
	flag.DurationVar(&cfg.PingInterval, "ping-interval", time.Second, "time between pings per client")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	start := time.Now()
	res := loadgen.Run(ctx, cfg)
	s := res.Summarize()

	fmt.Printf("clients: %d connected, %d failed (%s)\n", res.Connected, res.Failed, time.Since(start).Round(time.Millisecond))
	for msg, n := range res.Errors {
		fmt.Printf("  %dx %s\n", n, msg)
	}
	fmt.Printf("ping rtt: n=%d p50=%s p99=%s max=%s\n", s.Count, s.P50, s.P99, s.Max)

	if res.Failed > 0 || res.Connected == 0 {
		return 1
	}
	return 0
}
