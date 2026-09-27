// Package store is the Postgres implementation of board.Store: the event
// log in the ops table and zstd-compressed snapshots (ADR-0004).
package store

import (
	"context"
	"errors"
	"fmt"

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

	var (
		snapSeq int64
		data    []byte
	)
	err := s.pool.QueryRow(ctx,
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

	rows, err := s.pool.Query(ctx,
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
	_, err = s.pool.Exec(ctx,
		"INSERT INTO snapshots (board_id, seq, data) VALUES ($1, $2, $3) ON CONFLICT (board_id, seq) DO NOTHING",
		boardID, int64(snap.GetSeq()), s.enc.EncodeAll(raw, nil))
	return err
}
