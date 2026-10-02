// Command chaoscheck evaluates a chaos run (docs/BENCHMARKS.md, target 5):
//
//   - every batch the load generator saw acknowledged must be in the durable
//     log (rows and compacted segments), exactly once;
//
//   - recovery is the time from the event (a node killed or paused) until
//     99% of editors receive frames again, from the loadgen's timeline.
//
//     go run ./cmd/chaoscheck -database postgres://... -board chaos \
//     -acked acked.txt -timeline timeline.csv -event-at <unix seconds> -editors 250
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"whiteboard/internal/db"
	"whiteboard/internal/store"
)

func main() {
	dsn := flag.String("database", os.Getenv("DATABASE_URL"), "Postgres URL")
	board := flag.String("board", "chaos", "board id")
	ackedPath := flag.String("acked", "acked.txt", "acknowledged batches from loadgen -acked")
	timelinePath := flag.String("timeline", "timeline.csv", "per-second timeline from loadgen -timeline")
	eventAt := flag.Float64("event-at", 0, "unix time of the kill or pause")
	editors := flag.Int("editors", 0, "editors in the run")
	flag.Parse()
	ok, err := run(*dsn, *board, *ackedPath, *timelinePath, *eventAt, *editors)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func run(dsn, board, ackedPath, timelinePath string, eventAt float64, editors int) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, dsn, 30*time.Second)
	if err != nil {
		return false, err
	}
	defer pool.Close()
	st, err := store.NewPostgres(pool)
	if err != nil {
		return false, err
	}
	entries, err := st.Range(ctx, board, 0, math.MaxInt64)
	if err != nil {
		return false, err
	}
	logged := map[string]int{}
	for _, e := range entries {
		logged[fmt.Sprintf("%d:%d", e.ClientID, e.ClientSeq)]++
	}
	duplicates := 0
	for _, n := range logged {
		duplicates += n - 1
	}

	f, err := os.Open(ackedPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	acked, missing := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			acked++
			if logged[line] == 0 {
				missing++
			}
		}
	}
	if err := sc.Err(); err != nil {
		return false, err
	}

	fmt.Printf("| Acknowledged batches | In the log | Lost (acked, not logged) | Duplicated in the log |\n|---|---|---|---|\n")
	fmt.Printf("| %d | %d | %d | %d |\n\n", acked, len(entries), missing, duplicates)

	if eventAt > 0 && editors > 0 {
		if err := recovery(timelinePath, eventAt, editors); err != nil {
			return false, err
		}
	}
	return missing == 0 && duplicates == 0, nil
}

type second struct {
	at        int64
	receiving int
	samples   int
	p99       float64
}

// recovery reports how long editors went without frames after the event and
// how latency behaved, from the per-second timeline.
func recovery(path string, eventAt float64, editors int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return err
	}
	var secs []second
	for _, r := range rows[1:] {
		at, _ := strconv.ParseInt(r[0], 10, 64)
		recv, _ := strconv.Atoi(r[1])
		n, _ := strconv.Atoi(r[2])
		p99, _ := strconv.ParseFloat(r[4], 64)
		secs = append(secs, second{at, recv, n, p99})
	}
	event := int64(eventAt)
	need := int(math.Ceil(0.99 * float64(editors)))

	var before []float64 // per-second p99 in the minute before the event
	for _, s := range secs {
		if s.at >= event-60 && s.at < event && s.samples > 0 {
			before = append(before, s.p99)
		}
	}
	recovered := int64(-1)
	for _, s := range secs {
		if s.at > event && s.receiving >= need {
			recovered = s.at
			break
		}
	}
	worst := 0.0 // highest per-second p99 from the event until 10 s after recovery
	for _, s := range secs {
		if s.at >= event && (recovered < 0 || s.at <= recovered+10) && s.p99 > worst {
			worst = s.p99
		}
	}
	median := func(xs []float64) float64 {
		if len(xs) == 0 {
			return 0
		}
		slices.Sort(xs)
		return xs[len(xs)/2]
	}
	fmt.Printf("| Recovery: event until 99%% of editors get frames | Per-second p99 before (median of the minute) | Worst per-second p99 during recovery |\n|---|---|---|\n")
	rec := "not within the run"
	if recovered >= 0 {
		rec = fmt.Sprintf("%.1f s", float64(recovered)-eventAt)
	}
	fmt.Printf("| %s | %.1f ms | %.1f ms |\n", rec, median(before), worst)
	return nil
}
