package board

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

type fakeConn struct {
	id   uint64
	msgs chan *pb.ServerMessage

	mu     sync.Mutex
	full   bool
	kicked *KickReason
}

func newConn(id uint64) *fakeConn {
	return &fakeConn{id: id, msgs: make(chan *pb.ServerMessage, 1000)}
}

func (c *fakeConn) ClientID() uint64 { return c.id }

func (c *fakeConn) Send(data []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.full {
		return false
	}
	var m pb.ServerMessage
	if err := proto.Unmarshal(data, &m); err != nil {
		panic(err)
	}
	c.msgs <- &m
	return true
}

func (c *fakeConn) Kick(reason KickReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kicked = &reason
}

func (c *fakeConn) kickedFor() *KickReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kicked
}

func (c *fakeConn) next(t *testing.T) *pb.ServerMessage {
	t.Helper()
	for {
		select {
		case m := <-c.msgs:
			if !onlineOnly(m) {
				return m
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("client %d: no message", c.id)
			return nil
		}
	}
}

// onlineOnly reports frames that only update the online count, which the
// tests below ignore.
func onlineOnly(m *pb.ServerMessage) bool {
	f := m.GetFrame()
	return f != nil && len(f.Batches)+len(f.Acks)+len(f.Cursors)+len(f.Objects)+len(f.Leave) == 0
}

func (c *fakeConn) frame(t *testing.T) *pb.Frame {
	t.Helper()
	f := c.next(t).GetFrame()
	if f == nil {
		t.Fatalf("client %d: expected frame", c.id)
	}
	return f
}

func (c *fakeConn) quiet(t *testing.T) {
	t.Helper()
	deadline := time.After(50 * time.Millisecond)
	for {
		select {
		case m := <-c.msgs:
			if !onlineOnly(m) {
				t.Fatalf("client %d: unexpected message %v", c.id, m)
			}
		case <-deadline:
			return
		}
	}
}

var now = time.UnixMilli(1_700_000_000_000)

type env struct {
	reg     *Registry
	metrics *metrics.Metrics
	store   *MemoryStore
}

func newEnv(t *testing.T, mut ...func(*Config)) *env {
	t.Helper()
	cfg := Config{NodeID: "n1", Store: NewMemoryStore(), Tick: 5 * time.Millisecond, Now: func() time.Time { return now }}
	for _, f := range mut {
		f(&cfg)
	}
	m := metrics.New()
	return &env{reg: NewRegistry(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), m), metrics: m, store: cfg.Store.(*MemoryStore)}
}

