//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"whiteboard/internal/client"
	"whiteboard/internal/doc"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

func wsURL() string {
	if u := os.Getenv("E2E_WS_URL"); u != "" {
		return u
	}
	return "ws://localhost:8080/ws"
}

func compose(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", append([]string{"compose"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// SIGKILL the node serving the board while clients edit, and leave it dead.
// Clients must move to the other node once the lease runs out, converge, and
// every edit the server acked must be in Postgres.
func TestNodeKillLosesNoAckedEdits(t *testing.T) {
	board := fmt.Sprintf("e2e-kill-%d", time.Now().UnixNano())
	const nClients = 5

	clients := make([]*client.Client, nClients)
	for i := range clients {
		clients[i] = client.New(client.Config{URL: wsURL(), BoardID: board, Reconnect: true})
		t.Cleanup(clients[i].Close)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := clients[i].Connect(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
	}

	stop := make(chan struct{})
	var maxPendingDuringOutage atomic.Int64
	var killed atomic.Bool
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(i), 1))
			var mine []string
			for {
				select {
				case <-stop:
					return
				case <-time.After(20 * time.Millisecond):
				}
				if len(mine) == 0 || rng.IntN(5) == 0 {
					id := c.NewObjectID()
					mine = append(mine, id)
					c.Edit(&pb.Op{Id: id, Props: &pb.ObjectProps{
						Type: pb.ShapeType_SHAPE_TYPE_RECT.Enum(), X: proto.Float64(0), Y: proto.Float64(0),
						W: proto.Float64(100), H: proto.Float64(60),
					}})
				} else {
					c.Edit(&pb.Op{Id: mine[rng.IntN(len(mine))], Props: &pb.ObjectProps{
						X: proto.Float64(float64(rng.IntN(1000))), Y: proto.Float64(float64(rng.IntN(1000))),
					}})
				}
				if killed.Load() {
					if p := int64(c.Stats().Pending); p > maxPendingDuringOutage.Load() {
						maxPendingDuringOutage.Store(p)
					}
				}
			}
		}()
	}

	time.Sleep(2 * time.Second)
	owner := route(t, board)
	before := make([]int, nClients)
	for i, c := range clients {
		before[i] = c.Stats().Connects
	}
	compose(t, "kill", "-s", "SIGKILL", owner)
	killedAt := time.Now()
	killed.Store(true)
	// Bring it back for whatever runs next, once this test is done with it.
	t.Cleanup(func() { compose(t, "up", "--detach", "--wait", "--wait-timeout", "60", owner) })
	for {
		back := true
		for i, c := range clients {
			s := c.Stats()
			back = back && s.Connected && s.Connects > before[i]
		}
		if back {
			break
		}
		if time.Since(killedAt) > 60*time.Second {
			t.Fatal("clients did not fail over")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("killed %s; all clients reconnected to another node after %v (lease TTL 5 s)", owner, time.Since(killedAt).Round(100*time.Millisecond))
	if now := route(t, board); now == owner {
		t.Fatalf("board still routed to the dead node %s", owner)
	}
	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()

	if maxPendingDuringOutage.Load() == 0 {
		t.Fatal("no edits were pending during the outage; the test proved nothing")
	}
	t.Logf("max pending edits on one client during the outage: %d", maxPendingDuringOutage.Load())

	// Settle: everyone connected, nothing pending, same seq.
	deadline := time.Now().Add(60 * time.Second)
	for {
		seqs := map[uint64]bool{}
		ready := true
		for _, c := range clients {
			st := c.Stats()
			ready = ready && st.Connected && st.Pending == 0
			seqs[st.ServerSeq] = true
		}
		if ready && len(seqs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			for _, c := range clients {
				t.Logf("client %d: %+v", c.ID(), c.Stats())
			}
			t.Fatal("clients did not settle after the failover")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A fresh client loads the board from the new owner; everyone must match it.
	fresh := client.New(client.Config{URL: wsURL(), BoardID: board})
	t.Cleanup(fresh.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fresh.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	want, err := doc.FromSnapshot(fresh.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		got, err := doc.FromSnapshot(c.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if !doc.Equal(got, want) {
			t.Fatalf("client %d diverged from the reloaded board", c.ID())
		}
	}

	// Every acked batch is in the durable log.
	out := compose(t, "exec", "-T", "postgres", "psql", "-U", "whiteboard", "-d", "whiteboard", "-At", "-c",
		fmt.Sprintf("SELECT client_id || ':' || client_seq FROM ops WHERE board_id = '%s'", board))
	logged := map[string]bool{}
	for _, line := range strings.Fields(out) {
		logged[line] = true
	}
	total := 0
	for _, c := range clients {
		for _, cs := range c.Acked() {
			total++
			if !logged[fmt.Sprintf("%d:%d", c.ID(), cs)] {
				t.Fatalf("client %d batch %d was acked but is not in Postgres", c.ID(), cs)
			}
		}
	}
	t.Logf("%d acked batches, all present in Postgres; %d objects on the board", total, want.Len())
}

// route asks the stack which node serves the board.
func route(t *testing.T, board string) string {
	t.Helper()
	base := strings.TrimSuffix(strings.Replace(wsURL(), "ws", "http", 1), "/ws")
	resp, err := http.Get(base + "/api/boards/" + board + "/route")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var r struct {
		NodeID string `json:"nodeId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.NodeID == "" {
		t.Fatalf("route: %v %+v", err, r)
	}
	return r.NodeID
}
