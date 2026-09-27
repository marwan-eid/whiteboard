// Package hlc implements hybrid logical clock stamps: physical milliseconds
// plus a logical counter, tie-broken by client id, so every stamp is unique
// and all replicas order them identically.
package hlc

import (
	"cmp"
	"sync"

	pb "whiteboard/internal/pb/whiteboard/v1"
)

type Stamp struct {
	WallMs   int64
	Counter  uint32
	ClientID uint64
}

// Compare orders stamps by (WallMs, Counter, ClientID).
func (s Stamp) Compare(o Stamp) int {
	if c := cmp.Compare(s.WallMs, o.WallMs); c != 0 {
		return c
	}
	if c := cmp.Compare(s.Counter, o.Counter); c != 0 {
		return c
	}
	return cmp.Compare(s.ClientID, o.ClientID)
}

func (s Stamp) IsZero() bool { return s == Stamp{} }

func FromProto(p *pb.Stamp) Stamp {
	return Stamp{WallMs: p.GetWallMs(), Counter: p.GetCounter(), ClientID: p.GetClientId()}
}

func (s Stamp) Proto() *pb.Stamp {
	return &pb.Stamp{WallMs: s.WallMs, Counter: s.Counter, ClientId: s.ClientID}
}

// Clock issues monotonically increasing stamps for one client (or the server).
// It is safe for concurrent use.
type Clock struct {
	mu       sync.Mutex
	nowMs    func() int64
	clientID uint64
	wallMs   int64
	counter  uint32
}

func NewClock(clientID uint64, nowMs func() int64) *Clock {
	return &Clock{clientID: clientID, nowMs: nowMs}
}

// Now returns a stamp greater than every stamp previously issued or observed.
func (c *Clock) Now() Stamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	if phys := c.nowMs(); phys > c.wallMs {
		c.wallMs, c.counter = phys, 0
	} else {
		c.tick()
	}
	return Stamp{WallMs: c.wallMs, Counter: c.counter, ClientID: c.clientID}
}

// Observe advances the clock past a stamp seen from elsewhere, so later
// local stamps order after it.
func (c *Clock) Observe(s Stamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case s.WallMs > c.wallMs:
		c.wallMs, c.counter = s.WallMs, s.Counter
	case s.WallMs == c.wallMs && s.Counter > c.counter:
		c.counter = s.Counter
	}
}

func (c *Clock) tick() {
	if c.counter == ^uint32(0) {
		c.wallMs, c.counter = c.wallMs+1, 0
		return
	}
	c.counter++
}