func (e *env) join(t *testing.T, boardID string, c *fakeConn) (*Board, *pb.Welcome) {
	t.Helper()
	b, err := e.reg.Join(context.Background(), boardID, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := c.next(t).GetWelcome()
	if w == nil {
		t.Fatal("expected welcome")
	}
	return b, w
}

func batch(cs uint64, wall int64, ops ...*pb.Op) *pb.OpBatch {
	return &pb.OpBatch{ClientSeq: cs, Stamp: &pb.Stamp{WallMs: wall}, Ops: ops}
}

func create(id string, x float64) *pb.Op {
	return &pb.Op{Id: id, Props: &pb.ObjectProps{Type: pb.ShapeType_SHAPE_TYPE_RECT.Enum(), X: proto.Float64(x)}}
}

func move(id string, x float64) *pb.Op {
	return &pb.Op{Id: id, Props: &pb.ObjectProps{X: proto.Float64(x)}}
}

func submit(t *testing.T, b *Board, clientID uint64, ob *pb.OpBatch) {
	t.Helper()
	if err := b.Submit(context.Background(), clientID, ob); err != nil {
		t.Fatal(err)
	}
}

// Client 10 is "a" in base 36, client 11 is "b".
func TestBatchIsAckedToSenderAndBroadcastToOthers(t *testing.T) {
	e := newEnv(t)
	a, b := newConn(10), newConn(11)
	bd, w := e.join(t, "x", a)
	if w.GetSeq() != 0 || w.GetNodeId() != "n1" || w.GetProtocolVersion() != protocol.Version {
		t.Fatalf("welcome = %v", w)
	}
	e.join(t, "x", b)

	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 5)))

	// A new object reaches everyone as a full object (the creator too, since
	// it now holds it), not as an op.
	fa := a.frame(t)
	if len(fa.Batches) != 0 || len(fa.Acks) != 1 || fa.Acks[0].GetSeq() != 1 || fa.Acks[0].GetRejected() || len(fa.Objects) != 1 || fa.Seq != 1 {
		t.Fatalf("sender frame = %v", fa)
	}
	if got := fa.Acks[0].GetStamp(); got.GetClientId() != 10 || got.GetWallMs() != now.UnixMilli() {
		t.Fatalf("ack stamp = %v", got)
	}
	fb := b.frame(t)
	if len(fb.Batches) != 0 || len(fb.Objects) != 1 || fb.Objects[0].GetId() != "a:1" || len(fb.Acks) != 0 {
		t.Fatalf("receiver frame = %v", fb)
	}

	// Later edits to an object others hold travel as ops.
	submit(t, bd, 10, batch(2, now.UnixMilli()+1, move("a:1", 9)))
	if fa := a.frame(t); len(fa.Batches) != 0 || len(fa.Objects) != 0 || len(fa.Acks) != 1 {
		t.Fatalf("sender frame = %v, want just the ack", fa)
	}
	if fb := b.frame(t); len(fb.Batches) != 1 || fb.Batches[0].GetSeq() != 2 || fb.Batches[0].GetOps()[0].GetProps().GetX() != 9 {
		t.Fatalf("receiver frame = %v, want the move", fb)
	}
}

func TestBatchesInOneTickShareAFrameInSeqOrder(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Tick = 200 * time.Millisecond })
	a, b, obs := newConn(10), newConn(11), newConn(12)
	bd, _ := e.join(t, "x", a)
	e.join(t, "x", b)
	e.join(t, "x", obs)

	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	submit(t, bd, 11, batch(1, now.UnixMilli(), create("b:1", 2)))
	submit(t, bd, 10, batch(2, now.UnixMilli()+1, move("b:1", 3)))

	// One frame: both new objects in full, and the move (made after b:1
	// existed) as an op.
	f := obs.frame(t)
	if len(f.Objects) != 2 || len(f.Batches) != 1 || f.Batches[0].GetSeq() != 3 || f.Seq != 3 {
		t.Fatalf("observer frame = %v", f)
	}
	if fa := a.frame(t); len(fa.Acks) != 2 || len(fa.Batches) != 0 || len(fa.Objects) != 2 {
		t.Fatalf("a's frame = %v, want 2 acks and both objects", fa)
	}
}

func TestResentBatchIsAckedButNotReapplied(t *testing.T) {
	e := newEnv(t)
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	a.frame(t)

	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	f := a.frame(t)
	if len(f.Acks) != 1 || f.Acks[0].GetSeq() != 0 || f.Acks[0].GetRejected() {
		t.Fatalf("duplicate ack = %v, want seq 0 and not rejected", f.Acks)
	}
	snap, _ := bd.Snapshot(context.Background())
	if snap.Seq != 1 {
		t.Fatalf("board seq = %d after a duplicate, want 1", snap.Seq)
	}
}

