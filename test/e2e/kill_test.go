//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
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

// SIGKILL the node while clients edit. Clients must reconnect to the
// restarted node, converge, and every edit the server acked must be in Postgres.
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
	compose(t, "kill", "-s", "SIGKILL", "node-1")
	killed.Store(true)
	time.Sleep(time.Second) // edits keep queueing while the node is down
	compose(t, "up", "--detach", "--wait", "--wait-timeout", "60", "node-1")
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
			t.Fatal("clients did not settle after the restart")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A fresh client loads the board from the restarted node; everyone must match it.
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
