package client_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/board"
	"whiteboard/internal/client"
	"whiteboard/internal/db"
	"whiteboard/internal/db/dbtest"
	"whiteboard/internal/doc"
	"whiteboard/internal/gateway"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/store"
)

type server struct {
	url     string
	boards  *board.Registry
	metrics *metrics.Metrics
	// logged returns the (client id, client seq) pairs in board "b"'s durable log.
	logged func(t *testing.T) map[[2]uint64]bool
}

func startServer(t *testing.T) *server {
	t.Helper()
	st := board.NewMemoryStore()
	s := startServerWith(t, st)
	s.logged = func(*testing.T) map[[2]uint64]bool {
		out := map[[2]uint64]bool{}
		for _, e := range st.Entries("b") {
			out[[2]uint64{e.ClientID, e.ClientSeq}] = true
		}
		return out
	}
	return s
}

func startServerWith(t *testing.T, st board.Store) *server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	boards := board.NewRegistry(board.Config{NodeID: "n1", Store: st, Tick: 5 * time.Millisecond, SnapshotEvery: 50}, log, m)
	srv := httptest.NewServer(gateway.New(gateway.Config{}, boards, log, m))
	t.Cleanup(srv.Close)
	return &server{url: "ws" + strings.TrimPrefix(srv.URL, "http"), boards: boards, metrics: m}
}

var traces sync.Map // client id -> *traceLog

type traceLog struct {
	mu    sync.Mutex
	lines []string
}

func (s *server) client(t *testing.T, id uint64) *client.Client {
	t.Helper()
	tl := &traceLog{}
	traces.Store(id, tl)
	c := client.New(client.Config{URL: s.url, BoardID: "b", ClientID: id, Reconnect: true, Trace: func(line string) {
		tl.mu.Lock()
		defer tl.mu.Unlock()
		tl.lines = append(tl.lines, time.Now().Format("15:04:05.000 ")+line)
	}})
	t.Cleanup(c.Close)
	return c
}

func connect(t *testing.T, cs ...*client.Client) {
	t.Helper()
	for _, c := range cs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := c.Connect(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
	}
}

// settle waits until every client is connected, has nothing pending, and
// has seen the board's latest seq; then all replicas must equal the server's.
func (s *server) settle(t *testing.T, cs ...*client.Client) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for ; ; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			for _, c := range cs {
				t.Logf("client %d: %+v", c.ID(), c.Stats())
			}
			dumpTraces(t, lastErr)
			t.Fatalf("clients did not settle: %v", lastErr)
		}
		b := s.boards.Lookup("b")
		if b == nil {
			continue // reloading after a crash
		}
		snap, err := b.Snapshot(context.Background())
		if err != nil {
			continue
		}
		lastErr = nil
		for _, c := range cs {
			if st := c.Stats(); !st.Connected || st.Pending > 0 {
				lastErr = fmt.Errorf("client %d not idle: %+v", c.ID(), st)
				break
			}
			// A client holds exactly the visible objects in its viewport.
			if err := matches(c, snap.Objects); err != nil {
				lastErr = err
				break
			}
		}
		if lastErr == nil {
			return
		}
	}
}

// assertDurable checks that every batch the server acked is in the durable log.
func (s *server) assertDurable(t *testing.T, cs ...*client.Client) {
	t.Helper()
	logged := s.logged(t)
	for _, c := range cs {
		for _, cs := range c.Acked() {
			if !logged[[2]uint64{c.ID(), cs}] {
				t.Fatalf("client %d batch %d was acked but is not in the log", c.ID(), cs)
			}
		}
	}
}

func rect(id string, x float64) *pb.Op {
	return &pb.Op{Id: id, Props: &pb.ObjectProps{Type: pb.ShapeType_SHAPE_TYPE_RECT.Enum(), X: proto.Float64(x), Y: proto.Float64(0)}}
}

func set(id string, p *pb.ObjectProps) *pb.Op { return &pb.Op{Id: id, Props: p} }

func TestEditsReachOtherClients(t *testing.T) {
	s := startServer(t)
	a, b := s.client(t, 10), s.client(t, 11)
	connect(t, a, b)

	id := a.NewObjectID()
	a.Edit(rect(id, 1))
	if a.Object(id) == nil {
		t.Fatal("edit not applied locally at once")
	}
	s.settle(t, a, b)
	if got := b.Object(id).GetX(); got != 1 {
		t.Fatalf("b sees x=%v", got)
	}
}