func TestRejections(t *testing.T) {
	cases := map[string]*pb.OpBatch{
		"gap in client_seq":      batch(2, now.UnixMilli(), create("a:1", 1)),
		"edit of unknown object": batch(1, now.UnixMilli(), move("a:nope", 1)),
		"someone else's prefix":  batch(1, now.UnixMilli(), create("b:1", 1)),
	}
	for name, ob := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			a, other := newConn(10), newConn(11)
			bd, _ := e.join(t, "x", a)
			e.join(t, "x", other)

			submit(t, bd, 10, ob)
			f := a.frame(t)
			if len(f.Acks) != 1 || !f.Acks[0].GetRejected() || f.Acks[0].GetReason() == "" {
				t.Fatalf("ack = %v, want rejected with a reason", f.Acks)
			}
			other.quiet(t) // nothing broadcast
			snap, _ := bd.Snapshot(context.Background())
			if snap.Seq != 0 || len(snap.Objects) != 0 {
				t.Fatalf("rejected batch changed the board: %+v", snap)
			}

			// The client can carry on after resyncing.
			submit(t, bd, 10, batch(ob.GetClientSeq()+1, now.UnixMilli(), create("a:ok", 1)))
			if f := a.frame(t); f.Acks[0].GetRejected() {
				t.Fatalf("follow-up batch rejected: %v", f.Acks[0])
			}
		})
	}
}

func TestFutureStampIsClamped(t *testing.T) {
	e := newEnv(t)
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	future := now.Add(time.Hour).UnixMilli()
	submit(t, bd, 10, batch(1, future, create("a:1", 1)))

	ack := a.frame(t).Acks[0]
	if ack.GetStamp().GetWallMs() >= future || ack.GetStamp().GetClientId() != 10 {
		t.Fatalf("ack stamp = %v, want clamped below %d and client 10", ack.GetStamp(), future)
	}
	if testutil.ToFloat64(e.metrics.StampsClamped) != 1 {
		t.Fatal("stamps_clamped_total not incremented")
	}

	// Within the allowed skew, stamps are kept.
	submit(t, bd, 10, batch(2, now.Add(time.Second).UnixMilli(), move("a:1", 2)))
	if got := a.frame(t).Acks[0].GetStamp().GetWallMs(); got != now.Add(time.Second).UnixMilli() {
		t.Fatalf("stamp within skew changed to %d", got)
	}
}

func TestRejoinResumesFromLastClientSeq(t *testing.T) {
	e := newEnv(t)
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	submit(t, bd, 10, batch(2, now.UnixMilli()+1, move("a:1", 7)))
	a.frame(t)
	bd.Leave(a)

	again := newConn(10)
	_, w := e.join(t, "x", again)
	if w.GetLastClientSeq() != 2 || w.GetSeq() != 2 || len(w.GetObjects()) != 1 {
		t.Fatalf("welcome = %v, want last_client_seq 2, seq 2, one object", w)
	}
	if x := w.GetObjects()[0].GetProps().GetX(); x != 7 {
		t.Fatalf("x = %v, want 7", x)
	}
}

func TestNewerConnectionReplacesOlder(t *testing.T) {
	e := newEnv(t)
	old := newConn(10)
	bd, _ := e.join(t, "x", old)
	fresh := newConn(10)
	e.join(t, "x", fresh)
	if r := old.kickedFor(); r == nil || *r != KickReplaced {
		t.Fatalf("old connection kick = %v, want KickReplaced", r)
	}

	// Traffic now flows to the new connection only.
	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	if f := fresh.frame(t); len(f.Acks) != 1 {
		t.Fatalf("new connection frame = %v", f)
	}
	old.quiet(t)

	// A late Leave from the old connection must not detach the new one.
	bd.Leave(old)
	submit(t, bd, 10, batch(2, now.UnixMilli(), move("a:1", 2)))
	if f := fresh.frame(t); len(f.Acks) != 1 {
		t.Fatalf("new connection lost after old Leave: %v", f)
	}
}

func TestSlowConsumerIsKickedAndRemoved(t *testing.T) {
	e := newEnv(t)
	a, slow := newConn(10), newConn(11)
	bd, _ := e.join(t, "x", a)
	e.join(t, "x", slow)

	slow.mu.Lock()
	slow.full = true
	slow.mu.Unlock()
	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	a.frame(t)

	waitFor(t, func() bool { r := slow.kickedFor(); return r != nil && *r == KickSlow })
	// Its id is free again, so it can reconnect.
	e.join(t, "x", newConn(11))
}

