package board

import (
	"context"
	"errors"
	"hash/fnv"
	"sync/atomic"
	"time"
)

// Placement decides which node serves each board (internal/cluster): a node
// must hold a board's lease to load it. Leases have epochs; a lease taken
// over by another node cannot be renewed under the old epoch.
type Placement interface {
	// Claim takes the board's lease for this node and returns its epoch, or
	// a *MovedError naming the node that should serve the board.
	Claim(ctx context.Context, boardID string) (uint64, error)
	// Renew extends the given leases and returns the boards whose lease this
	// node no longer holds.
	Renew(ctx context.Context, leases map[string]uint64) (lost []string, err error)
	// Release ends a lease early, so another node can take the board at once.
	Release(ctx context.Context, boardID string, epoch uint64) error
	// RenewEvery is how often to renew; LeaseTTL how long a lease lasts.
	RenewEvery() time.Duration
	LeaseTTL() time.Duration
}

// MovedError says another node serves (or should serve) the board.
type MovedError struct{ Node string }

func (e *MovedError) Error() string { return "board is served by node " + e.Node }

var errLeaseLost = errors.New("board lease lost to another node")

// claimLock serializes claims for one board (striped, so the set of locks
// stays fixed).
func (r *Registry) claimLock(boardID string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(boardID))
	l := &r.claims[h.Sum32()%uint32(len(r.claims))]
	l.Lock()
	return l.Unlock
}

// Run renews this node's leases until ctx ends. Boards whose lease was lost
// are dropped without committing, and their clients reconnect to the new
// owner. If renewal keeps failing (Postgres unreachable), each board stops
// when its lease runs out (leaseClock): another node may own it by then.
// Without a Placement it returns at once.
func (r *Registry) Run(ctx context.Context) {
	p := r.cfg.Placement
	if p == nil {
		return
	}
	t := time.NewTicker(p.RenewEvery())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		started := time.Now()
		boards := r.all()
		leases := make(map[string]uint64, len(boards))
		for _, b := range boards {
			leases[b.id] = b.epoch
		}
		rctx, cancel := context.WithTimeout(ctx, p.RenewEvery())
		lost, err := p.Renew(rctx, leases)
		cancel()
		if err != nil {
			// Boards whose lease runs out before a renewal succeeds stop by
			// themselves (leaseClock).
			r.log.Warn("renewing board leases failed", "err", err, "boards", len(leases))
			continue
		}
		ttl := p.LeaseTTL()
		gone := make(map[string]bool, len(lost))
		for _, id := range lost {
			gone[id] = true
		}
		for _, b := range boards {
			if gone[b.id] {
				r.drop(b, errLeaseLost)
			} else if b.lease != nil {
				b.lease.extend(started, ttl)
			}
		}
	}
}

// drop crashes a board (see crashMsg) without waiting for it.
func (r *Registry) drop(b *Board, err error) {
	select {
	case b.inbox <- crashMsg{stage: "lease", err: err}:
	case <-b.done:
	default:
		go func() {
			select {
			case b.inbox <- crashMsg{stage: "lease", err: err}:
			case <-b.done:
			}
		}()
	}
}

// release gives up an unloaded board's lease in the background; Close waits
// for these.
func (r *Registry) release(b *Board) {
	p := r.cfg.Placement
	if p == nil || r.crashed.Load() {
		return
	}
	r.releases.Add(1)
	go func() {
		defer r.releases.Done()
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.IOTimeout)
		defer cancel()
		if err := p.Release(ctx, b.id, b.epoch); err != nil {
			r.log.Warn("releasing board lease failed", "board", b.id, "err", err)
		}
	}()
}

var errLeaseExpired = errors.New("board lease ran out without renewal")

// leaseClock is when a board's lease runs out, as far as this node knows:
// TTL after the start of the last claim or renewal that succeeded. The
// lease in Postgres lasts at least that long (it is set from the database's
// clock after the request was sent), so a board that stops by this time
// never serves after another node could have taken it, unless this process
// is paused between the check and a commit; the (board_id, seq) fence covers
// that case.
type leaseClock struct {
	until atomic.Int64 // monotonic nanoseconds since leaseEpoch
}

// leaseEpoch makes lease times monotonic (time.Since uses the monotonic clock).
var leaseEpoch = time.Now()

func (l *leaseClock) extend(from time.Time, ttl time.Duration) {
	l.until.Store(int64(from.Sub(leaseEpoch) + ttl))
}

func (l *leaseClock) expired() bool { return int64(time.Since(leaseEpoch)) > l.until.Load() }
