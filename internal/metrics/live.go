package metrics

import (
	"sync"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// LiveWindow is how far back the live stats look.
const LiveWindow = 10 * time.Second

// Live keeps the recent numbers the public stats panel shows, which
// Prometheus histograms cannot give directly (percentiles over the last few
// seconds). Batches are counted in one-second buckets.
type Live struct {
	mu      sync.Mutex
	buckets [int(LiveWindow/time.Second) + 1]liveBucket
	now     func() time.Time
}

type liveBucket struct {
	second  int64
	batches int64
	latency *hdrhistogram.Histogram // microseconds
}

func newLive(now func() time.Time) *Live {
	l := &Live{now: now}
	for i := range l.buckets {
		l.buckets[i].latency = hdrhistogram.New(1, int64(time.Minute/time.Microsecond), 2)
	}
	return l
}

// RecordBatch counts an applied batch and its server-side sync latency.
func (l *Live) RecordBatch(latency time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.bucket(l.now().Unix())
	b.batches++
	_ = b.latency.RecordValue(max(1, latency.Microseconds()))
}

// bucket returns the bucket for a second, clearing it if it held an older one.
func (l *Live) bucket(sec int64) *liveBucket {
	b := &l.buckets[sec%int64(len(l.buckets))]
	if b.second != sec {
		b.second, b.batches = sec, 0
		b.latency.Reset()
	}
	return b
}

// LiveStats is a snapshot over the complete seconds of the last LiveWindow.
type LiveStats struct {
	// EditsPerSec is batches applied per second on this node, all boards.
	EditsPerSec float64 `json:"editsPerSec"`
	// SyncP50Ms and SyncP99Ms are server-side sync latency: from a batch
	// arriving until it is committed and queued to the other clients.
	SyncP50Ms float64 `json:"syncP50Ms"`
	SyncP99Ms float64 `json:"syncP99Ms"`
	// Samples is how many batches the percentiles are over.
	Samples int64 `json:"samples"`
	WindowS int   `json:"windowS"`
}

func (l *Live) Snapshot() LiveStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().Unix()
	merged := hdrhistogram.New(1, int64(time.Minute/time.Microsecond), 2)
	var batches int64
	window := int64(LiveWindow / time.Second)
	for sec := now - window; sec < now; sec++ {
		b := &l.buckets[sec%int64(len(l.buckets))]
		if b.second != sec {
			continue
		}
		batches += b.batches
		merged.Merge(b.latency)
	}
	ms := func(q float64) float64 { return float64(merged.ValueAtQuantile(q)) / 1000 }
	return LiveStats{
		EditsPerSec: float64(batches) / float64(window),
		SyncP50Ms:   ms(50),
		SyncP99Ms:   ms(99),
		Samples:     merged.TotalCount(),
		WindowS:     int(window),
	}
}

// GaugeValue reads a gauge's current value.
func GaugeValue(g prometheus.Gauge) float64 {
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		return 0
	}
	return m.GetGauge().GetValue()
}