func TestIdleBoardSnapshotsUnloadsAndReloads(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		c.IdleTimeout = 20 * time.Millisecond
		c.Now = time.Now
	})
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	submit(t, bd, 10, batch(1, time.Now().UnixMilli(), create("a:1", 1)))
	submit(t, bd, 10, batch(2, time.Now().UnixMilli()+1, move("a:1", 9)))
	a.frame(t)
	bd.Leave(a)
	waitFor(t, func() bool { return e.reg.Lookup("x") == nil })
	if e.store.Snapshots("x") != 1 {
		t.Fatalf("unload wrote %d snapshots, want 1", e.store.Snapshots("x"))
	}

	_, w := e.join(t, "x", newConn(10))
	if w.GetSeq() != 2 || w.GetLastClientSeq() != 2 || len(w.GetObjects()) != 1 || w.GetObjects()[0].GetProps().GetX() != 9 {
		t.Fatalf("reloaded welcome = %v", w)
	}
}

func TestCrashLosesOnlyUnackedBatches(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Tick = 100 * time.Millisecond })
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	if f := a.frame(t); f.Acks[0].GetSeq() != 1 {
		t.Fatalf("ack = %v", f.Acks)
	}

	// Batch 2 is applied in memory but the crash comes before the tick commits it.
	submit(t, bd, 10, batch(2, now.UnixMilli()+1, move("a:1", 5)))
	e.reg.CrashAll()
	if r := a.kickedFor(); r == nil || *r != KickReload {
		t.Fatalf("client kick = %v, want KickReload", r)
	}
	a.quiet(t) // batch 2 was never acked

	again := newConn(10)
	_, w := e.join(t, "x", again)
	if w.GetSeq() != 1 || w.GetLastClientSeq() != 1 || w.GetObjects()[0].GetProps().GetX() != 1 {
		t.Fatalf("after crash: welcome = %v, want only the acked batch", w)
	}
	// The client resends batch 2 and it applies normally.
	submit(t, e.reg.Lookup("x"), 10, batch(2, now.UnixMilli()+1, move("a:1", 5)))
	if f := again.frame(t); f.Acks[0].GetSeq() != 2 {
		t.Fatalf("resent batch ack = %v", f.Acks)
	}
}

func TestCommitFailureDropsClientsWithoutAcking(t *testing.T) {
	e := newEnv(t)
	a, b := newConn(10), newConn(11)
	bd, _ := e.join(t, "x", a)
	e.join(t, "x", b)
	e.store.SetFailAppend(errors.New("disk on fire"))

	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	waitFor(t, func() bool { return a.kickedFor() != nil && b.kickedFor() != nil })
	a.quiet(t)
	b.quiet(t)
	if got := testutil.ToFloat64(e.metrics.BoardFailures.WithLabelValues("commit")); got != 1 {
		t.Fatalf("board_failures_total{commit} = %v", got)
	}
	waitFor(t, func() bool { return e.reg.Lookup("x") == nil })

	// Once storage recovers, the board reloads with nothing uncommitted in it.
	e.store.SetFailAppend(nil)
	_, w := e.join(t, "x", newConn(10))
	if w.GetSeq() != 0 || len(w.GetObjects()) != 0 {
		t.Fatalf("welcome = %v, want empty board", w)
	}
}

func TestLoadFailureIsReturnedToJoiner(t *testing.T) {
	e := newEnv(t)
	e.store.FailLoad = errors.New("database down")
	_, err := e.reg.Join(context.Background(), "x", newConn(10), nil)
	if err == nil || !strings.Contains(err.Error(), "database down") {
		t.Fatalf("err = %v, want the load error", err)
	}
}

