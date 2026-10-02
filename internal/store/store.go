// Package store is the Postgres implementation of board.Store: the event
// log in the ops table and zstd-compressed snapshots (ADR-0004).
package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/board"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

type Postgres struct {
	pool *pgxpool.Pool
	enc  *zstd.Encoder
	dec  *zstd.Decoder
}

var _ board.Store = (*Postgres)(nil)

func NewPostgres(pool *pgxpool.Pool) (*Postgres, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool, enc: enc, dec: dec}, nil
}

func (s *Postgres) Load(ctx context.Context, boardID string) (board.Loaded, error) {
	var l board.Loaded
	if _, err := s.pool.Exec(ctx, "INSERT INTO boards (id) VALUES ($1) ON CONFLICT DO NOTHING", boardID); err != nil {
		return l, fmt.Errorf("ensure board: %w", err)
	}
	// The snapshot and the tail after it are read in one snapshot of the
	// database: a snapshot plus compaction committing between two separate
	// reads would leave entries in neither.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return l, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		snapSeq int64
		data    []byte
	)
	err = tx.QueryRow(ctx,
		"SELECT seq, data FROM snapshots WHERE board_id = $1 ORDER BY seq DESC LIMIT 1", boardID,
	).Scan(&snapSeq, &data)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return l, fmt.Errorf("read snapshot: %w", err)
	default:
		raw, err := s.dec.DecodeAll(data, nil)
		if err != nil {
			return l, fmt.Errorf("decompress snapshot %d: %w", snapSeq, err)
		}
		l.Snapshot = &pb.BoardSnapshot{}
		if err := proto.Unmarshal(raw, l.Snapshot); err != nil {
			return l, fmt.Errorf("decode snapshot %d: %w", snapSeq, err)
		}
	}

	rows, err := tx.Query(ctx,
		"SELECT seq, client_id, client_seq, batch FROM ops WHERE board_id = $1 AND seq > $2 ORDER BY seq", boardID, snapSeq)
	if err != nil {
		return l, fmt.Errorf("read log: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			seq, clientID, clientSeq int64
			raw                      []byte
		)
		if err := rows.Scan(&seq, &clientID, &clientSeq, &raw); err != nil {
			return l, err
		}
		var sb pb.StoredBatch
		if err := proto.Unmarshal(raw, &sb); err != nil {
			return l, fmt.Errorf("decode op %d: %w", seq, err)
		}
		l.Tail = append(l.Tail, board.LogEntry{
			Seq: uint64(seq), ClientID: uint64(clientID), ClientSeq: uint64(clientSeq),
			Stamp: sb.GetStamp(), Ops: sb.GetOps(),
		})
	}
	return l, rows.Err()
}

func (s *Postgres) Append(ctx context.Context, boardID string, entries []board.LogEntry) error {
	rows := make([][]any, len(entries))
	for i, e := range entries {
		raw, err := proto.Marshal(&pb.StoredBatch{Stamp: e.Stamp, Ops: e.Ops})
		if err != nil {
			return err
		}
		rows[i] = []any{boardID, int64(e.Seq), int64(e.ClientID), int64(e.ClientSeq), raw}
	}
	// COPY is one statement, so the whole tick commits or none of it does.
	_, err := s.pool.CopyFrom(ctx, pgx.Identifier{"ops"},
		[]string{"board_id", "seq", "client_id", "client_seq", "batch"}, pgx.CopyFromRows(rows))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return fmt.Errorf("%w: %s", board.ErrConflict, pgErr.Detail)
	}
	return err
}

func (s *Postgres) SaveSnapshot(ctx context.Context, boardID string, snap *pb.BoardSnapshot) error {
	raw, err := proto.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx,
		"INSERT INTO snapshots (board_id, seq, data) VALUES ($1, $2, $3) ON CONFLICT (board_id, seq) DO NOTHING",
		boardID, int64(snap.GetSeq()), s.enc.EncodeAll(raw, nil)); err != nil {
		return err
	}
	return s.ThinSnapshots(ctx, boardID, SnapshotKeepAll)
}

// SnapshotKeepAll is how long every snapshot is kept (ADR-0004); older ones
// are thinned to the last of each UTC day.
const SnapshotKeepAll = 7 * 24 * time.Hour

