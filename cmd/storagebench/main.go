// Command storagebench measures storage per edit with full history
// (docs/BENCHMARKS.md, target 4). It writes a synthetic edit stream through
// the real Postgres store into a dedicated database, then reports table
// sizes before and after compaction.
//
//	go run ./cmd/storagebench -db postgres://.../storagebench -edits 100000
//
// The database is wiped first: never point it at real data.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/board"
	"whiteboard/internal/db"
	"whiteboard/internal/doc"
	"whiteboard/internal/hlc"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/store"
)

func main() {
	url := flag.String("db", "", "Postgres URL of a scratch database (it is wiped)")
	edits := flag.Int("edits", 100_000, "batches to write")
	perTick := flag.Int("tick", 50, "batches per append (one board tick)")
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "-db is required")
		os.Exit(2)
	}
	if err := run(*url, *edits, *perTick); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(url string, edits, perTick int) error {
	ctx := context.Background()
	pool, err := db.Connect(ctx, url, 30*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, "TRUNCATE ops, op_segments, snapshots, boards"); err != nil {
		return err
	}
	st, err := store.NewPostgres(pool)
	if err != nil {
		return err
	}
	const boardID = "storagebench"
	if _, err := st.Load(ctx, boardID); err != nil {
		return err
	}

	// The same mix as the load-test scenario: 60% moves, 20% creates,
	// 15% text edits, 5% deletes, from 20 editors.
	rng := rand.New(rand.NewPCG(1, 2))
	d := doc.New()
	var live []string
	next := 0
	wall := time.Now().UnixMilli()
	var buf []board.LogEntry
	start := time.Now()
	for seq := uint64(1); seq <= uint64(edits); seq++ {
		client := uint64(100 + rng.IntN(20))
		var op *pb.Op
		switch r := rng.IntN(100); {
		case r < 20 || len(live) == 0:
			next++
			id := strconv.FormatUint(client, 36) + ":" + strconv.Itoa(next)
			live = append(live, id)
			op = &pb.Op{Id: id, Props: &pb.ObjectProps{
				Type: pb.ShapeType(1 + rng.IntN(3)).Enum(), X: proto.Float64(float64(rng.IntN(20000))), Y: proto.Float64(float64(rng.IntN(20000))),
				W: proto.Float64(160), H: proto.Float64(100), Z: proto.String("a0"), Fill: proto.Uint32(rng.Uint32() | 0xff),
				Stroke: proto.Uint32(0x1f2328ff), StrokeWidth: proto.Float32(1.5),
			}}
		case r < 80:
			op = &pb.Op{Id: live[rng.IntN(len(live))], Props: &pb.ObjectProps{X: proto.Float64(float64(rng.IntN(20000))), Y: proto.Float64(float64(rng.IntN(20000)))}}
		case r < 95:
			op = &pb.Op{Id: live[rng.IntN(len(live))], Props: &pb.ObjectProps{Text: proto.String(fmt.Sprintf("note %d", rng.IntN(100000)))}}
		default:
			i := rng.IntN(len(live))
			op = &pb.Op{Id: live[i], Props: &pb.ObjectProps{Deleted: proto.Bool(true)}}
			live = append(live[:i], live[i+1:]...)
		}
		wall += int64(rng.IntN(40))
		stamp := hlc.Stamp{WallMs: wall, ClientID: client}
		d.Apply(op, stamp)
		buf = append(buf, board.LogEntry{Seq: seq, ClientID: client, ClientSeq: seq, Stamp: stamp.Proto(), Ops: []*pb.Op{op}})
		if len(buf) == perTick || seq == uint64(edits) {
			if err := st.Append(ctx, boardID, buf); err != nil {
				return err
			}
			buf = buf[:0]
		}
	}
	fmt.Printf("wrote %d edits (%d objects live) in %.1fs\n", edits, len(live), time.Since(start).Seconds())

	if err := report(ctx, pool, "log only (no compaction)", edits); err != nil {
		return err
	}
	snap := &pb.BoardSnapshot{Seq: uint64(edits), Objects: d.Snapshot()}
	if err := st.SaveSnapshot(ctx, boardID, snap); err != nil {
		return err
	}
	if _, err := st.Compact(ctx, boardID, uint64(edits)); err != nil {
		return err
	}
	// Compaction deletes rows; VACUUM FULL returns their space so sizes are comparable.
	if _, err := pool.Exec(ctx, "VACUUM FULL ops, op_segments, snapshots"); err != nil {
		return err
	}
	return report(ctx, pool, "snapshot + compacted log", edits)
}

// report prints on-disk sizes (tables with indexes and TOAST, which is what
// storage costs) and payload bytes (the encoded data alone), per edit.
func report(ctx context.Context, pool *pgxpool.Pool, label string, edits int) error {
	var ops, segs, snaps, opsData, segsData, snapsData int64
	err := pool.QueryRow(ctx, `SELECT
		pg_total_relation_size('ops'), pg_total_relation_size('op_segments'), pg_total_relation_size('snapshots'),
		(SELECT coalesce(sum(octet_length(batch)), 0) FROM ops),
		(SELECT coalesce(sum(octet_length(data)), 0) FROM op_segments),
		(SELECT coalesce(sum(octet_length(data)), 0) FROM snapshots)`).Scan(&ops, &segs, &snaps, &opsData, &segsData, &snapsData)
	if err != nil {
		return err
	}
	per := func(b int64) string { return fmt.Sprintf("%.1f B/edit", float64(b)/float64(edits)) }
	total := ops + segs + snaps
	fmt.Printf("\n%s, %d edits\n", label, edits)
	fmt.Printf("  %-12s %12s %14s %14s\n", "", "on disk", "per edit", "payload")
	for _, r := range []struct {
		name       string
		disk, data int64
	}{{"ops", ops, opsData}, {"op_segments", segs, segsData}, {"snapshots", snaps, snapsData}} {
		fmt.Printf("  %-12s %12d %14s %14d\n", r.name, r.disk, per(r.disk), r.data)
	}
	fmt.Printf("  %-12s %12d %14s\n", "total", total, per(total))
	return nil
}
