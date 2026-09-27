package client_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http/httptest"
	"os"
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

func (s *server) client(t *testing.T, id uint64) *client.Client {
	t.Helper()
	c := client.New(client.Config{URL: s.url, BoardID: "b", ClientID: id, Reconnect: true})
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
	for ; ; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			for _, c := range cs {
				t.Logf("client %d: %+v", c.ID(), c.Stats())
			}
			t.Fatal("clients did not settle")
		}
		b := s.boards.Lookup("b")
		if b == nil {
			continue // reloading after a crash
		}
		snap, err := b.Snapshot(context.Background())
		if err != nil {
			continue
		}
		done := true
		for _, c := range cs {
			st := c.Stats()
			if !st.Connected || st.Pending > 0 || st.ServerSeq != snap.Seq {
				done = false
			}
		}
		if done {
			want, err := doc.FromSnapshot(snap.Objects)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range cs {
				got, err := doc.FromSnapshot(c.Snapshot())
				if err != nil {
					t.Fatal(err)
				}
				if !doc.Equal(got, want) {
					t.Fatalf("client %d diverged from server at seq %d:\n client %v\n server %v", c.ID(), snap.Seq, c.Snapshot(), snap.Objects)
				}
			}
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
		runRandomized(t, seed, startServer(t), false)
	})
}

// The same, while the board also crashes repeatedly (losing whatever it had
// not committed) and reloads from the store. Nothing acked may be lost.
func TestRandomizedConvergenceWithCrashes(t *testing.T) {
	forSeeds(t, 8, func(t *testing.T, seed uint64) {
		runRandomized(t, seed, startServer(t), true)
	})
}

// With crashes, against real Postgres (skipped without Docker).
func TestRandomizedConvergenceWithCrashesPostgres(t *testing.T) {
	pool := dbtest.NewPool(t)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	forSeeds(t, 2, func(t *testing.T, seed uint64) {
		_, _ = pool.Exec(context.Background(), "DELETE FROM ops; DELETE FROM snapshots; DELETE FROM boards")
		st, err := store.NewPostgres(pool)
		if err != nil {
			t.Fatal(err)
		}
		s := startServerWith(t, st)
		s.logged = func(t *testing.T) map[[2]uint64]bool {
			rows, err := pool.Query(context.Background(), "SELECT client_id, client_seq FROM ops WHERE board_id = $1", "b")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			out := map[[2]uint64]bool{}
			for rows.Next() {
				var c, cs int64
				if err := rows.Scan(&c, &cs); err != nil {
					t.Fatal(err)
				}
				out[[2]uint64{uint64(c), uint64(cs)}] = true
			}
			return out
		}
		runRandomized(t, seed, s, true)
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

func runRandomized(t *testing.T, seed uint64, s *server, crashes bool) {
	const nClients, actionsPerClient = 5, 60

	clients := make([]*client.Client, nClients)
	for i := range clients {
		clients[i] = s.client(t, uint64(100+i))
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
