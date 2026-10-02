package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"whiteboard/internal/board"
	"whiteboard/internal/db"
	"whiteboard/internal/db/dbtest"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/store"
)

func newStore(t *testing.T) *store.Postgres {
	t.Helper()
	pool := dbtest.NewPool(t)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	s, err := store.NewPostgres(pool)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func entry(seq, client, clientSeq uint64, x float64) board.LogEntry {
	return board.LogEntry{
		Seq: seq, ClientID: client, ClientSeq: clientSeq,
		Stamp: &pb.Stamp{WallMs: int64(1000 + seq), ClientId: client},
		Ops:   []*pb.Op{{Id: "a:1", Props: &pb.ObjectProps{X: proto.Float64(x)}}},
	}
}

func TestStore(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	t.Run("new board loads empty", func(t *testing.T) {
		l, err := s.Load(ctx, "fresh")
		if err != nil || l.Snapshot != nil || len(l.Tail) != 0 {
			t.Fatalf("got %+v, %v", l, err)
		}
	})

	t.Run("appended entries load back in order", func(t *testing.T) {
		if _, err := s.Load(ctx, "b1"); err != nil {
			t.Fatal(err)
		}
		if err := s.Append(ctx, "b1", []board.LogEntry{entry(1, 10, 1, 1), entry(2, 11, 1, 2)}); err != nil {
			t.Fatal(err)
		}
		if err := s.Append(ctx, "b1", []board.LogEntry{entry(3, 10, 2, 3)}); err != nil {
			t.Fatal(err)
		}
		l, err := s.Load(ctx, "b1")
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Tail) != 3 {
			t.Fatalf("tail has %d entries", len(l.Tail))
		}
		for i, e := range l.Tail {
			want := entry(uint64(i+1), e.ClientID, e.ClientSeq, float64(i+1))
			if e.Seq != want.Seq || !proto.Equal(e.Stamp, want.Stamp) || !proto.Equal(e.Ops[0], want.Ops[0]) {
				t.Fatalf("entry %d = %+v", i, e)
			}
		}
		if l.Tail[2].ClientID != 10 || l.Tail[2].ClientSeq != 2 {
			t.Fatalf("client columns lost: %+v", l.Tail[2])
		}
	})

	t.Run("a second writer at the same seq conflicts and writes nothing", func(t *testing.T) {
		if _, err := s.Load(ctx, "b2"); err != nil {
			t.Fatal(err)
		}
		if err := s.Append(ctx, "b2", []board.LogEntry{entry(1, 10, 1, 1)}); err != nil {
			t.Fatal(err)
		}
		err := s.Append(ctx, "b2", []board.LogEntry{entry(2, 10, 2, 2), entry(1, 11, 1, 9)})
		if !errors.Is(err, board.ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
		l, _ := s.Load(ctx, "b2")
		if len(l.Tail) != 1 {
			t.Fatalf("partial write: tail has %d entries, want 1", len(l.Tail))
		}
	})

	t.Run("load returns the latest snapshot and only the log after it", func(t *testing.T) {
		if _, err := s.Load(ctx, "b3"); err != nil {
			t.Fatal(err)
		}
		for i := uint64(1); i <= 5; i++ {
			if err := s.Append(ctx, "b3", []board.LogEntry{entry(i, 10, i, float64(i))}); err != nil {
				t.Fatal(err)
			}
		}
		for _, seq := range []uint64{2, 4} {
			snap := &pb.BoardSnapshot{
				Seq:     seq,
				Objects: []*pb.ObjectState{{Id: "a:1", Props: &pb.ObjectProps{X: proto.Float64(float64(seq))}}},
				Clients: []*pb.ClientProgress{{ClientId: 10, LastClientSeq: seq}},
			}
			if err := s.SaveSnapshot(ctx, "b3", snap); err != nil {
				t.Fatal(err)
			}
		}
		l, err := s.Load(ctx, "b3")
		if err != nil {
			t.Fatal(err)
		}
		if l.Snapshot.GetSeq() != 4 || l.Snapshot.GetClients()[0].GetLastClientSeq() != 4 {
			t.Fatalf("snapshot = %v", l.Snapshot)
		}
		if len(l.Tail) != 1 || l.Tail[0].Seq != 5 {
			t.Fatalf("tail = %+v, want only seq 5", l.Tail)
		}
		// Saving the same snapshot again is harmless.
		if err := s.SaveSnapshot(ctx, "b3", l.Snapshot); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCompactionKeepsHistory(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Load(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	var all []board.LogEntry
	for i := uint64(1); i <= 25; i++ {
		e := entry(i, 10, i, float64(i))
		all = append(all, e)
		if err := s.Append(ctx, "c", []board.LogEntry{e}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveSnapshot(ctx, "c", &pb.BoardSnapshot{Seq: 20}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Compact(ctx, "c", 20)
	if err != nil || n != 20 {
		t.Fatalf("compacted %d, %v; want 20", n, err)
	}
	if n, _ := s.Compact(ctx, "c", 20); n != 0 {
		t.Fatalf("second compaction moved %d", n)
	}

	// Any range reads the same, across segments and live rows.
	for _, r := range [][2]uint64{{0, 25}, {5, 22}, {19, 21}, {20, 25}, {0, 3}} {
		got, err := s.Range(ctx, "c", r[0], r[1])
		if err != nil {
			t.Fatal(err)
		}
		want := all[r[0]:r[1]]
		if len(got) != len(want) {
			t.Fatalf("range %v: %d entries, want %d", r, len(got), len(want))
		}
		for i := range got {
			if got[i].Seq != want[i].Seq || got[i].ClientSeq != want[i].ClientSeq || !proto.Equal(got[i].Ops[0], want[i].Ops[0]) {
				t.Fatalf("range %v entry %d = %+v", r, i, got[i])
			}
		}
	}
	// Loading still sees the snapshot plus the uncompacted tail.
	l, err := s.Load(ctx, "c")
	if err != nil || l.Snapshot.GetSeq() != 20 || len(l.Tail) != 5 {
		t.Fatalf("load after compaction: snapshot %d, tail %d, %v", l.Snapshot.GetSeq(), len(l.Tail), err)
	}
	if snap, _ := s.SnapshotAtOrBefore(ctx, "c", 19); snap != nil {
		t.Fatalf("snapshot at or before 19 = %v, want none", snap)
	}
	if snap, _ := s.SnapshotAtOrBefore(ctx, "c", 24); snap.GetSeq() != 20 {
		t.Fatalf("snapshot at or before 24 = %d, want 20", snap.GetSeq())
	}
}

// Compactions racing each other (a crashed board's and its reload's) and a
// reader racing both: every entry is read exactly once, every time.
func TestConcurrentCompactionAndRange(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Load(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	const n = 2000
	for i := uint64(1); i <= n; i += 100 {
		var batch []board.LogEntry
		for j := i; j < i+100; j++ {
			batch = append(batch, entry(j, 10, j, float64(j)))
		}
		if err := s.Append(ctx, "r", batch); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveSnapshot(ctx, "r", &pb.BoardSnapshot{Seq: n}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 3 {
		wg.Go(func() {
			if _, err := s.Compact(ctx, "r", n); err != nil {
				errs <- err
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			got, err := s.Range(ctx, "r", 0, n)
			if err != nil {
				errs <- err
				return
			}
			if len(got) != n {
				errs <- fmt.Errorf("range read %d entries during compaction, want %d", len(got), n)
				return
			}
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got, err := s.Range(ctx, "r", 0, n)
	if err != nil || len(got) != n {
		t.Fatalf("after: %d entries, %v", len(got), err)
	}
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d has seq %d: duplicated or missing", i, e.Seq)
		}
	}
}
