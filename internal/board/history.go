package board

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"whiteboard/internal/doc"
	"whiteboard/internal/hlc"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

// History: the board as of any committed seq is the nearest snapshot (or
// cached keyframe) at or before it, plus replay of the log after it. This
// runs on the caller's goroutine, not the board actor, so scrubbing through
// history never delays live editing.

const (
	// A state this far from its base is cached, so scrubbing nearby is fast.
	keyframeEvery = 1000
	maxKeyframes  = 8
)

type historian struct {
	store   Store
	boardID string

	mu     sync.Mutex
	frames map[uint64]*doc.Doc // never mutated after caching
	recent []uint64            // least recently used first
}

func newHistorian(store Store, boardID string) *historian {
	return &historian{store: store, boardID: boardID, frames: map[uint64]*doc.Doc{}}
}

// stateAt rebuilds the board right after batch seq and reports that batch's wall time.
func (h *historian) stateAt(ctx context.Context, seq uint64) (*doc.Doc, int64, error) {
	var base *doc.Doc
	var baseSeq uint64
	h.mu.Lock()
	for s, d := range h.frames {
		if s <= seq && (base == nil || s > baseSeq) {
			base, baseSeq = d, s
		}
	}
	if base != nil {
		h.touch(baseSeq)
		base = base.Clone()
	}
	h.mu.Unlock()

	snap, err := h.store.SnapshotAtOrBefore(ctx, h.boardID, seq)
	if err != nil {
		return nil, 0, err
	}
	if snap != nil && (base == nil || snap.GetSeq() > baseSeq) {
		if base, err = doc.FromSnapshot(snap.GetObjects()); err != nil {
			return nil, 0, fmt.Errorf("snapshot %d: %w", snap.GetSeq(), err)
		}
		baseSeq = snap.GetSeq()
	}
	if base == nil {
		base = doc.New()
	}

	entries, err := h.store.Range(ctx, h.boardID, baseSeq, seq)
	if err != nil {
		return nil, 0, err
	}
	var wall int64
	for _, e := range entries {
		base.ApplyBatch(e.Ops, hlc.FromProto(e.Stamp))
		if e.Seq == seq {
			wall = e.Stamp.GetWallMs()
		}
	}
	if seq-baseSeq >= keyframeEvery {
		h.mu.Lock()
		h.frames[seq] = base.Clone()
		h.touch(seq)
		for len(h.recent) > maxKeyframes {
			delete(h.frames, h.recent[0])
			h.recent = h.recent[1:]
		}
		h.mu.Unlock()
	}
	return base, wall, nil
}

// touch marks a keyframe as most recently used; h.mu must be held.
func (h *historian) touch(seq uint64) {
	for i, s := range h.recent {
		if s == seq {
			h.recent = append(h.recent[:i], h.recent[i+1:]...)
			break
		}
	}
	h.recent = append(h.recent, seq)
}

// History returns the board as of seq (capped at the latest committed
// seq), limited to viewport (nil: the whole board).
func (b *Board) History(ctx context.Context, seq uint64, viewport *pb.Viewport) (*pb.History, error) {
	seq = min(seq, b.committedSeq.Load())
	d, wall, err := b.hist.stateAt(ctx, seq)
	if err != nil {
		return nil, err
	}
	return &pb.History{Seq: seq, WallMs: wall, Objects: ObjectsInView(d, viewport)}, nil
}

// Restore makes the board look as it did at seq by appending the difference
// as one batch from the server, sent to everyone (the requester included).
func (b *Board) Restore(ctx context.Context, seq uint64) error {
	seq = min(seq, b.committedSeq.Load())
	past, _, err := b.hist.stateAt(ctx, seq)
	if err != nil {
		return err
	}
	reply := make(chan error, 1)
	if err := b.send(ctx, restoreMsg{past: past, reply: reply}); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-b.done:
		return b.closedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

type restoreMsg struct {
	past  *doc.Doc
	reply chan error
}

// applyRestore runs on the actor. The batch has client id 0 (the server), so
// no client skips it as its own, and a stamp newer than every edit so far.
func (b *Board) applyRestore(past *doc.Doc) {
	ops := doc.Diff(b.doc, past)
	if len(ops) == 0 {
		return
	}
	st := b.clock.Now()
	b.applyOps(0, 0, ops, st, time.Now())
	b.metrics.Restores.Inc()
}

// ObjectsInView returns the visible objects of d whose box intersects v (nil:
// all), in full, sorted by id.
func ObjectsInView(d *doc.Doc, v *pb.Viewport) []*pb.ObjectState {
	view, _ := viewRect(v)
	var out []*pb.ObjectState
	d.Range(func(o *doc.Object) bool {
		if r, ok := bounds(o); ok && r.Intersects(view) {
			out = append(out, o.State())
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out
}
