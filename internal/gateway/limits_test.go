package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
	"whiteboard/internal/ratelimit"
)

func TestConnectionsPerIPAreCapped(t *testing.T) {
	h := newHarness(t, Config{ConnsPerIP: ratelimit.NewCounter(2)})
	a := h.dial(t)
	join(t, a, hello(protocol.Version, "demo"))
	join(t, h.dial(t), hello(protocol.Version, "demo"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, h.url, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third connection: err %v, resp %v; want 429", err, resp)
	}
	_ = resp.Body.Close()

	// Closing one frees its slot.
	_ = a.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, resp, err := websocket.Dial(ctx, h.url, nil)
		if err == nil {
			c.CloseNow()
			break
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not released: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFastSendersAreSlowedNotDropped(t *testing.T) {
	h := newHarness(t, Config{BatchesPerSec: 20}) // a burst of 40
	c := h.dial(t)
	join(t, c, helloAs(protocol.Version, "demo", 10))

	const n = 60
	start := time.Now()
	for i := range n {
		props := &pb.ObjectProps{X: proto.Float64(float64(i))}
		if i == 0 {
			props.Type = pb.ShapeType_SHAPE_TYPE_RECT.Enum()
		}
		send(t, c, &pb.ClientMessage{Msg: &pb.ClientMessage_OpBatch{OpBatch: &pb.OpBatch{
			ClientSeq: uint64(i + 1),
			Stamp:     &pb.Stamp{WallMs: fixedNow.UnixMilli()},
			Ops:       []*pb.Op{{Id: "a:1", Props: props}},
		}}})
	}
	acked := 0
	for acked < n {
		for _, a := range recvFrame(t, c).GetAcks() {
			if a.GetRejected() {
				t.Fatalf("batch rejected: %v", a)
			}
			acked++
		}
	}
	// 20 batches over the burst at 20/s take about a second.
	if d := time.Since(start); d < 800*time.Millisecond {
		t.Fatalf("60 batches took %v; the limit did not apply", d)
	}
	if got := testutil.ToFloat64(h.metrics.Throttled.WithLabelValues("batches")); got == 0 {
		t.Fatal("throttling not counted")
	}
}