func TestLogGapFailsLoad(t *testing.T) {
	e := newEnv(t)
	e.store.AppendRaw("x",
		LogEntry{Seq: 1, ClientID: 10, ClientSeq: 1, Stamp: &pb.Stamp{WallMs: 1}, Ops: []*pb.Op{create("a:1", 1)}},
		LogEntry{Seq: 3, ClientID: 10, ClientSeq: 2, Stamp: &pb.Stamp{WallMs: 2}, Ops: []*pb.Op{move("a:1", 2)}},
	)
	if _, err := e.reg.Join(context.Background(), "x", newConn(10), nil); err == nil || !strings.Contains(err.Error(), "log gap") {
		t.Fatalf("err = %v, want log gap", err)
	}
}

func TestPeriodicSnapshots(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.SnapshotEvery, c.SnapshotMinInterval = 3, -1 })
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 0)))
	// Three rounds, each committed in its own tick: seqs 1-4, 5-7 and 8.
	// Snapshots land at 4 and 7; seq 8 stays in the log tail.
	next := uint64(2)
	for _, upTo := range []uint64{4, 7, 8} {
		for ; next <= upTo; next++ {
			submit(t, bd, 10, batch(next, now.UnixMilli()+int64(next), move("a:1", float64(next))))
		}
		for acked := 0; acked < 1; {
			f := a.frame(t)
			for _, ack := range f.Acks {
				if ack.GetSeq() == upTo {
					acked++
				}
			}
		}
	}
	waitFor(t, func() bool { return e.store.Snapshots("x") == 2 })

	// Load uses the latest snapshot plus the log after it, and matches live state.
	live, _ := bd.Snapshot(context.Background())
	e.reg.CrashAll()
	_, w := e.join(t, "x", newConn(10))
	if w.GetSeq() != live.Seq || !proto.Equal(w.GetObjects()[0], live.Objects[0]) {
		t.Fatalf("reloaded %v, live %v", w, live)
	}
}

func TestRegistryCloseCommitsAndSnapshots(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Tick = time.Hour }) // only Close flushes
	a := newConn(10)
	bd, err := e.reg.Join(context.Background(), "x", a, nil)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	if err := e.reg.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.store.LogLen("x") != 1 || e.store.Snapshots("x") != 1 {
		t.Fatalf("log %d, snapshots %d; want 1 and 1", e.store.LogLen("x"), e.store.Snapshots("x"))
	}
	if r := a.kickedFor(); r == nil || *r != KickReload {
		t.Fatalf("client kick = %v, want KickReload", r)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 3s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTimeBasedSnapshot(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		c.SnapshotInterval = 30 * time.Millisecond
		c.Now = time.Now
	})
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	time.Sleep(50 * time.Millisecond)
	if e.store.Snapshots("x") != 0 {
		t.Fatal("snapshot written for an unchanged board")
	}
	submit(t, bd, 10, batch(1, time.Now().UnixMilli(), create("a:1", 1)))
	waitFor(t, func() bool { return e.store.Snapshots("x") == 1 })
}

func TestCursorsReachOthersAndDepartureIsAnnounced(t *testing.T) {
	e := newEnv(t)
	a, b := newConn(10), newConn(11)
	bd, _ := e.join(t, "x", a)
	e.join(t, "x", b)

	bd.SetCursor(10, 1, 2)
	bd.SetCursor(10, 3, 4) // only the latest position per tick is sent
	f := b.frame(t)
	if len(f.Cursors) != 1 || f.Cursors[0].GetClientId() != 10 || f.Cursors[0].GetX() != 3 || f.Cursors[0].GetY() != 4 {
		t.Fatalf("b's frame cursors = %v", f.Cursors)
	}
	a.quiet(t) // nobody is sent their own cursor

	bd.Leave(a)
	f = b.frame(t)
	if len(f.Cursors) != 1 || !f.Cursors[0].GetGone() || f.Cursors[0].GetClientId() != 10 {
		t.Fatalf("departure = %v, want gone for client 10", f.Cursors)
	}

	// A cursor message racing with the departure must not resurrect it.
	bd.SetCursor(10, 5, 6)
	b.quiet(t)
}

