package usage

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"whiteboard/internal/db"
	"whiteboard/internal/db/dbtest"
)

func TestCounts(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := New(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	day := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return day }
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go r.Run(runCtx)

	waitFor := func(want Counts) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got, err := r.Counts(ctx)
			if err == nil && got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("counts = %+v (err %v), want %+v", got, err, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Many edits by two guests, and one shared board, count once each.
	for range 5 {
		r.Edited("alice")
		r.Edited("bob")
		r.Together("demo")
	}
	r.Edited("") // anonymous: not counted
	waitFor(Counts{GuestsToday: 2, Guests7d: 2, SharedBoardsToday: 1, SharedBoards7d: 1})

	// The next day: alice again, and carol. The week counts each guest once.
	day = day.Add(24 * time.Hour)
	r.Edited("alice")
	r.Edited("carol")
	waitFor(Counts{GuestsToday: 2, Guests7d: 3, SharedBoardsToday: 0, SharedBoards7d: 1})
}
