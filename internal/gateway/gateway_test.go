package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/board"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

var fixedNow = time.UnixMilli(1_700_000_000_000)

type harness struct {
	gw      *Gateway
	metrics *metrics.Metrics
	url     string
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	now := func() time.Time { return fixedNow }
	cfg.Now = now
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	boards := board.NewRegistry(board.Config{NodeID: "node-test", Store: board.NewMemoryStore(), Tick: 5 * time.Millisecond, Now: now}, log, m)
	gw := New(cfg, boards, log, m)
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	return &harness{gw: gw, metrics: m, url: "ws" + strings.TrimPrefix(srv.URL, "http")}
}

func (h *harness) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, h.url, nil) //nolint:bodyclose // the websocket library owns the handshake response body
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(64 << 20)
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, msg *pb.ClientMessage) {
	t.Helper()
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func recv(t *testing.T, c *websocket.Conn) *pb.ServerMessage {
	t.Helper()
	msg, err := tryRecv(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return msg
}

func tryRecv(c *websocket.Conn) (*pb.ServerMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var msg pb.ServerMessage
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

var nextClientID atomic.Uint64

func hello(version uint32, board string) *pb.ClientMessage {
	return helloAs(version, board, 1000+nextClientID.Add(1))
}

func helloAs(version uint32, board string, clientID uint64) *pb.ClientMessage {
	return &pb.ClientMessage{Msg: &pb.ClientMessage_Hello{Hello: &pb.Hello{
		ProtocolVersion: version, BoardId: board, ClientId: clientID,
	}}}
}

// recvFrame returns the next frame, skipping ones that only update the online count.
func recvFrame(t *testing.T, c *websocket.Conn) *pb.Frame {
	t.Helper()
	for {
		f := recv(t, c).GetFrame()
		if f == nil {
			t.Fatal("expected a frame")
		}
		if len(f.Batches)+len(f.Acks)+len(f.Cursors)+len(f.Objects)+len(f.Leave) > 0 {
			return f
		}
	}
}

// join handshakes and returns the Welcome.
func join(t *testing.T, c *websocket.Conn, msg *pb.ClientMessage) *pb.Welcome {
	t.Helper()
	send(t, c, msg)
	w := recv(t, c).GetWelcome()
	if w == nil {
		t.Fatal("expected welcome")
	}
	return w
}

// expectRejected asserts an Error with code arrives, followed by a policy-violation close.
func expectRejected(t *testing.T, c *websocket.Conn, code pb.ErrorCode) {
	t.Helper()
	msg := recv(t, c)
	if got := msg.GetError().GetCode(); got != code {
		t.Fatalf("error code = %v, want %v (msg %v)", got, code, msg)
	}
	_, err := tryRecv(c)
	if status := websocket.CloseStatus(err); status != websocket.StatusPolicyViolation {
		t.Fatalf("close status = %v (err %v), want PolicyViolation", status, err)
	}
}

func TestHandshakeAndTimePing(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.dial(t)

	w := join(t, c, hello(protocol.Version, "demo"))
	if w.GetNodeId() != "node-test" || w.GetProtocolVersion() != protocol.Version || w.GetServerTimeMs() != fixedNow.UnixMilli() {
		t.Fatalf("unexpected welcome: %v", w)
	}
	if w.GetSeq() != 0 || len(w.GetObjects()) != 0 {
		t.Fatalf("new board should be empty: %v", w)
	}

	for _, t0 := range []float64{0, 12.5, 99999.75} {
		send(t, c, &pb.ClientMessage{Msg: &pb.ClientMessage_TimePing{TimePing: &pb.TimePing{T0: t0}}})
		p := recv(t, c).GetTimePong()
		if p == nil || p.GetT0() != t0 || p.GetServerTimeMs() != fixedNow.UnixMilli() {
			t.Fatalf("unexpected pong for t0=%v: %v", t0, p)
		}
	}

	if got := testutil.ToFloat64(h.metrics.WSMessagesIn.WithLabelValues("time_ping")); got != 3 {
		t.Fatalf("time_ping counter = %v, want 3", got)
	}
}

func TestBatchRoundTrip(t *testing.T) {
	h := newHarness(t, Config{})
	a, b := h.dial(t), h.dial(t)
	join(t, a, helloAs(protocol.Version, "demo", 10)) // base 36: "a"
	join(t, b, helloAs(protocol.Version, "demo", 11))

	op := &pb.Op{Id: "a:1", Props: &pb.ObjectProps{Type: pb.ShapeType_SHAPE_TYPE_RECT.Enum(), X: proto.Float64(5)}}
	send(t, a, &pb.ClientMessage{Msg: &pb.ClientMessage_OpBatch{OpBatch: &pb.OpBatch{
		ClientSeq: 1,
		Stamp:     &pb.Stamp{WallMs: fixedNow.UnixMilli()},
		Ops:       []*pb.Op{op},
	}}})

	ack := recvFrame(t, a)
	if len(ack.GetAcks()) != 1 || ack.GetAcks()[0].GetSeq() != 1 || len(ack.GetBatches()) != 0 {
		t.Fatalf("sender frame = %v, want one ack for seq 1 and no batches", ack)
	}
	// The new object reaches the other client in full, with its stamps.
	got := recvFrame(t, b)
	if len(got.GetObjects()) != 1 || got.GetSeq() != 1 {
		t.Fatalf("receiver frame = %v, want the new object", got)
	}
	obj := got.GetObjects()[0]
	if obj.GetId() != "a:1" || !proto.Equal(obj.GetProps(), op.GetProps()) || obj.GetStamps()[0].GetStamp().GetClientId() != 10 {
		t.Fatalf("object changed in transit: %v", obj)
	}
}

func TestRejections(t *testing.T) {
	cases := map[string]struct {
		first *pb.ClientMessage
		raw   []byte
		code  pb.ErrorCode
	}{
		"wrong version":     {first: hello(protocol.Version+1, "demo"), code: pb.ErrorCode_ERROR_CODE_UNSUPPORTED_VERSION},
		"bad board id":      {first: hello(protocol.Version, "../etc"), code: pb.ErrorCode_ERROR_CODE_BAD_REQUEST},
		"empty board":       {first: hello(protocol.Version, ""), code: pb.ErrorCode_ERROR_CODE_BAD_REQUEST},
		"zero client id":    {first: helloAs(protocol.Version, "demo", 0), code: pb.ErrorCode_ERROR_CODE_BAD_REQUEST},
		"client id too big": {first: helloAs(protocol.Version, "demo", protocol.MaxClientID+1), code: pb.ErrorCode_ERROR_CODE_BAD_REQUEST},
		"ping first": {
			first: &pb.ClientMessage{Msg: &pb.ClientMessage_TimePing{TimePing: &pb.TimePing{}}},
			code:  pb.ErrorCode_ERROR_CODE_BAD_REQUEST,
		},
		"garbage bytes": {raw: []byte{0xff, 0xff, 0xff}, code: pb.ErrorCode_ERROR_CODE_BAD_REQUEST},
		"empty message": {first: &pb.ClientMessage{}, code: pb.ErrorCode_ERROR_CODE_BAD_REQUEST},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, Config{})
			c := h.dial(t)
			if tc.raw != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := c.Write(ctx, websocket.MessageBinary, tc.raw); err != nil {
					t.Fatal(err)
				}
			} else {
				send(t, c, tc.first)
			}
			expectRejected(t, c, tc.code)
		})
	}
}

