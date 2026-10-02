package metrics

import (
	"math"
	"testing"
	"time"
)

func TestLiveWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLive(func() time.Time { return now })
	for range 98 {
		l.RecordBatch(time.Millisecond)
	}
	l.RecordBatch(100 * time.Millisecond)
	l.RecordBatch(100 * time.Millisecond)

	// The current second is not complete yet, so it is not counted.
	if s := l.Snapshot(); s.Samples != 0 || s.EditsPerSec != 0 {
		t.Fatalf("current second counted: %+v", s)
	}
	now = now.Add(time.Second)
	l.RecordBatch(time.Second) // in the new current second
	s := l.Snapshot()
	near := func(got, want float64) bool { return math.Abs(got-want) <= want*0.02 }
	if s.Samples != 100 || s.EditsPerSec != 10 || !near(s.SyncP50Ms, 1) || !near(s.SyncP99Ms, 100) || s.WindowS != 10 {
		t.Fatalf("snapshot = %+v", s)
	}
	// Ten seconds on, the old second has left the window.
	now = now.Add(10 * time.Second)
	if s := l.Snapshot(); s.Samples != 1 || !near(s.SyncP50Ms, 1000) {
		t.Fatalf("after the window moved: %+v", s)
	}
	now = now.Add(time.Second)
	if s := l.Snapshot(); s.Samples != 0 {
		t.Fatalf("window should be empty: %+v", s)
	}
}
