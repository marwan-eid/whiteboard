package board

import (
	"sort"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"whiteboard/internal/doc"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/spatial"
)

// Viewport interest (docs/ARCHITECTURE.md, "Viewport interest").
//
// Each client holds exactly the visible objects whose bounding box intersects
// its viewport. For every op the board knows whether the object was in a
// client's view before and after it:
//   - in view before: the client has it, so it gets the op (a delta);
//   - only in view after: the client lacks it, so it gets the full object;
//   - neither: nothing.
// The client applies deltas only to objects it holds and evicts objects that
// leave its view, using the same rule, so both sides agree without the server
// tracking per-client object sets.

// maxCursorsPerClient bounds presence fan-out: each client sees the nearest
// cursors in its view, plus the total online count.
const maxCursorsPerClient = 30

// lodMask selects the properties sent to zoomed-out (LOD) clients: enough to
// draw a box. Bit n is ObjectProps field number n.
const lodMask uint32 = 1<<1 | 1<<2 | 1<<3 | 1<<4 | 1<<5 | 1<<6 | 1<<7 | 1<<8

type clientState struct {
	conn Conn
	view spatial.Rect
	lod  bool
	// Objects entering (true: send in full) or leaving (false: tell the
	// client to drop) its view since the last frame; the last change wins.
	moves map[string]bool
	// Cursors this client is currently shown.
	shownCursors map[uint64]struct{}
	// presenceStale forces a cursor recompute on the next frame (after joining
	// or moving the viewport).
	presenceStale bool
	// Ops of this tick's batches already judged against this client's view,
	// and how many of b.uncommitted that covers. A viewport change judges the
	// batches before it against the old view, so none is judged by a view
	// the client did not have when the op happened.
	refs          []opRef
	evaluatedUpTo int
}

// opRef is an op a client gets as a delta: b.uncommitted[batch].Ops[op].
type opRef struct {
	batch, op int
	lod       bool
}

// judge decides, for the batches the client has not been judged on yet, which
// ops it gets as deltas and which objects enter or leave its view.
func (b *Board) judge(id uint64, c *clientState) {
	for i := c.evaluatedUpTo; i < len(b.uncommitted); i++ {
		p := &b.uncommitted[i]
		for j, op := range p.entry.Ops {
			heldBefore := p.before[j].ok && p.before[j].r.Intersects(c.view)
			heldAfter := p.after[j].ok && p.after[j].r.Intersects(c.view)
			switch {
			case heldBefore && !heldAfter:
				c.moves[op.GetId()] = false
			case !heldBefore && heldAfter:
				// Includes the sender's own ops, e.g. undoing a delete.
				c.moves[op.GetId()] = true
			case heldBefore && p.entry.ClientID != id:
				// The sender applied its own op already.
				c.refs = append(c.refs, opRef{batch: i, op: j, lod: c.lod})
			}
		}
	}
	c.evaluatedUpTo = len(b.uncommitted)
}

// bounds is an object's box for interest purposes; ok is false when the
// object is not visible (not created, or deleted).
func bounds(o *doc.Object) (spatial.Rect, bool) {
	if o == nil || !o.Visible() {
		return spatial.Rect{}, false
	}
	p := o.Props
	return spatial.Rect{X: p.GetX(), Y: p.GetY(), W: p.GetW(), H: p.GetH()}, true
}

func viewRect(v *pb.Viewport) (spatial.Rect, bool) {
	if v == nil {
		return spatial.Everything, false
	}
	return spatial.Rect{X: v.GetX(), Y: v.GetY(), W: v.GetW(), H: v.GetH()}, v.GetLod()
}

// objectsIn returns the full (or LOD) state of every visible object in r, sorted by id.
func (b *Board) objectsIn(r spatial.Rect, lod bool) []*pb.ObjectState {
	var ids []string
	b.grid.Query(r, func(id string, _ spatial.Rect) bool {
		ids = append(ids, id)
		return true
	})
	sort.Strings(ids)
	out := make([]*pb.ObjectState, 0, len(ids))
	for _, id := range ids {
		out = append(out, b.objectState(id, lod))
	}
	return out
}

func (b *Board) objectState(id string, lod bool) *pb.ObjectState {
	s := b.doc.Get(id).State()
	if lod {
		filterState(s, lodMask)
	}
	return s
}

// setViewport records a client's new viewport and queues the objects that
// entered and left it.
func (b *Board) setViewport(id uint64, c *clientState, v *pb.Viewport) {
	b.judge(id, c) // ops so far were made under the old view
	old, oldLOD := c.view, c.lod
	c.view, c.lod = viewRect(v)
	c.presenceStale = true
	b.grid.Query(old, func(id string, r spatial.Rect) bool {
		if !r.Intersects(c.view) {
			c.moves[id] = false
		}
		return true
	})
	b.grid.Query(c.view, func(id string, r spatial.Rect) bool {
		// Leaving LOD, the client holds only partial objects: resend all in view.
		if !r.Intersects(old) || (oldLOD && !c.lod) {
			c.moves[id] = true
		}
		return true
	})
}

// filterState keeps only the properties (and stamps) in mask.
func filterState(s *pb.ObjectState, mask uint32) {
	filterProps(s.Props, mask)
	kept := s.Stamps[:0]
	for _, fs := range s.Stamps {
		if fs.FieldMask &= mask; fs.FieldMask != 0 {
			kept = append(kept, fs)
		}
	}
	s.Stamps = kept
}

func filterProps(p *pb.ObjectProps, mask uint32) {
	m := p.ProtoReflect()
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if mask&(1<<fd.Number()) == 0 {
			m.Clear(fd)
		}
		return true
	})
}

// lodOp returns op limited to LOD properties, or nil if none remain.
func lodOp(op *pb.Op) *pb.Op {
	props := proto.CloneOf(op.GetProps())
	filterProps(props, lodMask)
	if proto.Size(props) == 0 {
		return nil
	}
	return &pb.Op{Id: op.GetId(), Props: props}
}
