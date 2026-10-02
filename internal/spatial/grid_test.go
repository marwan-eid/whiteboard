package spatial

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func drawRect(t *rapid.T, label string) Rect {
	return Rect{
		X: float64(rapid.IntRange(-3000, 3000).Draw(t, label+".x")),
		Y: float64(rapid.IntRange(-3000, 3000).Draw(t, label+".y")),
		W: float64(rapid.IntRange(0, 2500).Draw(t, label+".w")),
		H: float64(rapid.IntRange(0, 2500).Draw(t, label+".h")),
	}
}

// The grid returns exactly what a brute-force scan returns, each id once,
// after any sequence of inserts, moves and removals.
func TestQueryMatchesBruteForce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := NewGrid(512, 16)
		model := map[string]Rect{}
		for i := range rapid.IntRange(1, 80).Draw(t, "ops") {
			id := fmt.Sprintf("o%d", rapid.IntRange(0, 20).Draw(t, "id"))
			if rapid.IntRange(0, 5).Draw(t, "remove?") == 0 {
				g.Remove(id)
				delete(model, id)
				continue
			}
			r := drawRect(t, fmt.Sprint("r", i))
			g.Set(id, r)
			model[id] = r
		}
		q := drawRect(t, "q")
		var got []string
		g.Query(q, func(id string, _ Rect) bool { got = append(got, id); return true })
		var want []string
		for id, r := range model {
			if r.Intersects(q) {
				want = append(want, id)
			}
		}
		sort.Strings(got)
		sort.Strings(want)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("query %v:\n got  %v\n want %v", q, got, want)
		}
		if g.Len() != len(model) {
			t.Fatalf("Len = %d, want %d", g.Len(), len(model))
		}
	})
}

func TestEverythingQueryAndEarlyStop(t *testing.T) {
	g := NewGrid(100, 4)
	for i := range 50 {
		g.Set(fmt.Sprint(i), Rect{X: float64(i * 1000), Y: 0, W: 10, H: 10})
	}
	g.Set("huge", Rect{X: -1e6, Y: -1e6, W: 2e6, H: 2e6})
	n := 0
	g.Query(Everything, func(string, Rect) bool { n++; return true })
	if n != 51 {
		t.Fatalf("Everything matched %d, want 51", n)
	}
	n = 0
	g.Query(Everything, func(string, Rect) bool { n++; return n < 3 })
	if n != 3 {
		t.Fatalf("early stop after %d, want 3", n)
	}
	// The oversized rect is found from any small query it covers.
	found := false
	g.Query(Rect{X: 500, Y: 500, W: 1, H: 1}, func(id string, _ Rect) bool { found = found || id == "huge"; return true })
	if !found {
		t.Fatal("large rect not returned")
	}
}

// Viewport queries on a 100k-object grid board (docs/BENCHMARKS.md target 2,
// server side), at random positions, with the board's grid settings. It
// reports p50 and p99 per query as well as the mean.
func BenchmarkQueryViewport100k(b *testing.B) {
	g := NewGrid(512, 256) // as in board.newBoard
	for i := range 100_000 {
		x, y := float64(i%316)*220, float64(i/316)*160
		g.Set(fmt.Sprint(i), Rect{X: x, Y: y, W: 180, H: 120})
	}
	rng := rand.New(rand.NewPCG(1, 2))
	var times []time.Duration
	b.ResetTimer()
	for b.Loop() {
		// 1920x1080 plus 50% margin, anywhere on the 69,500 x 50,500 board.
		view := Rect{X: rng.Float64() * 66_000, Y: rng.Float64() * 48_000, W: 2880, H: 1620}
		start := time.Now()
		n := 0
		g.Query(view, func(string, Rect) bool { n++; return true })
		times = append(times, time.Since(start))
	}
	slices.Sort(times)
	b.ReportMetric(float64(times[len(times)/2].Nanoseconds()), "p50-ns/query")
	b.ReportMetric(float64(times[len(times)*99/100].Nanoseconds()), "p99-ns/query")
}

func BenchmarkMove100k(b *testing.B) {
	g := NewGrid(512, 64)
	for i := range 100_000 {
		g.Set(fmt.Sprint(i), Rect{X: float64(i%316) * 220, Y: float64(i/316) * 160, W: 180, H: 120})
	}
	b.ResetTimer()
	i := 0
	for b.Loop() {
		i++
		g.Set("5000", Rect{X: float64(i % 5000), Y: 700, W: 180, H: 120})
	}
}
