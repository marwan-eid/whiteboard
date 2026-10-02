package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"whiteboard/internal/board"
	"whiteboard/internal/db"
	"whiteboard/internal/db/dbtest"
)

func TestPreferredIsBalancedAndStable(t *testing.T) {
	nodes := []string{"node-1", "node-2", "node-3"}
	count := map[string]int{}
	moved := 0
	const boards = 3000
	for i := range boards {
		id := fmt.Sprintf("board-%d", i)
		p := Preferred(id, nodes)
		count[p]++
		// Removing node-3 moves only the boards it had.
		if q := Preferred(id, nodes[:2]); q != p {
			if p != "node-3" {
				t.Fatalf("%s moved from %s to %s although %s stayed", id, p, q, p)
			}
			moved++
		}
	}
	for _, n := range nodes {
		if c := count[n]; c < boards/3*8/10 || c > boards/3*12/10 {
			t.Fatalf("unbalanced: %v", count)
		}
	}
	if moved != count["node-3"] {
		t.Fatalf("moved %d, node-3 had %d", moved, count["node-3"])
	}
	if Preferred("x", nil) != "" {
		t.Fatal("no nodes, no preference")
	}
}

func TestLeases(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := func(id string) Config {
		return Config{NodeID: id, Heartbeat: 50 * time.Millisecond, LiveFor: 200 * time.Millisecond, LeaseTTL: 400 * time.Millisecond}
	}
	a, b := New(cfg("node-a"), pool, log), New(cfg("node-b"), pool, log)
	for _, n := range []*Node{a, b, a} { // a again, to see b
		if err := n.Join(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(a.Live()) != "[node-a node-b]" {
		t.Fatalf("live = %v", a.Live())
	}
	// A board node-a prefers.
	var boardID string
	for i := 0; boardID == ""; i++ {
		if id := fmt.Sprintf("b%d", i); Preferred(id, a.Live()) == "node-a" {
			boardID = id
		}
	}
	moved := func(err error) string {
		var m *board.MovedError
		if !errors.As(err, &m) {
			t.Fatalf("want MovedError, got %v", err)
		}
		return m.Node
	}

	// The non-preferred node sends clients to the preferred one.
	if _, err := b.Claim(ctx, boardID); moved(err) != "node-a" {
		t.Fatal("b should defer to a")
	}
	epoch, err := a.Claim(ctx, boardID)
	if err != nil || epoch != 1 {
		t.Fatalf("a claims: %d, %v", epoch, err)
	}
	if _, err := b.Claim(ctx, boardID); moved(err) != "node-a" {
		t.Fatal("b should see a's lease")
	}
	for _, n := range []*Node{a, b} {
		if r, err := n.Route(ctx, boardID); err != nil || r != "node-a" {
			t.Fatalf("route from %s = %q, %v", n.ID(), r, err)
		}
	}
	if lost, err := a.Renew(ctx, map[string]uint64{boardID: epoch}); err != nil || len(lost) != 0 {
		t.Fatalf("renew: %v, %v", lost, err)
	}

	// node-a goes silent: no heartbeats, no renewals. Once both have run out,
	// node-b is the only live node and takes the board with a new epoch.
	time.Sleep(450 * time.Millisecond)
	if err := b.Join(ctx); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(b.Live()) != "[node-b]" {
		t.Fatalf("live = %v", b.Live())
	}
	epochB, err := b.Claim(ctx, boardID)
	if err != nil || epochB != 2 {
		t.Fatalf("b takes over: %d, %v", epochB, err)
	}
	// node-a wakes up: its lease is gone, and releasing it changes nothing.
	if lost, err := a.Renew(ctx, map[string]uint64{boardID: epoch}); err != nil || fmt.Sprint(lost) != "["+boardID+"]" {
		t.Fatalf("stale renew: %v, %v", lost, err)
	}
	if err := a.Release(ctx, boardID, epoch); err != nil {
		t.Fatal(err)
	}
	if r, _ := a.Route(ctx, boardID); r != "node-b" {
		t.Fatalf("route after takeover = %q", r)
	}

	// Releasing hands the board over at once; reclaiming by the same node bumps the epoch.
	if err := b.Release(ctx, boardID, epochB); err != nil {
		t.Fatal(err)
	}
	if e, err := b.Claim(ctx, boardID); err != nil || e != 3 {
		t.Fatalf("reclaim: %d, %v", e, err)
	}
	if err := a.Join(ctx); err != nil {
		t.Fatal(err)
	}
	b.Leave()
	if err := a.Join(ctx); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a.Live()) != "[node-a]" {
		t.Fatalf("after b left, live = %v", a.Live())
	}
}

func TestEventsReachOtherNodes(t *testing.T) {
	pool := dbtest.NewPool(t)
	if _, err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, b := New(Config{NodeID: "node-a"}, pool, log), New(Config{NodeID: "node-b"}, pool, log)
	got := make(chan string, 1)
	go b.Listen(t.Context(), func(boardID, linkID string) {
		select {
		case got <- boardID + "/" + linkID:
		default: // an earlier announcement already arrived
		}
	})
	// The listener needs a moment to subscribe; keep announcing until it hears one.
	deadline := time.After(10 * time.Second)
	for {
		if err := a.KickLink(t.Context(), "board-1", "link-1"); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-got:
			if e != "board-1/link-1" {
				t.Fatalf("event %q", e)
			}
			return
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("event never arrived")
		}
	}
}