func TestOfflineEditsMergePerProperty(t *testing.T) {
	s := startServer(t)
	a, b := s.client(t, 10), s.client(t, 11)
	connect(t, a, b)
	id := a.NewObjectID()
	a.Edit(rect(id, 0))
	s.settle(t, a, b)

	// a goes offline and moves the shape; b recolors it meanwhile, then later
	// also moves it. b's move is newer, so it must win x; a's y and b's fill survive.
	a.Offline()
	a.Edit(set(id, &pb.ObjectProps{X: proto.Float64(100), Y: proto.Float64(50)}))
	b.Edit(set(id, &pb.ObjectProps{Fill: proto.Uint32(0xff0000ff)}))
	time.Sleep(5 * time.Millisecond) // make b's move strictly newer by wall clock
	b.Edit(set(id, &pb.ObjectProps{X: proto.Float64(7)}))
	connect(t, a)
	s.settle(t, a, b)

	got := a.Object(id)
	if got.GetX() != 7 || got.GetY() != 50 || got.GetFill() != 0xff0000ff {
		t.Fatalf("merged object = %v, want x=7 (b, newer) y=50 (a) fill=red (b)", got)
	}
}

func TestDroppedConnectionResendsWithoutDuplicates(t *testing.T) {
	s := startServer(t)
	a, b := s.client(t, 10), s.client(t, 11)
	connect(t, a, b)
	for i := range 20 {
		a.Edit(rect(a.NewObjectID(), float64(i)))
		if i%5 == 0 {
			a.Drop() // some batches are in flight, some already applied
		}
	}
	s.settle(t, a, b)
	snap, _ := s.boards.Lookup("b").Snapshot(context.Background())
	if snap.Seq != 20 || len(snap.Objects) != 20 {
		t.Fatalf("server has seq %d and %d objects, want exactly 20 of each (no duplicates, nothing lost)", snap.Seq, len(snap.Objects))
	}
	if a.Stats().Connects < 2 {
		t.Fatal("expected at least one reconnect")
	}
}

func TestLateJoinerGetsCurrentState(t *testing.T) {
	s := startServer(t)
	a := s.client(t, 10)
	connect(t, a)
	for i := range 50 {
		a.Edit(rect(a.NewObjectID(), float64(i)))
	}
	late := s.client(t, 11)
	connect(t, late)
	s.settle(t, a, late)
}

// Many clients edit the same few objects at random while their connections
// drop and they go offline; at the end every replica equals the server.
func TestRandomizedConvergence(t *testing.T) {
	forSeeds(t, 8, func(t *testing.T, seed uint64) {
		runRandomized(t, seed, startServer(t), false, false)
	})
}

// The same, while the board also crashes repeatedly (losing whatever it had
// not committed) and reloads from the store. Nothing acked may be lost.
func TestRandomizedConvergenceWithCrashes(t *testing.T) {
	forSeeds(t, 8, func(t *testing.T, seed uint64) {
		runRandomized(t, seed, startServer(t), true, false)
	})
}

// With crashes, against real Postgres (skipped without Docker).
func TestRandomizedConvergenceWithCrashesPostgres(t *testing.T) {
	pool := dbtest.NewPool(t)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	forSeeds(t, 2, func(t *testing.T, seed uint64) {
		if _, err := pool.Exec(context.Background(), "DELETE FROM ops; DELETE FROM op_segments; DELETE FROM snapshots; DELETE FROM boards"); err != nil {
			t.Fatal(err)
		}
		st, err := store.NewPostgres(pool)
		if err != nil {
			t.Fatal(err)
		}
		s := startServerWith(t, st)
		// The whole log, compacted or not.
		s.logged = func(t *testing.T) map[[2]uint64]bool {
			entries, err := st.Range(context.Background(), "b", 0, math.MaxInt64)
			if err != nil {
				t.Fatal(err)
			}
			out := map[[2]uint64]bool{}
			for _, e := range entries {
				out[[2]uint64{e.ClientID, e.ClientSeq}] = true
			}
			return out
		}
		runRandomized(t, seed, s, true, false)
	})
}

func forSeeds(t *testing.T, n int, fn func(t *testing.T, seed uint64)) {
	if testing.Short() {
		n = min(n, 2)
	}
	base := uint64(time.Now().UnixNano())
	if s := os.Getenv("CONVERGENCE_SEED"); s != "" {
		base, _ = strconv.ParseUint(s, 10, 64)
		n = 1
	}
	for i := range n {
		seed := base + uint64(i)
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Logf("reproduce with CONVERGENCE_SEED=%d", seed)
			fn(t, seed)
		})
	}
}