func TestObjectCap(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxObjects = 3 })
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	ackOf := func(ob *pb.OpBatch) *pb.Ack {
		t.Helper()
		submit(t, bd, 10, ob)
		return a.frame(t).Acks[0]
	}
	if ack := ackOf(batch(1, now.UnixMilli(), create("a:1", 1), create("a:2", 2))); ack.GetRejected() {
		t.Fatalf("under the cap: %v", ack)
	}
	// Two more would make 4; ops on the same new id count once.
	if ack := ackOf(batch(2, now.UnixMilli(), create("a:3", 3), move("a:1", 5), create("a:4", 4))); !ack.GetRejected() || !strings.Contains(ack.GetReason(), "full") {
		t.Fatalf("over the cap: %v", ack)
	}
	if ack := ackOf(batch(3, now.UnixMilli(), create("a:3", 3), move("a:3", 4), move("a:1", 5))); ack.GetRejected() {
		t.Fatalf("exactly at the cap, edits included: %v", ack)
	}
	if ack := ackOf(batch(4, now.UnixMilli(), move("a:2", 9))); ack.GetRejected() {
		t.Fatalf("editing a full board: %v", ack)
	}
}

// Two nodes that both believe they own a board (a lease taken over while the
// old owner was paused) share one log. The second to commit a seq hits the
// fence: its board drops without acking, and the edit lands after a rejoin.
func TestSecondWriterIsFenced(t *testing.T) {
	st := NewMemoryStore()
	newNode := func() (*Registry, *metrics.Metrics) {
		m := metrics.New()
		cfg := Config{NodeID: "n", Store: st, Tick: 5 * time.Millisecond, Now: func() time.Time { return now }}
		return NewRegistry(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), m), m
	}
	ra, _ := newNode()
	rb, mb := newNode()
	a, b := newConn(10), newConn(11)
	ba, err := ra.Join(t.Context(), "x", a, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.next(t)
	bb, err := rb.Join(t.Context(), "x", b, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.next(t)

	submit(t, ba, 10, batch(1, now.UnixMilli(), create("a:1", 1)))
	if f := a.frame(t); f.Acks[0].GetSeq() != 1 {
		t.Fatalf("first writer ack = %v", f.Acks)
	}
	submit(t, bb, 11, batch(1, now.UnixMilli()+1, create("b:1", 1)))
	waitFor(t, func() bool { return b.kickedFor() != nil })
	if *b.kickedFor() != KickReload {
		t.Fatalf("kicked for %v", *b.kickedFor())
	}
	if got := testutil.ToFloat64(mb.BoardFailures.WithLabelValues("commit")); got != 1 {
		t.Fatalf("commit failures = %v", got)
	}
	for _, m := range drain(b) {
		if f := m.GetFrame(); f != nil && len(f.Acks) > 0 {
			t.Fatalf("fenced batch was acked: %v", f.Acks)
		}
	}

	// The client rejoins (its board reloads from the log) and resends.
	b2 := newConn(11)
	bb, err = rb.Join(t.Context(), "x", b2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w := b2.next(t).GetWelcome(); w.GetSeq() != 1 || len(w.GetObjects()) != 1 {
		t.Fatalf("reloaded welcome = %v", w)
	}
	submit(t, bb, 11, batch(1, now.UnixMilli()+1, create("b:1", 1)))
	if f := b2.frame(t); f.Acks[0].GetSeq() != 2 {
		t.Fatalf("resent batch ack = %v", f.Acks)
	}
}

// drain returns the messages already delivered to c.
func drain(c *fakeConn) []*pb.ServerMessage {
	var out []*pb.ServerMessage
	for {
		select {
		case m := <-c.msgs:
			out = append(out, m)
		default:
			return out
		}
	}
}

// Many clients share one view, so the shared cursor fast path does most of
// the work. However cursors move, each client must end up showing
// min(30, others) cursors, never its own, each at its latest position.
func TestSharedCursorsStayExact(t *testing.T) {
	e := newEnv(t)
	const n = 40
	view := &pb.Viewport{X: 0, Y: 0, W: 1000, H: 1000}
	conns := make([]*fakeConn, n)
	var bd *Board
	for i := range conns {
		conns[i] = newConn(uint64(100 + i))
		b, err := e.reg.Join(t.Context(), "x", conns[i], view)
		if err != nil {
			t.Fatal(err)
		}
		bd = b
	}
	// Everyone has a cursor before the moves start.
	for i := range n {
		if i < maxCursorsPerClient {
			bd.SetCursor(uint64(100+i), 500, 500)
		} else {
			bd.SetCursor(uint64(100+i), 40, 40)
		}
	}
	shown := make([]map[uint64][2]float64, n)
	for i := range shown {
		shown[i] = map[uint64][2]float64{}
	}
	collect := func() {
		for i, c := range conns {
			for _, m := range drain(c) {
				for _, cu := range m.GetFrame().GetCursors() {
					if cu.GetGone() {
						delete(shown[i], cu.GetClientId())
					} else {
						shown[i][cu.GetClientId()] = [2]float64{cu.GetX(), cu.GetY()}
					}
				}
			}
		}
	}
	latest := map[uint64][2]float64{}
	for i := range n {
		latest[uint64(100+i)] = [2]float64{500, 500}
		if i >= maxCursorsPerClient {
			latest[uint64(100+i)] = [2]float64{40, 40}
		}
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for range 60 {
		for range 1 + rng.IntN(15) {
			// Clients 100..129 stay near the center and 130..139 in a corner, so the
			// shown set stays the same while its members move: the fast path.
			k := rng.IntN(n)
			id := uint64(100 + k)
			p := [2]float64{float64(460 + rng.IntN(80)), float64(460 + rng.IntN(80))}
			if k >= maxCursorsPerClient {
				p = [2]float64{float64(20 + rng.IntN(40)), float64(20 + rng.IntN(40))}
			}
			latest[id] = p
			bd.SetCursor(id, p[0], p[1])
		}
		time.Sleep(time.Duration(rng.IntN(20)) * time.Millisecond)
		collect()
	}
	time.Sleep(100 * time.Millisecond) // several presence ticks with no moves
	collect()
	for i := range conns {
		self := uint64(100 + i)
		others := len(latest)
		if _, ok := latest[self]; ok {
			others--
		}
		if want := min(maxCursorsPerClient, others); len(shown[i]) != want {
			t.Fatalf("client %d shows %d cursors, want %d", self, len(shown[i]), want)
		}
		for id, p := range shown[i] {
			if id == self {
				t.Fatalf("client %d is shown its own cursor", self)
			}
			if p != latest[id] {
				t.Fatalf("client %d shows %d at %v, latest is %v", self, id, p, latest[id])
			}
		}
	}
}

// On a busy board, batches alone do not snapshot more often than
// SnapshotMinInterval (the clock here never moves).
func TestSnapshotsWaitForMinInterval(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.SnapshotEvery = 3 })
	a := newConn(10)
	bd, _ := e.join(t, "x", a)
	submit(t, bd, 10, batch(1, now.UnixMilli(), create("a:1", 0)))
	for cs := uint64(2); cs <= 12; cs++ {
		submit(t, bd, 10, batch(cs, now.UnixMilli()+int64(cs), move("a:1", float64(cs))))
	}
	for seen := false; !seen; {
		for _, ack := range a.frame(t).Acks {
			seen = seen || ack.GetSeq() == 12
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := e.store.Snapshots("x"); n != 0 {
		t.Fatalf("%d snapshots within the minimum interval", n)
	}
}
