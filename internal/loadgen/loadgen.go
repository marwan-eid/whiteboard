// Package loadgen simulates many clients against a node. In this first
// version each client handshakes and measures TimePing round trips; editing
// scenarios arrive with the sync core (see docs/BENCHMARKS.md).
package loadgen

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

type Config struct {
	URL     string
	BoardID string
	Clients int
	// Duration is how long each client keeps pinging after it connects.
	Duration time.Duration
	// Ramp spreads client connects evenly over this period.
	Ramp         time.Duration
	PingInterval time.Duration
}

type Result struct {
	Connected int
	Failed    int
	// Errors counts failures by message, for the summary.
	Errors map[string]int
	RTTs   []time.Duration
}

type Summary struct {
	Count         int
	P50, P99, Max time.Duration
}

// Summarize computes nearest-rank percentiles over the recorded RTTs.
func (r Result) Summarize() Summary {
	if len(r.RTTs) == 0 {
		return Summary{}
	}
	s := append([]time.Duration(nil), r.RTTs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := func(p float64) time.Duration {
		i := int(p*float64(len(s))+0.999999) - 1
		return s[max(0, min(i, len(s)-1))]
	}
	return Summary{Count: len(s), P50: rank(0.50), P99: rank(0.99), Max: s[len(s)-1]}
}

func Run(ctx context.Context, cfg Config) Result {
	res := Result{Errors: map[string]int{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := time.Now()

	for i := range cfg.Clients {
		delay := time.Duration(0)
		if cfg.Clients > 1 {
			delay = cfg.Ramp * time.Duration(i) / time.Duration(cfg.Clients)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			}
			rtts, err := runClient(ctx, cfg, start)
			mu.Lock()
			defer mu.Unlock()
			res.RTTs = append(res.RTTs, rtts...)
			if err != nil {
				res.Failed++
				res.Errors[err.Error()]++
			} else {
				res.Connected++
			}
		}()
	}
	wg.Wait()
	return res
}

func runClient(ctx context.Context, cfg Config, epoch time.Time) ([]time.Duration, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dialCtx, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 20)

	if err := write(ctx, c, &pb.ClientMessage{Msg: &pb.ClientMessage_Hello{Hello: &pb.Hello{
		ProtocolVersion: protocol.Version,
		BoardId:         cfg.BoardID,
		ClientId:        rand.Uint64N(protocol.MaxClientID) + 1,
	}}}); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}
	msg, err := read(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("await welcome: %w", err)
	}
	if msg.GetWelcome() == nil {
		return nil, fmt.Errorf("expected welcome, got %v", msg)
	}

	var rtts []time.Duration
	deadline := time.Now().Add(cfg.Duration)
	ticker := time.NewTicker(cfg.PingInterval)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		// t0 is a monotonic offset from a shared epoch, like performance.now() in the browser.
		sent := time.Since(epoch)
		if err := write(ctx, c, &pb.ClientMessage{Msg: &pb.ClientMessage_TimePing{TimePing: &pb.TimePing{
			T0: float64(sent.Microseconds()) / 1000,
		}}}); err != nil {
			return rtts, fmt.Errorf("send ping: %w", err)
		}
		msg, err := read(ctx, c)
		if err != nil {
			return rtts, fmt.Errorf("await pong: %w", err)
		}
		if msg.GetTimePong() == nil {
			return rtts, fmt.Errorf("expected pong, got %v", msg)
		}
		rtts = append(rtts, time.Since(epoch)-sent)

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return rtts, ctx.Err()
		}
	}
	_ = c.Close(websocket.StatusNormalClosure, "done")
	return rtts, nil
}

func write(ctx context.Context, c *websocket.Conn, msg *pb.ClientMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageBinary, data)
}

func read(ctx context.Context, c *websocket.Conn) (*pb.ServerMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var msg pb.ServerMessage
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	if e := msg.GetError(); e != nil {
		return nil, errors.New("server error: " + e.GetMessage())
	}
	return &msg, nil
}