func runRandomized(t *testing.T, seed uint64, s *server, crashes, viewports bool) {
	const nClients, actionsPerClient = 5, 60

	clients := make([]*client.Client, nClients)
	for i := range clients {
		clients[i] = s.client(t, uint64(100+i))
		if viewports {
			clients[i].SetViewport(randomViewport(rand.New(rand.NewPCG(seed, uint64(1000+i)))))
		}
	}
	connect(t, clients...)

	// Seed shared objects so clients contend on them.
	var shared []string
	for i := range 4 {
		c := clients[i%nClients]
		id := c.NewObjectID()
		c.Edit(rect(id, 0))
		shared = append(shared, id)
	}
	s.settle(t, clients...)

	stopCrashes := make(chan struct{})
	crashesDone := make(chan struct{})
	go func() {
		defer close(crashesDone)
		if !crashes {
			return
		}
		rng := rand.New(rand.NewPCG(seed, 999))
		for {
			select {
			case <-stopCrashes:
				return
			case <-time.After(time.Duration(5+rng.IntN(20)) * time.Millisecond):
				s.boards.CrashAll()
			}
		}
	}()

	var wg sync.WaitGroup
	for ci, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, uint64(ci)))
			own := []string{}
			for range actionsPerClient {
				switch r := rng.IntN(100); {
				case viewports && r >= 88:
					c.SetViewport(randomViewport(rng))
				case r < 3:
					c.Drop()
				case r < 6:
					c.Offline()
				case r < 12:
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_ = c.Connect(ctx)
					cancel()
				case r < 22:
					id := c.NewObjectID()
					own = append(own, id)
					c.Edit(rect(id, float64(rng.IntN(100))))
				default:
					pool := append(append([]string{}, shared...), own...)
					targets := 1 + rng.IntN(2)
					var ops []*pb.Op
					for range targets {
						ops = append(ops, set(pool[rng.IntN(len(pool))], randomProps(rng)))
					}
					c.Edit(ops...)
				}
				time.Sleep(time.Duration(rng.IntN(3)) * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	close(stopCrashes)
	<-crashesDone
	connect(t, clients...)
	s.settle(t, clients...)
	s.assertDurable(t, clients...)
	if crashed := testutil.ToFloat64(s.metrics.BoardFailures.WithLabelValues("crash")); crashes && crashed == 0 {
		t.Fatal("no crash happened during the run; the test proved nothing")
	} else {
		t.Logf("board crashed %v times", crashed)
	}
}

func randomProps(rng *rand.Rand) *pb.ObjectProps {
	p := &pb.ObjectProps{}
	switch rng.IntN(5) {
	case 0:
		p.X, p.Y = proto.Float64(float64(rng.IntN(500))), proto.Float64(float64(rng.IntN(500)))
	case 1:
		p.Fill = proto.Uint32(rng.Uint32())
	case 2:
		p.Deleted = proto.Bool(rng.IntN(2) == 0)
	case 3:
		p.Text = proto.String(strconv.Itoa(rng.IntN(1000)))
	default:
		p.W, p.H = proto.Float64(float64(1+rng.IntN(300))), proto.Float64(float64(1+rng.IntN(300)))
	}
	return p
}

func TestCursorsArePropagated(t *testing.T) {
	s := startServer(t)
	a, b := s.client(t, 10), s.client(t, 11)
	connect(t, a, b)
	a.MoveCursor(12, 34)
	deadline := time.Now().Add(5 * time.Second)
	for b.Cursors()[10] != [2]float64{12, 34} {
		if time.Now().After(deadline) {
			t.Fatalf("b sees cursors %v", b.Cursors())
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.Close()
	for _, ok := b.Cursors()[10]; ok; _, ok = b.Cursors()[10] {
		if time.Now().After(deadline) {
			t.Fatal("departed cursor never removed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// matches reports whether c holds exactly the server's visible objects in its viewport.
func matches(c *client.Client, server []*pb.ObjectState) error {
	full, err := doc.FromSnapshot(server)
	if err != nil {
		return err
	}
	want := doc.New()
	for _, s := range server {
		if client.InView(full.Get(s.GetId()), c.Viewport()) {
			want.MergeState(s)
		}
	}
	got, err := doc.FromSnapshot(c.Snapshot())
	if err != nil {
		return err
	}
	if doc.Equal(got, want) {
		return nil
	}
	var diff []string
	for _, s := range want.Snapshot() {
		if g := got.Get(s.GetId()); g == nil {
			diff = append(diff, fmt.Sprintf("missing %s %v", s.GetId(), s.GetProps()))
		} else if !proto.Equal(g.State(), s) {
			diff = append(diff, fmt.Sprintf("differs %s: have %v want %v", s.GetId(), g.State(), s))
		}
	}
	for _, s := range got.Snapshot() {
		if want.Get(s.GetId()) == nil {
			diff = append(diff, fmt.Sprintf("extra %s %v", s.GetId(), s.GetProps()))
		}
	}
	return fmt.Errorf("client %d (viewport %v) diverged:\n  %s", c.ID(), c.Viewport(), strings.Join(diff, "\n  "))
}

// randomViewport covers part of the 0..500 area objects move in; sometimes
// the whole board.
func randomViewport(rng *rand.Rand) *pb.Viewport {
	if rng.IntN(5) == 0 {
		return nil
	}
	return &pb.Viewport{X: float64(rng.IntN(400)) - 50, Y: float64(rng.IntN(400)) - 50, W: float64(50 + rng.IntN(250)), H: float64(50 + rng.IntN(250))}
}

// Clients watch different, changing parts of the board while edits move
// objects across their edges; each must end up holding exactly the server's
// objects in its viewport.
func TestRandomizedConvergenceWithViewports(t *testing.T) {
	forSeeds(t, 10, func(t *testing.T, seed uint64) {
		runRandomized(t, seed, startServer(t), false, true)
	})
}

func TestRandomizedConvergenceWithViewportsAndCrashes(t *testing.T) {
	forSeeds(t, 6, func(t *testing.T, seed uint64) {
		runRandomized(t, seed, startServer(t), true, true)
	})
}

// dumpTraces logs each client's sync events that mention an object named in err.
func dumpTraces(t *testing.T, err error) {
	if err == nil {
		return
	}
	ids := regexp.MustCompile(`(?:missing|differs|extra) ([0-9a-z]+:[0-9a-z]+)`).FindAllStringSubmatch(err.Error(), -1)
	for _, m := range ids {
		id := m[1]
		traces.Range(func(k, v any) bool {
			tl := v.(*traceLog)
			tl.mu.Lock()
			defer tl.mu.Unlock()
			for _, line := range tl.lines {
				if strings.Contains(line, `"`+id+`"`) || strings.HasPrefix(line[13:], "welcome") {
					t.Logf("client %d: %.600s", k, line)
				}
			}
			return true
		})
	}
}

func TestHistoryAndRestore(t *testing.T) {
	s := startServer(t)
	a, b := s.client(t, 10), s.client(t, 11) // base 36: "a", "b"
	connect(t, a, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a.Edit(rect("a:1", 0))                                         // seq 1
	a.Edit(rect("a:2", 50))                                        // seq 2
	a.Edit(set("a:1", &pb.ObjectProps{X: proto.Float64(99)}))      // seq 3
	a.Edit(set("a:2", &pb.ObjectProps{Deleted: proto.Bool(true)})) // seq 4
	s.settle(t, a, b)

	h, err := b.HistoryAt(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.GetObjects()) != 2 || h.GetObjects()[0].GetProps().GetX() != 0 || h.GetWallMs() == 0 {
		t.Fatalf("history at 2 = %v", h)
	}
	if h, _ := b.HistoryAt(ctx, 0); len(h.GetObjects()) != 0 {
		t.Fatalf("history at 0 = %v, want empty", h)
	}

	// Restore to seq 2: a:1 moves back, a:2 comes back. Everyone sees it,
	// including the client that asked.
	if err := b.Restore(ctx, 2); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p1, p2 := a.Object("a:1"), b.Object("a:2")
		if p1.GetX() == 0 && p2 != nil && !p2.GetDeleted() && b.Object("a:1").GetX() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restore not seen: a:1=%v a:2=%v", p1, p2)
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.settle(t, a, b)

	// The restore is itself history: going back to seq 4 undoes it.
	if err := a.Restore(ctx, 4); err != nil {
		t.Fatal(err)
	}
	for a.Object("a:1").GetX() != 99 || b.Object("a:2") != nil {
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatalf("undoing the restore not seen: a:1=%v a:2=%v", a.Object("a:1"), b.Object("a:2"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// History reads across snapshots, compacted segments and keyframes (Postgres).
func TestHistoryAcrossCompactionPostgres(t *testing.T) {
	pool := dbtest.NewPool(t)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewPostgres(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := startServerWith(t, st) // snapshots every 50 batches, then compaction
	a := s.client(t, 10)
	connect(t, a)
	a.Edit(rect("a:1", 0)) // seq 1
	const n = 1500
	for i := 1; i <= n; i++ {
		a.Edit(set("a:1", &pb.ObjectProps{X: proto.Float64(float64(i))})) // seq i+1
		if i%100 == 0 {
			s.settle(t, a) // spread batches over ticks so snapshots happen along the way
		}
	}
	s.settle(t, a)
	var segments int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM op_segments").Scan(&segments); err != nil || segments == 0 {
		t.Fatalf("no compaction happened (segments=%d, err=%v)", segments, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, seq := range []uint64{1, 2, 49, 50, 51, 777, 1201, 1202, 1300, n + 1, 1203} {
		h, err := a.HistoryAt(ctx, seq)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := h.GetObjects()[0].GetProps().GetX(), float64(seq-1); got != want {
			t.Fatalf("x at seq %d = %v, want %v", seq, got, want)
		}
	}
}
