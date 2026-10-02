package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	pb "whiteboard/internal/pb/whiteboard/v1"
)

// A server that welcomes the client and then freezes (a paused process: the
// TCP connection stays open, nothing arrives). With Liveness set, the client
// gives up on it and redials.
func TestLivenessNoticesAFrozenServer(t *testing.T) {
	var accepted atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		accepted.Add(1)
		if _, _, err := ws.Read(r.Context()); err != nil { // the Hello
			return
		}
		data, _ := proto.Marshal(&pb.ServerMessage{Msg: &pb.ServerMessage_Welcome{Welcome: &pb.Welcome{}}})
		_ = ws.Write(r.Context(), websocket.MessageBinary, data)
		<-r.Context().Done() // frozen: never reads or writes again
	}))
	defer srv.Close()

	c := New(Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), BoardID: "b", Reconnect: true, Liveness: time.Second})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for accepted.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("client never gave up on the frozen server (%d connections)", accepted.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