// ThinSnapshots deletes the board's snapshots older than keepAll, except the
// newest of each UTC day and the newest overall. Snapshots only make loading
// and history faster: the log (rows and segments) is never thinned, so every
// version stays reachable by replay.
func (s *Postgres) ThinSnapshots(ctx context.Context, boardID string, keepAll time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM snapshots s
		WHERE s.board_id = $1
		  AND s.created_at < now() - make_interval(secs => $2)
		  AND s.seq < (SELECT max(seq) FROM snapshots WHERE board_id = $1)
		  AND EXISTS (
		    SELECT 1 FROM snapshots t
		    WHERE t.board_id = s.board_id AND t.seq > s.seq
		      AND date_trunc('day', t.created_at AT TIME ZONE 'UTC') = date_trunc('day', s.created_at AT TIME ZONE 'UTC'))`,
		boardID, keepAll.Seconds())
	return err
}

// segmentSize bounds how many entries go in one compressed segment, so
// reading a little history never means decompressing a huge blob.
const segmentSize = 10_000

// Range reads segments and live rows in one repeatable-read transaction: a
// compaction committing between two separate reads would move entries out of
// the rows after the segments were read, and they would be seen in neither.
func (s *Postgres) Range(ctx context.Context, boardID string, from, to uint64) ([]board.LogEntry, error) {
	to = min(to, math.MaxInt64) // seqs are bigint; a larger bound would wrap negative
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var out []board.LogEntry
	rows, err := tx.Query(ctx,
		"SELECT data FROM op_segments WHERE board_id = $1 AND to_seq > $2 AND from_seq <= $3 ORDER BY from_seq",
		boardID, int64(from), int64(to))
	if err != nil {
		return nil, fmt.Errorf("read segments: %w", err)
	}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return nil, err
		}
		raw, err := s.dec.DecodeAll(data, nil)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("decompress segment: %w", err)
		}
		var seg pb.LogSegment
		if err := proto.Unmarshal(raw, &seg); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode segment: %w", err)
		}
		for _, e := range seg.GetEntries() {
			if e.GetSeq() > from && e.GetSeq() <= to {
				out = append(out, board.LogEntry{Seq: e.GetSeq(), ClientID: e.GetClientId(), ClientSeq: e.GetClientSeq(), Stamp: e.GetStamp(), Ops: e.GetOps()})
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	live, err := opsRange(ctx, tx, boardID, from, to)
	if err != nil {
		return nil, err
	}
	return append(out, live...), tx.Commit(ctx)
}

func opsRange(ctx context.Context, tx pgx.Tx, boardID string, from, to uint64) ([]board.LogEntry, error) {
	rows, err := tx.Query(ctx,
		"SELECT seq, client_id, client_seq, batch FROM ops WHERE board_id = $1 AND seq > $2 AND seq <= $3 ORDER BY seq",
		boardID, int64(from), int64(to))
	if err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	defer rows.Close()
	var out []board.LogEntry
	for rows.Next() {
		var (
			seq, clientID, clientSeq int64
			raw                      []byte
		)
		if err := rows.Scan(&seq, &clientID, &clientSeq, &raw); err != nil {
			return nil, err
		}
		var sb pb.StoredBatch
		if err := proto.Unmarshal(raw, &sb); err != nil {
			return nil, fmt.Errorf("decode op %d: %w", seq, err)
		}
		out = append(out, board.LogEntry{Seq: uint64(seq), ClientID: uint64(clientID), ClientSeq: uint64(clientSeq), Stamp: sb.GetStamp(), Ops: sb.GetOps()})
	}
	return out, rows.Err()
}

func (s *Postgres) SnapshotAtOrBefore(ctx context.Context, boardID string, seq uint64) (*pb.BoardSnapshot, error) {
	var data []byte
	err := s.pool.QueryRow(ctx,
		"SELECT data FROM snapshots WHERE board_id = $1 AND seq <= $2 ORDER BY seq DESC LIMIT 1", boardID, int64(seq),
	).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err := s.dec.DecodeAll(data, nil)
	if err != nil {
		return nil, err
	}
	snap := &pb.BoardSnapshot{}
	return snap, proto.Unmarshal(raw, snap)
}

// Compact moves ops rows with seq <= upTo into segments of up to
// segmentSize entries. Each segment is written and its rows deleted in one
// transaction, so every seq is always stored exactly once.
func (s *Postgres) Compact(ctx context.Context, boardID string, upTo uint64) (int, error) {
	moved := 0
	for {
		n, err := s.compactOne(ctx, boardID, upTo)
		moved += n
		if err != nil || n == 0 {
			return moved, err
		}
	}
}

func (s *Postgres) compactOne(ctx context.Context, boardID string, upTo uint64) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		// FOR UPDATE: a crashed board's compaction can still be running when the
		// reloaded board starts its own; the second waits instead of writing the
		// same entries into a second segment.
		"SELECT seq, client_id, client_seq, batch FROM ops WHERE board_id = $1 AND seq <= $2 ORDER BY seq LIMIT $3 FOR UPDATE",
		boardID, int64(upTo), segmentSize)
	if err != nil {
		return 0, err
	}
	var seg pb.LogSegment
	for rows.Next() {
		var (
			seq, clientID, clientSeq int64
			raw                      []byte
		)
		if err := rows.Scan(&seq, &clientID, &clientSeq, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		var sb pb.StoredBatch
		if err := proto.Unmarshal(raw, &sb); err != nil {
			rows.Close()
			return 0, err
		}
		seg.Entries = append(seg.Entries, &pb.SegmentEntry{
			Seq: uint64(seq), ClientId: uint64(clientID), ClientSeq: uint64(clientSeq), Stamp: sb.GetStamp(), Ops: sb.GetOps(),
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(seg.Entries) == 0 {
		return 0, err
	}
	raw, err := proto.Marshal(&seg)
	if err != nil {
		return 0, err
	}
	first, last := seg.Entries[0].GetSeq(), seg.Entries[len(seg.Entries)-1].GetSeq()
	if _, err := tx.Exec(ctx, "INSERT INTO op_segments (board_id, from_seq, to_seq, data) VALUES ($1, $2, $3, $4)",
		boardID, int64(first), int64(last), s.enc.EncodeAll(raw, nil)); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM ops WHERE board_id = $1 AND seq >= $2 AND seq <= $3", boardID, int64(first), int64(last)); err != nil {
		return 0, err
	}
	return len(seg.Entries), tx.Commit(ctx)
}
