package board

import (
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"pgregory.net/rapid"

	pb "whiteboard/internal/pb/whiteboard/v1"
)

// Frames assembled from pre-encoded pieces decode to exactly the message
// the long way (build the proto, marshal) would produce.
func TestFrameBuilderMatchesMarshal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := &pb.Frame{}
		fb := newFrameBuilder(nil)
		for i := range rapid.IntRange(0, 4).Draw(t, "batches") {
			e := LogEntry{
				Seq:   uint64(i + 1),
				Stamp: &pb.Stamp{WallMs: rapid.Int64Range(0, 1e13).Draw(t, "wall"), ClientId: rapid.Uint64Range(1, 1<<53).Draw(t, "client")},
			}
			for j := range rapid.IntRange(1, 4).Draw(t, "ops") {
				e.Ops = append(e.Ops, &pb.Op{Id: fmt.Sprintf("a:%d", j), Props: &pb.ObjectProps{
					X:    proto.Float64(float64(rapid.IntRange(-1000, 1000).Draw(t, "x"))),
					Text: proto.String(rapid.StringMatching(`[a-z]{0,5}`).Draw(t, "text")),
				}})
			}
			eb := encodeBatch(e, nil, nil)
			// Send a random subset of the ops.
			sb := &pb.SequencedBatch{Seq: e.Seq, Stamp: e.Stamp}
			var sel [][]byte
			for j, op := range e.Ops {
				if rapid.Bool().Draw(t, "include") {
					sel = append(sel, eb.ops[j])
					sb.Ops = append(sb.Ops, op)
				}
			}
			if len(sel) > 0 {
				fb.batch(eb.header, sel)
				want.Batches = append(want.Batches, sb)
			}
		}
		if rapid.Bool().Draw(t, "ack") {
			a := &pb.Ack{ClientSeq: 3, Seq: 7, Stamp: &pb.Stamp{WallMs: 5}}
			fb.message(fieldFrameAcks, a)
			want.Acks = append(want.Acks, a)
		}
		if rapid.Bool().Draw(t, "cursor") {
			u := &pb.CursorUpdate{ClientId: 9, X: 1.5, Y: -2}
			fb.message(fieldFrameCursors, u)
			want.Cursors = append(want.Cursors, u)
		}
		if rapid.Bool().Draw(t, "object") {
			s := &pb.ObjectState{Id: "b:1", Props: &pb.ObjectProps{W: proto.Float64(3)}, Stamps: []*pb.FieldStamps{{Stamp: &pb.Stamp{WallMs: 1}, FieldMask: 32}}}
			fb.raw(appendMessage(nil, fieldFrameObjects, s))
			want.Objects = append(want.Objects, s)
		}
		if rapid.Bool().Draw(t, "leave") {
			fb.leave("c:2")
			want.Leave = append(want.Leave, "c:2")
		}
		if rapid.Bool().Draw(t, "online") {
			fb.online(4)
			want.Online = proto.Uint32(4)
		}
		fb.seq(42)
		want.Seq = 42

		var got pb.ServerMessage
		if err := proto.Unmarshal(fb.serverMessage(), &got); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(got.GetFrame(), want) {
			t.Fatalf("assembled frame\n got  %v\n want %v", got.GetFrame(), want)
		}
	})
}

func rectAt(id string, x, y float64) *pb.Op {
	return &pb.Op{Id: id, Props: &pb.ObjectProps{
		Type: pb.ShapeType_SHAPE_TYPE_RECT.Enum(), X: proto.Float64(x), Y: proto.Float64(y),
		W: proto.Float64(10), H: proto.Float64(10), Text: proto.String("detail"),
	}}
}

func ids(states []*pb.ObjectState) []string {
	var out []string
	for _, s := range states {
		out = append(out, s.GetId())
	}
	return out
}