func TestSameClientIDReplacesOlderConnection(t *testing.T) {
	h := newHarness(t, Config{})
	first := h.dial(t)
	join(t, first, helloAs(protocol.Version, "demo", 42))

	// A reconnect that beats the server noticing the old socket died.
	second := h.dial(t)
	join(t, second, helloAs(protocol.Version, "demo", 42))
	expectRejected(t, first, pb.ErrorCode_ERROR_CODE_CLIENT_ID_IN_USE)

	// The same id on another board is independent.
	join(t, h.dial(t), helloAs(protocol.Version, "other", 42))
}

func TestDuplicateHelloRejected(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.dial(t)
	join(t, c, hello(protocol.Version, "demo"))
	send(t, c, hello(protocol.Version, "demo"))
	expectRejected(t, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST)
}

func TestTextFrameRejected(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"hello":{}}`)); err != nil {
		t.Fatal(err)
	}
	expectRejected(t, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST)
}

func TestHelloTimeoutClosesConnection(t *testing.T) {
	h := newHarness(t, Config{HelloTimeout: 100 * time.Millisecond})
	c := h.dial(t)
	start := time.Now()
	if _, err := tryRecv(c); err == nil {
		t.Fatal("expected connection to close")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("connection closed after %v, want ~100ms", elapsed)
	}
}

func TestShutdownClosesWithGoingAway(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.dial(t)
	join(t, c, hello(protocol.Version, "demo"))

	h.gw.Shutdown()
	_, err := tryRecv(c)
	if status := websocket.CloseStatus(err); status != websocket.StatusGoingAway {
		t.Fatalf("close status = %v (err %v), want GoingAway", status, err)
	}

	// Connections arriving after shutdown are turned away the same way.
	late := h.dial(t)
	_, err = tryRecv(late)
	if status := websocket.CloseStatus(err); status != websocket.StatusGoingAway {
		t.Fatalf("late conn close status = %v (err %v), want GoingAway", status, err)
	}
}

func TestSlowConsumerIsKicked(t *testing.T) {
	h := newHarness(t, Config{SendQueue: 1})
	slow := h.dial(t)
	join(t, slow, helloAs(protocol.Version, "demo", 20))
	fast := h.dial(t)
	join(t, fast, helloAs(protocol.Version, "demo", 21)) // base 36: "l"

	// The fast client drains its acks; the slow one never reads.
	fastClosed := make(chan error, 1)
	go func() {
		for {
			if _, err := tryRecv(fast); err != nil {
				fastClosed <- err
				return
			}
		}
	}()

	// Flood large edits until the slow client's socket and queue fill up.
	kicked := func() bool { return testutil.ToFloat64(h.metrics.ClientsKicked.WithLabelValues("slow_consumer")) > 0 }
	big := strings.Repeat("x", protocol.MaxTextBytes)
	for i := uint64(1); i <= 5000 && !kicked(); i++ {
		props := &pb.ObjectProps{X: proto.Float64(float64(i)), Text: proto.String(big)}
		if i == 1 {
			props.Type = pb.ShapeType_SHAPE_TYPE_RECT.Enum()
		}
		send(t, fast, &pb.ClientMessage{Msg: &pb.ClientMessage_OpBatch{OpBatch: &pb.OpBatch{
			ClientSeq: i,
			Stamp:     &pb.Stamp{WallMs: fixedNow.UnixMilli(), Counter: uint32(i)},
			Ops:       []*pb.Op{{Id: "l:1", Props: props}},
		}}})
	}
	waitFor(t, kicked)

	// Only the slow client was dropped.
	select {
	case err := <-fastClosed:
		t.Fatalf("fast client was disconnected too: %v", err)
	default:
	}
	_, err := tryRecv(slow)
	for err == nil {
		_, err = tryRecv(slow) // drain what was queued before the kick
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusTryAgainLater {
		t.Fatalf("slow client close status = %v (err %v), want TryAgainLater", status, err)
	}
}

func TestConnectionGaugeTracksOpenConnections(t *testing.T) {
	h := newHarness(t, Config{})
	conns := make([]*websocket.Conn, 3)
	for i := range conns {
		conns[i] = h.dial(t)
		join(t, conns[i], hello(protocol.Version, "demo"))
	}
	waitFor(t, func() bool { return testutil.ToFloat64(h.metrics.WSConnections) == 3 })

	for _, c := range conns {
		_ = c.Close(websocket.StatusNormalClosure, "")
	}
	waitFor(t, func() bool { return testutil.ToFloat64(h.metrics.WSConnections) == 0 })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(errors.New("condition not met within 5s"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
