// Package usage counts real use of the demo (docs/BENCHMARKS.md, target 7),
// on the server and without third-party analytics: guests who edited on a
// day, and boards that had two or more people connected at once. Guest ids
// are random and carry no personal data; only a hash of each is stored.
package usage

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Recorder notes activity. Its methods never block the caller: each
// (day, key) is recorded once per process, by a background writer.
type Recorder struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time

	mu   sync.Mutex
	day  string
	seen map[string]bool // "e:" + guest or "t:" + board, for day
	jobs chan job
}

type job struct {
	day        string
	table, key string
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Recorder {
	return &Recorder{pool: pool, log: log, now: time.Now, seen: map[string]bool{}, jobs: make(chan job, 1024)}
}

// Run writes recorded activity until ctx ends.
func (r *Recorder) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-r.jobs:
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			switch j.table {
			case "guest":
				h := sha256.Sum256([]byte(j.key))
				_, err = r.pool.Exec(wctx, "INSERT INTO usage_guests (day, guest_hash) VALUES ($1, $2) ON CONFLICT DO NOTHING", j.day, h[:])
			case "board":
				_, err = r.pool.Exec(wctx, "INSERT INTO usage_boards (day, board_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", j.day, j.key)
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				r.log.Warn("recording usage failed", "err", err)
			}
		}
	}
}

func (r *Recorder) note(kind, table, key string) {
	if key == "" {
		return
	}
	day := r.now().UTC().Format(time.DateOnly)
	r.mu.Lock()
	if day != r.day {
		r.day, r.seen = day, map[string]bool{}
	}
	k := kind + key
	if r.seen[k] {
		r.mu.Unlock()
		return
	}
	r.seen[k] = true
	r.mu.Unlock()
	select {
	case r.jobs <- job{day: day, table: table, key: key}:
	default: // the writer is behind; a missed count is better than a slow edit
	}
}

// Edited notes that a guest edited a board today.
func (r *Recorder) Edited(guestID string) { r.note("e:", "guest", guestID) }

// Together notes that a board had two or more people connected today.
func (r *Recorder) Together(boardID string) { r.note("t:", "board", boardID) }

// Counts is what the stats panel and README report.
type Counts struct {
	GuestsToday int `json:"guestsToday"`
	Guests7d    int `json:"guests7d"`
	// SharedBoards are boards that had two or more people connected at once.
	SharedBoardsToday int `json:"sharedBoardsToday"`
	SharedBoards7d    int `json:"sharedBoards7d"`
}

// Counts reads the totals for today and the last 7 days (UTC).
func (r *Recorder) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	today := r.now().UTC().Format(time.DateOnly)
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM usage_guests WHERE day = $1::date),
			(SELECT count(DISTINCT guest_hash) FROM usage_guests WHERE day > $1::date - 7),
			(SELECT count(*) FROM usage_boards WHERE day = $1::date),
			(SELECT count(DISTINCT board_id) FROM usage_boards WHERE day > $1::date - 7)`, today).
		Scan(&c.GuestsToday, &c.Guests7d, &c.SharedBoardsToday, &c.SharedBoards7d)
	return c, err
}
