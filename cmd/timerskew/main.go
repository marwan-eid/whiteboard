// Command timerskew measures how closely clients agree on the board timer
// (docs/BENCHMARKS.md, target 6). It starts a timer, then connects simulated
// clients whose local clocks are off by up to ±5 minutes and whose messages
// are delayed by random, asymmetric amounts. Each estimates the server clock
// the way the web client does (web/src/net/clock.ts: the lowest-RTT ping of
// the last 8) and computes the remaining time it would display. Every 100 ms
// the spread (max − min) across clients is sampled.
//
//	go run ./cmd/timerskew -url ws://localhost:8080/ws -clients 50
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/client"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

func main() {
	url := flag.String("url", "ws://localhost:8080/ws", "WebSocket URL of a node (or Caddy)")
	board := flag.String("board", fmt.Sprintf("skew-%d", time.Now().Unix()), "board id")
	n := flag.Int("clients", 50, "simulated clients")
	maxOffset := flag.Duration("clock-offset", 5*time.Minute, "client clocks are off by up to ± this")
	minDelay := flag.Duration("min-delay", 10*time.Millisecond, "least added delay, each way")
	maxDelay := flag.Duration("max-delay", 75*time.Millisecond, "most added delay, each way")
	duration := flag.Duration("duration", 30*time.Second, "how long to sample")
	flag.Parse()
	if err := run(*url, *board, *n, *maxOffset, *minDelay, *maxDelay, *duration); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type sample struct{ rtt, offset float64 }

// sim is one client with a wrong clock and a jittery network.
type sim struct {
	clockOffset time.Duration // local clock minus true time
	minD, maxD  time.Duration
	rng         *rand.Rand

	mu      sync.Mutex
	samples []sample // newest last, at most 8
	seeded  bool
	seed    float64
	endsAt  float64
}

func (s *sim) local() float64 { return float64(time.Now().Add(s.clockOffset).UnixNano()) / 1e6 }

func (s *sim) delay() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.minD + time.Duration(s.rng.Int64N(int64(s.maxD-s.minD)+1))
}

// offset is the server-minus-local estimate, as in web/src/net/clock.ts.
func (s *sim) offset() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.samples) == 0 {
		return s.seed
	}
	best := s.samples[0]
	for _, x := range s.samples[1:] {
		if x.rtt < best.rtt {
			best = x
		}
	}
	return best.offset
}

// remaining is what this client would display, in ms.
func (s *sim) remaining() (float64, bool) {
	s.mu.Lock()
	ends, ok := s.endsAt, s.seeded && s.endsAt > 0
	s.mu.Unlock()
	if !ok {
		return 0, false
	}
	return ends - (s.local() + s.offset()), true
}

func run(url, board string, n int, maxOffset, minD, maxD, duration time.Duration) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a 10-minute timer, stamped on the server's clock.
	ctl := client.New(client.Config{URL: url, BoardID: board})
	defer ctl.Close()
	cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
	err := ctl.Connect(cctx)
	ccancel()
	if err != nil {
		return err
	}
	ends := time.Now().Add(10 * time.Minute).UnixMilli()
	ctl.Edit(&pb.Op{Id: protocol.TimerID, Props: &pb.ObjectProps{
		Type: pb.ShapeType_SHAPE_TYPE_TIMER.Enum(), EndsAtMs: proto.Int64(ends), RemainingMs: proto.Int64(600_000),
	}})
	time.Sleep(time.Second) // let it commit

	// These simple clients do not follow Moved: send them to the serving node.
	nodeURL, err := route(ctx, url, board)
	if err != nil {
		return err
	}

	sims := make([]*sim, n)
	for i := range sims {
		rng := rand.New(rand.NewPCG(uint64(i), 99))
		sims[i] = &sim{
			clockOffset: time.Duration((rng.Float64()*2 - 1) * float64(maxOffset)),
			minD:        minD, maxD: maxD, rng: rng,
		}
		go func() {
			if err := sims[i].run(ctx, nodeURL, board, uint64(5000+i)); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "client %d: %v\n", i, err)
			}
		}()
	}
	time.Sleep(5 * time.Second) // connect and gather a few pings

	var spreads []float64
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		<-tick.C
		lo, hi, got := 0.0, 0.0, 0
		for _, s := range sims {
			r, ok := s.remaining()
			if !ok {
				continue
			}
			if got == 0 || r < lo {
				lo = r
			}
			if got == 0 || r > hi {
				hi = r
			}
			got++
		}
		if got == n {
			spreads = append(spreads, hi-lo)
		}
	}
	if len(spreads) == 0 {
		return fmt.Errorf("no sample had all %d clients with the timer", n)
	}
	slices.Sort(spreads)
	q := func(p float64) float64 { return spreads[min(len(spreads)-1, int(p*float64(len(spreads))))] }
	fmt.Printf("%d clients, clocks off by up to ±%v, %v–%v added delay each way; %d samples every 100 ms\n",
		n, maxOffset, minD, maxD, len(spreads))
	fmt.Printf("spread of displayed remaining time (max − min across clients): p50 %.1f ms, p99 %.1f ms, max %.1f ms\n",
		q(0.50), q(0.99), spreads[len(spreads)-1])
	return nil
}

