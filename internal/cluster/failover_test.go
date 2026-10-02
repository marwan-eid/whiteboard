package cluster_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/board"
	"whiteboard/internal/client"
	"whiteboard/internal/cluster"
	"whiteboard/internal/db"
	"whiteboard/internal/db/dbtest"
	"whiteboard/internal/doc"
	"whiteboard/internal/gateway"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/store"
)

// testNode is one node: membership, board registry and gateway on its own
// HTTP server, with timings shortened from ADR-0005's (5 s leases).
type testNode struct {
	id      string
	cluster *cluster.Node
	reg     *board.Registry
	metrics *metrics.Metrics
	srv     *httptest.Server
	// stopLeases stops heartbeats and lease renewal, as if the node were
	// cut off from Postgres for those (or paused).
	stopLeases context.CancelFunc
	dead       atomic.Bool
}

func (n *testNode) wsURL() string { return "ws" + strings.TrimPrefix(n.srv.URL, "http") + "/ws" }

// kill simulates SIGKILL: no commit, no lease release, no leaving.
func (n *testNode) kill() {
	n.dead.Store(true)
	n.stopLeases()
	n.reg.CrashAll()
	n.srv.CloseClientConnections()
	n.srv.Close()
}

type cluster2 struct {
	nodes map[string]*testNode
	order []*testNode
	front *httptest.Server // round-robins /ws over live nodes, like Caddy
	st    *store.Postgres
}

func startCluster(t *testing.T, ids ...string) *cluster2 {
	t.Helper()
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewPostgres(pool)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &cluster2{nodes: map[string]*testNode{}, st: st}
	for _, id := range ids {
		n := &testNode{id: id, metrics: metrics.New()}
		n.cluster = cluster.New(cluster.Config{
			NodeID: id, Heartbeat: 100 * time.Millisecond, LiveFor: time.Second, LeaseTTL: time.Second,
		}, pool, log)
		if err := n.cluster.Join(ctx); err != nil {
			t.Fatal(err)
		}
		n.reg = board.NewRegistry(board.Config{NodeID: id, Store: st, Placement: n.cluster}, log, n.metrics)
		leaseCtx, stop := context.WithCancel(ctx)
		n.stopLeases = stop
		go n.cluster.Run(leaseCtx)
		go n.reg.Run(leaseCtx)
		gw := gateway.New(gateway.Config{}, n.reg, log, n.metrics)
		mux := http.NewServeMux()
		mux.Handle("GET /ws", gw)
		mux.Handle("GET /n/{node}/ws", gw)
		n.srv = httptest.NewServer(mux)
		t.Cleanup(func() {
			if !n.dead.Load() {
				gw.Shutdown()
				n.srv.CloseClientConnections()
				n.srv.Close()
				_ = n.reg.Close(context.Background())
				stop()
			}
		})
		c.nodes[id] = n
		c.order = append(c.order, n)
	}
	for _, n := range c.order { // everyone sees everyone
		if err := n.cluster.Join(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var next atomic.Uint64
	c.front = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range c.order {
			n := c.order[next.Add(1)%uint64(len(c.order))]
			if n.dead.Load() {
				continue
			}
			u, _ := url.Parse(n.srv.URL)
			httputil.NewSingleHostReverseProxy(u).ServeHTTP(w, r)
			return
		}
		http.Error(w, "no live node", http.StatusBadGateway)
	}))
	t.Cleanup(c.front.Close)
	return c
}

func (c *cluster2) client(t *testing.T, boardID string) *client.Client {
	t.Helper()
	cl := client.New(client.Config{
		URL: "ws" + strings.TrimPrefix(c.front.URL, "http") + "/ws", BoardID: boardID, Reconnect: true,
		NodeURL: func(node string) string { return c.nodes[node].wsURL() },
	})
	t.Cleanup(cl.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cl.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	return cl
}

func (c *cluster2) owner(t *testing.T, boardID string) *testNode {
	t.Helper()
	for _, n := range c.order {
		if !n.dead.Load() {
			id, err := n.cluster.Route(context.Background(), boardID)
			if err != nil {
				t.Fatal(err)
			}
			return c.nodes[id]
		}
	}
	t.Fatal("no live node")
	return nil
}

// edit makes random creates and moves on every client until stop closes.
func edit(clients []*client.Client, stop <-chan struct{}) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(i), 7))
			var mine []string
			for {
				select {
				case <-stop:
					return
				case <-time.After(15 * time.Millisecond):
				}
				if len(mine) == 0 || rng.IntN(4) == 0 {
					id := c.NewObjectID()
					mine = append(mine, id)
					c.Edit(&pb.Op{Id: id, Props: &pb.ObjectProps{
						Type: pb.ShapeType_SHAPE_TYPE_RECT.Enum(), X: proto.Float64(0), Y: proto.Float64(0), W: proto.Float64(50), H: proto.Float64(50),
					}})
				} else {
					c.Edit(&pb.Op{Id: mine[rng.IntN(len(mine))], Props: &pb.ObjectProps{X: proto.Float64(float64(rng.IntN(1000)))}})
				}
			}
		})
	}
	return &wg
}

