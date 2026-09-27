package board

import (
	"cmp"
	"runtime"
	"slices"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"

	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/spatial"
)

// Fan-out: once per tick every client gets one frame. Building frames is
// independent per client, so it is split across worker goroutines while the
// actor waits. Workers only read board state; the only per-client state
// they write belongs to their own clients, and kicking a slow client (which
// changes b.clients) happens back on the actor afterwards.

const (
	// minClientsPerWorker keeps small boards on one goroutine, where
	// spawning workers would cost more than it saves.
	minClientsPerWorker = 32
	// presenceEvery ticks (20 ms each) cursors are sent: about 16 Hz, the
	// rate clients send them at.
	presenceEvery = 3
)

// tickFrames is what this tick's frames are assembled from.
type tickFrames struct {
	batches  []encodedBatch
	presence bool            // cursors are recomputed this tick
	moved    map[uint64]bool // cursors that moved since the last presence tick
	online   int             // clients on the board, or -1 if unchanged
}

type target struct {
	id uint64
	c  *clientState
}

// frameWorker is one goroutine's reusable scratch space.
type frameWorker struct {
	buf    []byte
	sel    [][]byte
	enter  map[string][]byte // encoded Frame.objects entries, per tick
	cursor map[uint64][]byte // encoded Frame.cursors entries, per tick
	cands  []cursorCand
	failed []Conn
}

type cursorCand struct {
	id   uint64
	x, y float64
	d    float64
}

func (b *Board) sendFrames(t *tickFrames, targets []target) {
	n := max(1, min(runtime.GOMAXPROCS(0), len(targets)/minClientsPerWorker))
	for len(b.workers) < n {
		b.workers = append(b.workers, &frameWorker{})
	}
	for _, w := range b.workers[:n] {
		w.enter = map[string][]byte{}
		w.cursor = map[uint64][]byte{}
		w.failed = w.failed[:0]
	}
	if n == 1 {
		for _, tg := range targets {
			b.buildFrame(b.workers[0], tg.id, tg.c, t)
		}
	} else {
		var wg sync.WaitGroup
		chunk := (len(targets) + n - 1) / n
		for i, w := range b.workers[:n] {
			lo, hi := min(i*chunk, len(targets)), min((i+1)*chunk, len(targets))
			wg.Go(func() {
				for _, tg := range targets[lo:hi] {
					b.buildFrame(w, tg.id, tg.c, t)
				}
			})
		}
		wg.Wait()
	}
	for _, w := range b.workers[:n] {
		for _, conn := range w.failed {
			b.metrics.ClientsKicked.WithLabelValues("slow_consumer").Inc()
			conn.Kick(KickSlow)
			b.remove(conn)
		}
	}
}

// buildFrame assembles and queues one client's frame. It runs on a worker.
func (b *Board) buildFrame(w *frameWorker, id uint64, c *clientState, t *tickFrames) {
	fb := newFrameBuilder(w.buf)
	defer func() { w.buf = fb.body }()

	b.judge(id, c)
	for k := 0; k < len(c.refs); {
		bi := c.refs[k].batch
		w.sel = w.sel[:0]
		for ; k < len(c.refs) && c.refs[k].batch == bi; k++ {
			entry := t.batches[bi].ops[c.refs[k].op]
			if c.refs[k].lod {
				entry = t.batches[bi].lodOps[c.refs[k].op]
			}
			if entry != nil {
				w.sel = append(w.sel, entry)
			}
		}
		if len(w.sel) > 0 {
			fb.batch(t.batches[bi].header, w.sel)
		}
	}
	c.refs, c.evaluatedUpTo = c.refs[:0], 0

	for _, a := range b.outAcks[id] {
		fb.message(fieldFrameAcks, a)
	}
	if t.presence && (len(t.moved) > 0 || c.presenceStale) {
		b.appendCursors(w, fb, id, c, t.moved)
		c.presenceStale = false
	}
	if len(c.moves) > 0 {
		for _, oid := range sortedMoves(c.moves) {
			if c.moves[oid] {
				fb.raw(b.enterEntry(w.enter, oid, c.lod))
			} else {
				fb.leave(oid)
			}
		}
		clear(c.moves)
	}
	if t.online >= 0 {
		fb.online(uint32(t.online))
	}
	if fb.empty {
		return
	}
	fb.seq(b.seq)
	msg := fb.serverMessage()
	if !c.conn.Send(msg) {
		w.failed = append(w.failed, c.conn)
		return
	}
	b.metrics.FanoutBytes.Add(float64(len(msg)))
}

// appendCursors adds the cursors a client should see: others in its view,
// nearest to its center first, at most maxCursorsPerClient. It sends cursors
// that moved or newly appeared, and gone for those that left its selection.
func (b *Board) appendCursors(w *frameWorker, fb *frameBuilder, id uint64, c *clientState, moved map[uint64]bool) {
	cx, cy := c.view.X+c.view.W/2, c.view.Y+c.view.H/2
	w.cands = w.cands[:0]
	b.cursorGrid.Query(c.view, func(key string, r spatial.Rect) bool {
		if cid := b.cursorIDs[key]; cid != id {
			dx, dy := r.X-cx, r.Y-cy
			w.cands = append(w.cands, cursorCand{cid, r.X, r.Y, dx*dx + dy*dy})
		}
		return true
	})
	if len(w.cands) > maxCursorsPerClient {
		slices.SortFunc(w.cands, func(a, b cursorCand) int {
			switch {
			case a.d < b.d:
				return -1
			case a.d > b.d:
				return 1
			}
			return 0
		})
		w.cands = w.cands[:maxCursorsPerClient]
	}
	slices.SortFunc(w.cands, func(a, b cursorCand) int { return cmp.Compare(a.id, b.id) })

	visible := make(map[uint64]struct{}, len(w.cands))
	for _, k := range w.cands {
		visible[k.id] = struct{}{}
		if _, shown := c.shownCursors[k.id]; !shown || moved[k.id] {
			fb.raw(w.cursorEntry(k))
		}
	}
	for cid := range c.shownCursors {
		if _, ok := visible[cid]; !ok {
			fb.message(fieldFrameCursors, &pb.CursorUpdate{ClientId: cid, Gone: true})
		}
	}
	c.shownCursors = visible
}

// cursorEntry encodes a cursor position once per tick per worker.
func (w *frameWorker) cursorEntry(k cursorCand) []byte {
	if e, ok := w.cursor[k.id]; ok {
		return e
	}
	e := appendMessage(nil, protowire.Number(fieldFrameCursors), &pb.CursorUpdate{ClientId: k.id, X: k.x, Y: k.y})
	w.cursor[k.id] = e
	return e
}
