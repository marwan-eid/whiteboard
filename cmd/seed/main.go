// Command seed fills a board with many objects through the normal sync
// protocol, for the 100k-object benchmark (docs/BENCHMARKS.md).
//
//	go run ./cmd/seed -url ws://localhost:8080/ws -board big -objects 100000
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	"whiteboard/internal/client"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

func main() {
	url := flag.String("url", "ws://localhost:8080/ws", "WebSocket URL of a node (or Caddy)")
	boardID := flag.String("board", "big", "board id")
	n := flag.Int("objects", 100_000, "objects to create")
	layout := flag.String("layout", "grid", "grid or clustered")
	batch := flag.Int("batch", 500, "ops per batch")
	seed := flag.Uint64("seed", 1, "random seed")
	flag.Parse()
	if err := run(*url, *boardID, *n, *layout, *batch, *seed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(url, boardID string, n int, layout string, batchSize int, seed uint64) error {
	// A tiny viewport far off the board, so the server does not send our own
	// objects back to us.
	c := client.New(client.Config{URL: url, BoardID: boardID, Viewport: &pb.Viewport{X: -9e8, Y: -9e8, W: 1, H: 1}})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		return err
	}

	rng := rand.New(rand.NewPCG(seed, 7))
	cols := int(math.Ceil(math.Sqrt(float64(n))))
	centers := make([][2]float64, 40)
	for i := range centers {
		centers[i] = [2]float64{rng.Float64() * 60_000, rng.Float64() * 60_000}
	}
	start := time.Now()
	var ops []*pb.Op
	for i := range n {
		var x, y float64
		switch layout {
		case "grid":
			x, y = float64(i%cols)*220, float64(i/cols)*160
		case "clustered":
			ctr := centers[rng.IntN(len(centers))]
			x, y = ctr[0]+rng.NormFloat64()*3000, ctr[1]+rng.NormFloat64()*3000
		default:
			return fmt.Errorf("unknown layout %q", layout)
		}
		p := &pb.ObjectProps{
			X: proto.Float64(math.Round(x)), Y: proto.Float64(math.Round(y)),
			W: proto.Float64(180), H: proto.Float64(120),
			// All seeded objects share one valid fractional index; ties order by id.
			Z:           proto.String("a0"),
			Fill:        proto.Uint32(rng.Uint32() | 0xff),
			Stroke:      proto.Uint32(0x1f2328ff),
			StrokeWidth: proto.Float32(1.5),
		}
		switch r := rng.IntN(10); {
		case r < 6:
			p.Type = pb.ShapeType_SHAPE_TYPE_RECT.Enum()
		case r < 8:
			p.Type = pb.ShapeType_SHAPE_TYPE_ELLIPSE.Enum()
		default:
			p.Type = pb.ShapeType_SHAPE_TYPE_STICKY.Enum()
			p.W, p.H = proto.Float64(150), proto.Float64(150)
			p.Fill = proto.Uint32(0xfff3b0ff)
			p.Text = proto.String(fmt.Sprintf("Note %d", i))
		}
		ops = append(ops, &pb.Op{Id: c.NewObjectID(), Props: p})
		if len(ops) == batchSize || i == n-1 {
			c.Edit(ops...)
			ops = nil
		}
	}
	for c.Stats().Pending > 0 {
		if time.Since(start) > 10*time.Minute {
			return fmt.Errorf("gave up waiting for acks; %d batches pending", c.Stats().Pending)
		}
		time.Sleep(50 * time.Millisecond)
	}
	elapsed := time.Since(start)
	fmt.Printf("created %d objects on board %q in %.1fs (%.0f objects/s)\n", n, boardID, elapsed.Seconds(), float64(n)/elapsed.Seconds())
	return nil
}