func (s *sim) run(ctx context.Context, url, board string, id uint64) error {
	ws, _, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose // the websocket library owns the handshake response body
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 20)
	var wmu sync.Mutex
	send := func(m *pb.ClientMessage) error {
		data, err := proto.Marshal(m)
		if err != nil {
			return err
		}
		time.Sleep(s.delay()) // the trip to the server
		wmu.Lock()
		defer wmu.Unlock()
		return ws.Write(ctx, websocket.MessageBinary, data)
	}
	// A tiny far-away viewport: the timer is board-wide, so it still arrives.
	if err := send(&pb.ClientMessage{Msg: &pb.ClientMessage_Hello{Hello: &pb.Hello{
		ProtocolVersion: protocol.Version, BoardId: board, ClientId: id,
		Viewport: &pb.Viewport{X: -9e8, Y: -9e8, W: 1, H: 1},
	}}}); err != nil {
		return err
	}
	go func() {
		for ctx.Err() == nil {
			_ = send(&pb.ClientMessage{Msg: &pb.ClientMessage_TimePing{TimePing: &pb.TimePing{T0: s.local()}}})
			time.Sleep(500 * time.Millisecond)
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		var m pb.ServerMessage
		if proto.Unmarshal(data, &m) != nil {
			continue
		}
		go s.receive(&m) // each message arrives after its own delay
	}
}

func (s *sim) receive(m *pb.ServerMessage) {
	time.Sleep(s.delay()) // the trip back
	now := s.local()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch msg := m.Msg.(type) {
	case *pb.ServerMessage_Welcome:
		s.seed, s.seeded = float64(msg.Welcome.GetServerTimeMs())-now, true
		for _, o := range msg.Welcome.GetObjects() {
			if o.GetId() == protocol.TimerID {
				s.endsAt = float64(o.GetProps().GetEndsAtMs())
			}
		}
	case *pb.ServerMessage_TimePong:
		rtt := now - msg.TimePong.GetT0()
		s.samples = append(s.samples, sample{rtt: rtt, offset: float64(msg.TimePong.GetServerTimeMs()) + rtt/2 - now})
		if len(s.samples) > 8 {
			s.samples = s.samples[1:]
		}
	}
}

// route returns the WebSocket URL of the node serving the board.
func route(ctx context.Context, wsURL, board string) (string, error) {
	base := strings.TrimSuffix(strings.Replace(wsURL, "ws", "http", 1), "/ws")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/boards/"+board+"/route", http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var r struct {
		NodeID string `json:"nodeId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.NodeID == "" {
		return wsURL, nil //nolint:nilerr // a single node without the route API: use the URL as given
	}
	return strings.TrimSuffix(wsURL, "/ws") + "/n/" + r.NodeID + "/ws", nil
}