// settle waits until every client is connected with nothing pending at the
// same seq, then checks they all match a fresh client and that every acked
// batch is in the durable log.
func (c *cluster2) settle(t *testing.T, boardID string, clients []*client.Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		seqs := map[uint64]bool{}
		ready := true
		for _, cl := range clients {
			s := cl.Stats()
			ready = ready && s.Connected && s.Pending == 0
			seqs[s.ServerSeq] = true
		}
		if ready && len(seqs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			for _, cl := range clients {
				t.Logf("client %d: %+v", cl.ID(), cl.Stats())
			}
			t.Fatal("clients did not settle")
		}
		time.Sleep(50 * time.Millisecond)
	}
	want, err := doc.FromSnapshot(c.client(t, boardID).Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, cl := range clients {
		got, err := doc.FromSnapshot(cl.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if !doc.Equal(got, want) {
			t.Fatalf("client %d diverged", cl.ID())
		}
	}
	entries, err := c.st.Range(context.Background(), boardID, 0, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	logged := map[string]bool{}
	for _, e := range entries {
		logged[fmt.Sprintf("%d:%d", e.ClientID, e.ClientSeq)] = true
	}
	total := 0
	for _, cl := range clients {
		for _, cs := range cl.Acked() {
			total++
			if !logged[fmt.Sprintf("%d:%d", cl.ID(), cs)] {
				t.Fatalf("client %d batch %d was acked but is not in the log", cl.ID(), cs)
			}
		}
	}
	t.Logf("%d acked batches, all in the log; %d objects", total, want.Len())
}

// The owner dies mid-edit. Clients move to the other node once its lease
// runs out, and no acknowledged edit is lost.
func TestKilledOwnerFailsOver(t *testing.T) {
	c := startCluster(t, "node-a", "node-b")
	boardID := fmt.Sprintf("failover-%d", time.Now().UnixNano())
	clients := make([]*client.Client, 4)
	for i := range clients {
		clients[i] = c.client(t, boardID)
	}
	owner := c.owner(t, boardID)
	stop := make(chan struct{})
	wg := edit(clients, stop)

	time.Sleep(500 * time.Millisecond)
	before := make([]int, len(clients))
	for i, cl := range clients {
		before[i] = cl.Stats().Connects
	}
	killedAt := time.Now()
	owner.kill()
	// Wait until every client has reconnected, to the survivor.
	for {
		back := true
		for i, cl := range clients {
			s := cl.Stats()
			back = back && s.Connected && s.Connects > before[i]
		}
		if back {
			break
		}
		if time.Since(killedAt) > 15*time.Second {
			t.Fatal("clients did not reconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("all clients back %v after the kill (lease TTL 1 s)", time.Since(killedAt).Round(10*time.Millisecond))
	time.Sleep(500 * time.Millisecond)
	close(stop)
	wg.Wait()

	if now := c.owner(t, boardID); now == owner {
		t.Fatalf("board still routed to the dead node %s", owner.id)
	}
	c.settle(t, boardID, clients)
}

// The owner stops heartbeating and renewing (paused, or cut off from the
// lease table) but keeps its sockets. It must stop serving when its lease
// runs out, rather than keep committing as a stale owner; its clients move
// to the other node, and no acknowledged edit is lost.
func TestPausedOwnerStepsDown(t *testing.T) {
	c := startCluster(t, "node-a", "node-b")
	boardID := fmt.Sprintf("fence-%d", time.Now().UnixNano())
	clients := make([]*client.Client, 3)
	for i := range clients {
		clients[i] = c.client(t, boardID)
	}
	owner := c.owner(t, boardID)
	var other *testNode
	for _, n := range c.order {
		if n != owner {
			other = n
		}
	}
	stop := make(chan struct{})
	wg := edit(clients, stop)

	time.Sleep(300 * time.Millisecond)
	owner.stopLeases()                  // still serving its clients, but no longer heartbeating or renewing
	time.Sleep(1500 * time.Millisecond) // lease and liveness (1 s each) run out

	// A client reaching the other node now gets the board there.
	late := client.New(client.Config{URL: other.wsURL(), BoardID: boardID, Reconnect: true,
		NodeURL: func(node string) string { return c.nodes[node].wsURL() }})
	t.Cleanup(late.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := late.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	all := append(slices.Clip(clients), late)
	wg2 := edit([]*client.Client{late}, stop)
	time.Sleep(time.Second)
	close(stop)
	wg.Wait()
	wg2.Wait()

	if n := testutil.ToFloat64(owner.metrics.BoardFailures.WithLabelValues("lease")); n == 0 {
		t.Fatal("the paused owner never stepped down")
	}
	t.Logf("commits that hit the (board_id, seq) fence: %v",
		testutil.ToFloat64(owner.metrics.BoardFailures.WithLabelValues("commit"))+testutil.ToFloat64(other.metrics.BoardFailures.WithLabelValues("commit")))
	c.settle(t, boardID, all)
	if now := c.owner(t, boardID); now != other {
		t.Fatalf("board routed to %s, want %s", now.id, other.id)
	}
}
