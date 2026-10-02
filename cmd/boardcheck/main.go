// Command boardcheck prints a fingerprint of a board as a client sees it:
// its seq, object count, and a SHA-256 of every object's state (values and
// per-field stamps, in id order). Two runs print the same line exactly when
// the board is the same, which is how the restore drill checks a backup
// (docs/DEPLOY.md).
//
//	go run ./cmd/boardcheck -url ws://node-1:8081/ws -board demo
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	"whiteboard/internal/client"
)

func main() {
	url := flag.String("url", "ws://localhost:8080/ws", "WebSocket URL of a node (or Caddy)")
	board := flag.String("board", "demo", "board id")
	flag.Parse()
	if err := run(*url, *board); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(url, board string) error {
	c := client.New(client.Config{URL: url, BoardID: board})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		return err
	}
	objects := c.Snapshot() // sorted by id
	h := sha256.New()
	opts := proto.MarshalOptions{Deterministic: true}
	for _, o := range objects {
		b, err := opts.Marshal(o)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%d:", len(b))
		_, _ = h.Write(b)
	}
	fmt.Printf("board %s: seq %d, %d objects, sha256 %x\n", board, c.Stats().ServerSeq, len(objects), h.Sum(nil))
	return nil
}
