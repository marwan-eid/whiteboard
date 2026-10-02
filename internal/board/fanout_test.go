package board

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func TestSelectNearest(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 500 {
		n := 1 + rng.IntN(200)
		c := make([]cursorCand, n)
		for i := range c {
			c[i] = cursorCand{id: uint64(i), d: float64(rng.IntN(50))} // many ties
		}
		k := 1 + rng.IntN(n)
		want := make([]float64, n)
		for i, x := range c {
			want[i] = x.d
		}
		slices.Sort(want)
		selectNearest(c, k)
		got := make([]float64, k)
		for i := range k {
			got[i] = c[i].d
		}
		slices.Sort(got)
		if !slices.Equal(got, want[:k]) {
			t.Fatalf("trial %d (n=%d k=%d): got %v want %v", trial, n, k, got, want[:k])
		}
	}
}
