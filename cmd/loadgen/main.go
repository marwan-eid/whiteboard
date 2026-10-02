// Command loadgen runs simulated editors against a node and reports
// sender-to-receiver sync latency. It exits non-zero if any editor fails to
// connect, so CI can use it as a smoke test.
//
//	go run ./cmd/loadgen -url ws://localhost:8080/ws -editors 200 -duration 30s
//	go run ./cmd/loadgen -scenario loadgen/scenarios/editors.yaml -editors 1000 -hlog run.hlog
//
// A scenario file sets the defaults; flags given explicitly override it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"gopkg.in/yaml.v3"

	"whiteboard/internal/loadgen"
)

// scenario is the YAML form of a run (loadgen/scenarios/).
type scenario struct {
	Editors   int           `yaml:"editors"`
	Ramp      time.Duration `yaml:"ramp"`
	Warmup    time.Duration `yaml:"warmup"`
	Duration  time.Duration `yaml:"duration"`
	OpsPerSec float64       `yaml:"ops_per_sec"`
	Mix       struct {
		Drag, Create, Text, Delete float64
	} `yaml:"mix"`
	Drag struct {
		Hz       float64
		Min, Max time.Duration
	} `yaml:"drag"`
	CursorHz float64 `yaml:"cursor_hz"`
	Hotspot  float64 `yaml:"hotspot"`
	Area     float64 `yaml:"area"`
	View     struct {
		W, H float64
	} `yaml:"view"`
	ObjectsPerEditor int `yaml:"objects_per_editor"`
}

func main() {
	os.Exit(run())
}

// scenarioPath finds -scenario before the flags are defined, since the
// scenario supplies their defaults.
func scenarioPath(args []string) string {
	for i, a := range args {
		a = strings.TrimLeft(a, "-")
		if v, ok := strings.CutPrefix(a, "scenario="); ok {
			return v
		}
		if a == "scenario" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func run() int {
	cfg := loadgen.Config{
		Editors: 100, Ramp: 5 * time.Second, Warmup: 2 * time.Second, Duration: 30 * time.Second,
		OpsPerSec: 1, CursorHz: 15, Hotspot: 0.5, Area: 20_000,
	}
	if path := scenarioPath(os.Args[1:]); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		var s scenario
		if err := yaml.Unmarshal(data, &s); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			return 2
		}
		cfg.Editors, cfg.Ramp, cfg.Warmup, cfg.Duration = s.Editors, s.Ramp, s.Warmup, s.Duration
		cfg.OpsPerSec, cfg.CursorHz, cfg.Hotspot, cfg.Area = s.OpsPerSec, s.CursorHz, s.Hotspot, s.Area
		cfg.DragShare, cfg.CreateShare, cfg.TextShare, cfg.DeleteShare = s.Mix.Drag, s.Mix.Create, s.Mix.Text, s.Mix.Delete
		cfg.DragHz, cfg.DragMin, cfg.DragMax = s.Drag.Hz, s.Drag.Min, s.Drag.Max
		cfg.ViewW, cfg.ViewH, cfg.ObjectsPerEditor = s.View.W, s.View.H, s.ObjectsPerEditor
	}

	var out, hlog string
	flag.String("scenario", "", "YAML scenario file (loadgen/scenarios/); flags override it")
	flag.StringVar(&cfg.URL, "url", "ws://localhost:8080/ws", "WebSocket URL of a node (or Caddy)")
	flag.StringVar(&cfg.BoardID, "board", fmt.Sprintf("load-%d", time.Now().Unix()), "board id")
	flag.IntVar(&cfg.Editors, "editors", cfg.Editors, "simulated editors")
	flag.DurationVar(&cfg.Ramp, "ramp", cfg.Ramp, "spread connects over this period")
	flag.DurationVar(&cfg.Warmup, "warmup", cfg.Warmup, "excluded from measurement, after the ramp")
	flag.DurationVar(&cfg.Duration, "duration", cfg.Duration, "editing time after the ramp")
	flag.Float64Var(&cfg.OpsPerSec, "ops", cfg.OpsPerSec, "batches per editor per second (drags, creates, text, deletes)")
	flag.Float64Var(&cfg.CursorHz, "cursor-hz", cfg.CursorHz, "cursor updates per editor per second")
	flag.Float64Var(&cfg.Hotspot, "hotspot", cfg.Hotspot, "fraction of editors viewing the shared center region")
	flag.Float64Var(&cfg.Area, "area", cfg.Area, "side of the square board area editors spread over")
	flag.Uint64Var(&cfg.Seed, "seed", uint64(time.Now().UnixNano()), "random seed")
	flag.StringVar(&out, "json", "", "also write the summary as JSON to this file")
	var cpuprofile string
	flag.StringVar(&hlog, "hlog", "", "also write the latency histogram (HdrHistogram log, microseconds) to this file")
	flag.StringVar(&cpuprofile, "cpuprofile", "", "write a CPU profile of the load generator to this file")
	flag.Parse()
	if cpuprofile != "" {
		f, err := os.Create(cpuprofile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer pprof.StopCPUProfile()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	started := time.Now()
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
	if hlog != "" {
		if err := writeHlog(hlog, res.Latency, started); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if s.Failed > 0 || s.Late > 0 || s.Connected == 0 {
		return 1
	}
	return 0
}

func writeHlog(path string, h *hdrhistogram.Histogram, started time.Time) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := hdrhistogram.NewHistogramLogWriter(f)
	err = w.OutputLogFormatVersion()
	if err == nil {
		err = w.OutputStartTime(started.UnixMilli())
	}
	if err == nil {
		err = w.OutputIntervalHistogram(h)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
