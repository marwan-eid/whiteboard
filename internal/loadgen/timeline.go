package loadgen

import (
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

// Timeline records, per wall-clock second, how many editors received a
// frame and the latency of what they received. A chaos run uses it to see
// how long clients go without frames after a node dies, and how latency
// behaves while they recover.
type Timeline struct {
	mu   sync.Mutex
	secs map[int64]*timelineSecond
}

type timelineSecond struct {
	receivers map[int]struct{}
	latency   *hdrhistogram.Histogram
}

func NewTimeline() *Timeline { return &Timeline{secs: map[int64]*timelineSecond{}} }

// record notes that editor got a frame at t, with one latency sample per
// measured batch in it.
func (tl *Timeline) record(t time.Time, editor int, latenciesUs []int64) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	s := tl.secs[t.Unix()]
	if s == nil {
		s = &timelineSecond{receivers: map[int]struct{}{}, latency: newHistogram()}
		tl.secs[t.Unix()] = s
	}
	s.receivers[editor] = struct{}{}
	for _, us := range latenciesUs {
		_ = s.latency.RecordValue(us)
	}
}

// WriteCSV writes "unix_second,editors_receiving,samples,p50_ms,p99_ms".
func (tl *Timeline) WriteCSV(w io.Writer) error {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	secs := make([]int64, 0, len(tl.secs))
	for s := range tl.secs {
		secs = append(secs, s)
	}
	slices.Sort(secs)
	if _, err := fmt.Fprintln(w, "unix_second,editors_receiving,samples,p50_ms,p99_ms"); err != nil {
		return err
	}
	for _, sec := range secs {
		s := tl.secs[sec]
		if _, err := fmt.Fprintf(w, "%d,%d,%d,%.1f,%.1f\n", sec, len(s.receivers), s.latency.TotalCount(),
			float64(s.latency.ValueAtQuantile(50))/1000, float64(s.latency.ValueAtQuantile(99))/1000); err != nil {
			return err
		}
	}
	return nil
}
