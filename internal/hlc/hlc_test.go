package hlc

import (
	"testing"

	"pgregory.net/rapid"
)

func TestCompareOrder(t *testing.T) {
	ordered := []Stamp{
		{WallMs: 1, Counter: 0, ClientID: 9},
		{WallMs: 1, Counter: 1, ClientID: 1},
		{WallMs: 1, Counter: 1, ClientID: 2},
		{WallMs: 2, Counter: 0, ClientID: 0},
	}
	for i := range ordered {
		for j := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := ordered[i].Compare(ordered[j]); got != want {
				t.Errorf("%v.Compare(%v) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}

func TestNowIsStrictlyMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		phys := int64(1000)
		c := NewClock(7, func() int64 { return phys })
		prev := c.Now()
		for range rapid.IntRange(1, 200).Draw(t, "steps") {
			switch rapid.IntRange(0, 2).Draw(t, "action") {
			case 0: // physical clock moves, possibly backwards
				phys += rapid.Int64Range(-50, 50).Draw(t, "dt")
			case 1: // observe a remote stamp
				c.Observe(Stamp{
					WallMs:   phys + rapid.Int64Range(-100, 100).Draw(t, "rw"),
					Counter:  rapid.Uint32Range(0, 5).Draw(t, "rc"),
					ClientID: 99,
				})
			}
			next := c.Now()
			if next.Compare(prev) <= 0 {
				t.Fatalf("stamp went backwards: %v then %v", prev, next)
			}
			prev = next
		}
	})
}

func TestNowOrdersAfterObserved(t *testing.T) {
	c := NewClock(1, func() int64 { return 100 })
	remote := Stamp{WallMs: 500, Counter: 3, ClientID: 2}
	c.Observe(remote)
	if got := c.Now(); got.Compare(remote) <= 0 {
		t.Fatalf("Now() = %v, want after observed %v", got, remote)
	}
}

func TestCounterOverflowAdvancesWall(t *testing.T) {
	c := NewClock(1, func() int64 { return 10 })
	c.Observe(Stamp{WallMs: 10, Counter: ^uint32(0)})
	got := c.Now()
	if got.WallMs != 11 || got.Counter != 0 {
		t.Fatalf("got %v, want wall 11 counter 0", got)
	}
}

func TestProtoRoundTrip(t *testing.T) {
	s := Stamp{WallMs: 1_700_000_000_000, Counter: 42, ClientID: 1<<53 - 1}
	if got := FromProto(s.Proto()); got != s {
		t.Fatalf("round trip: %v != %v", got, s)
	}
	if !FromProto(nil).IsZero() {
		t.Fatal("nil proto should be the zero stamp")
	}
}