func TestViewportLimitsWhatClientsReceive(t *testing.T) {
	e := newEnv(t)
	editor := newConn(10)
	bd, _ := e.join(t, "x", editor)
	submit(t, bd, 10, batch(1, now.UnixMilli(), rectAt("a:left", 0, 0), rectAt("a:right", 1000, 0)))
	editor.frame(t)

	// A viewer of the left side gets only the left object.
	viewer := newConn(11)
	if _, err := e.reg.Join(t.Context(), "x", viewer, &pb.Viewport{X: -50, Y: -50, W: 200, H: 200}); err != nil {
		t.Fatal(err)
	}
	if w := viewer.next(t).GetWelcome(); fmt.Sprint(ids(w.GetObjects())) != "[a:left]" {
		t.Fatalf("welcome objects = %v", ids(w.GetObjects()))
	}

	// Edits on the right are not sent; moving right->left arrives in full;
	// moving left->right is a leave.
	submit(t, bd, 10, batch(2, now.UnixMilli()+1, &pb.Op{Id: "a:right", Props: &pb.ObjectProps{Fill: proto.Uint32(7)}}))
	viewer.quiet(t)
	submit(t, bd, 10, batch(3, now.UnixMilli()+2, &pb.Op{Id: "a:right", Props: &pb.ObjectProps{X: proto.Float64(20)}}))
	if f := viewer.frame(t); fmt.Sprint(ids(f.Objects)) != "[a:right]" || f.Objects[0].GetProps().GetFill() != 7 || len(f.Batches) != 0 {
		t.Fatalf("entering object frame = %v", f)
	}
	submit(t, bd, 10, batch(4, now.UnixMilli()+3, &pb.Op{Id: "a:left", Props: &pb.ObjectProps{X: proto.Float64(900)}}))
	if f := viewer.frame(t); fmt.Sprint(f.Leave) != "[a:left]" || len(f.Batches) != 0 {
		t.Fatalf("leaving object frame = %v", f)
	}

	// Moving the viewport: what is now in view arrives, what is not leaves.
	if err := bd.SetViewport(t.Context(), viewer, &pb.Viewport{X: 850, Y: -50, W: 200, H: 200}); err != nil {
		t.Fatal(err)
	}
	if f := viewer.frame(t); fmt.Sprint(ids(f.Objects)) != "[a:left]" || fmt.Sprint(f.Leave) != "[a:right]" {
		t.Fatalf("viewport change frame = %v", f)
	}
}

func TestLODClientsGetBoxesOnly(t *testing.T) {
	e := newEnv(t)
	editor := newConn(10)
	bd, _ := e.join(t, "x", editor)
	viewer := newConn(11)
	if _, err := e.reg.Join(t.Context(), "x", viewer, &pb.Viewport{X: -1e6, Y: -1e6, W: 2e6, H: 2e6, Lod: true}); err != nil {
		t.Fatal(err)
	}
	viewer.next(t)

	submit(t, bd, 10, batch(1, now.UnixMilli(), rectAt("a:1", 0, 0)))
	f := viewer.frame(t)
	if len(f.Objects) != 1 || f.Objects[0].GetProps().Text != nil || f.Objects[0].GetProps().GetW() != 10 {
		t.Fatalf("LOD object = %v, want box fields without text", f.Objects)
	}
	for _, fs := range f.Objects[0].GetStamps() {
		if fs.GetFieldMask()&^lodMask != 0 {
			t.Fatalf("stamp mask %b includes non-LOD fields", fs.GetFieldMask())
		}
	}
	// A text-only edit is not sent to LOD clients; a move is.
	submit(t, bd, 10, batch(2, now.UnixMilli()+1, &pb.Op{Id: "a:1", Props: &pb.ObjectProps{Text: proto.String("x")}}))
	viewer.quiet(t)
	submit(t, bd, 10, batch(3, now.UnixMilli()+2, &pb.Op{Id: "a:1", Props: &pb.ObjectProps{X: proto.Float64(5), Text: proto.String("y")}}))
	if f := viewer.frame(t); len(f.Batches) != 1 || f.Batches[0].GetOps()[0].GetProps().Text != nil || f.Batches[0].GetOps()[0].GetProps().GetX() != 5 {
		t.Fatalf("LOD delta = %v", f)
	}

	// Leaving LOD resends full objects.
	if err := bd.SetViewport(t.Context(), viewer, &pb.Viewport{X: -1e6, Y: -1e6, W: 2e6, H: 2e6}); err != nil {
		t.Fatal(err)
	}
	if f := viewer.frame(t); len(f.Objects) != 1 || f.Objects[0].GetProps().GetText() != "y" {
		t.Fatalf("full resend = %v", f)
	}
}

func TestEachClientSeesOnlyNearestCursors(t *testing.T) {
	e := newEnv(t)
	viewer := newConn(1)
	bd, err := e.reg.Join(t.Context(), "x", viewer, &pb.Viewport{X: 0, Y: 0, W: 1000, H: 1000})
	if err != nil {
		t.Fatal(err)
	}
	viewer.next(t)
	for i := range 50 {
		c := newConn(uint64(100 + i))
		e.join(t, "x", c)
		bd.SetCursor(uint64(100+i), 500+float64(i)*5, 500) // distance grows with i
	}
	bd.SetCursor(999, 5000, 5000) // not on the board: ignored
	var shown map[uint64]bool
	deadline := time.Now().Add(3 * time.Second)
	for len(shown) < maxCursorsPerClient && time.Now().Before(deadline) {
		f := viewer.frame(t)
		if shown == nil {
			shown = map[uint64]bool{}
		}
		for _, u := range f.Cursors {
			if u.GetGone() {
				delete(shown, u.GetClientId())
			} else {
				shown[u.GetClientId()] = true
			}
		}
	}
	if len(shown) != maxCursorsPerClient {
		t.Fatalf("viewer shown %d cursors, want %d", len(shown), maxCursorsPerClient)
	}
	for id := range shown {
		if id >= 100+maxCursorsPerClient {
			t.Fatalf("cursor %d shown, but it is not among the nearest", id)
		}
	}
}
