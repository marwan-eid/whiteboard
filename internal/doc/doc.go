// Package doc is the board's replicated data model: a map of objects whose
// properties are last-writer-wins registers ordered by hlc.Stamp.
//
// Merge is commutative, associative and idempotent, so any two replicas that
// have applied the same set of batches, in any order and with duplicates,
// hold identical documents. The web client implements the same rules in
// web/src/sync/doc.ts; testdata/lww-vectors.json keeps the two in agreement.
package doc

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"whiteboard/internal/hlc"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

// fieldSlots bounds ObjectProps field numbers; FieldStamps masks are 32-bit.
const fieldSlots = 32

var propFields = (&pb.ObjectProps{}).ProtoReflect().Descriptor().Fields()

func init() {
	for i := range propFields.Len() {
		if n := propFields.Get(i).Number(); n >= fieldSlots {
			panic(fmt.Sprintf("ObjectProps field %d does not fit a 32-bit FieldStamps mask", n))
		}
	}
}

type Object struct {
	ID    string
	Props *pb.ObjectProps
	// stamps[n] is the stamp of the write that set field number n.
	stamps [fieldSlots]hlc.Stamp
}

// Created reports whether the object's creating op (the one that sets type) has been applied.
func (o *Object) Created() bool { return o.Props.Type != nil }

// Visible reports whether the object should be rendered.
func (o *Object) Visible() bool { return o.Created() && !o.Props.GetDeleted() }

// merge applies each present property whose stamp is newer than the stored one.
func (o *Object) merge(props *pb.ObjectProps, st hlc.Stamp) (changed bool) {
	dst := o.Props.ProtoReflect()
	props.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		n := fd.Number()
		if st.Compare(o.stamps[n]) <= 0 {
			return true // older, or this very write again
		}
		o.stamps[n] = st
		dst.Set(fd, v)
		changed = true
		return true
	})
	return changed
}

// State returns the object with its per-field stamps, grouped by stamp in
// ascending order (the canonical form shared with the web client).
func (o *Object) State() *pb.ObjectState {
	masks := map[hlc.Stamp]uint32{}
	for n := 1; n < fieldSlots; n++ {
		if st := o.stamps[n]; !st.IsZero() {
			masks[st] |= 1 << n
		}
	}
	stamps := make([]hlc.Stamp, 0, len(masks))
	for st := range masks {
		stamps = append(stamps, st)
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].Compare(stamps[j]) < 0 })

	s := &pb.ObjectState{Id: o.ID, Props: proto.CloneOf(o.Props)}
	for _, st := range stamps {
		s.Stamps = append(s.Stamps, &pb.FieldStamps{Stamp: st.Proto(), FieldMask: masks[st]})
	}
	return s
}

type Doc struct {
	objects map[string]*Object
}

func New() *Doc {
	return &Doc{objects: map[string]*Object{}}
}

func (d *Doc) Get(id string) *Object { return d.objects[id] }

func (d *Doc) Len() int { return len(d.objects) }

// Apply merges one op written at stamp st and reports whether anything changed.
// An op on an unknown id creates the object, even without a type: replicas
// may see an edit before its create, and must still converge.
func (d *Doc) Apply(op *pb.Op, st hlc.Stamp) bool {
	props := op.GetProps()
	if props == nil || isEmpty(props) {
		return false
	}
	o := d.objects[op.GetId()]
	if o == nil {
		o = &Object{ID: op.GetId(), Props: &pb.ObjectProps{}}
		d.objects[o.ID] = o
	}
	return o.merge(props, st)
}

// ApplyBatch applies every op with the batch stamp.
func (d *Doc) ApplyBatch(ops []*pb.Op, st hlc.Stamp) (changed bool) {
	for _, op := range ops {
		if d.Apply(op, st) {
			changed = true
		}
	}
	return changed
}

// MergeState merges an object's state (as from Object.State, possibly
// limited to some fields) using its per-field stamps, as if each write had
// been applied as an op. It reports whether anything changed.
func (d *Doc) MergeState(s *pb.ObjectState) (changed bool) {
	for _, fs := range s.GetStamps() {
		props := proto.CloneOf(s.GetProps())
		if props == nil {
			return changed
		}
		m := props.ProtoReflect()
		m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
			if fs.GetFieldMask()&(1<<fd.Number()) == 0 {
				m.Clear(fd)
			}
			return true
		})
		if d.Apply(&pb.Op{Id: s.GetId(), Props: props}, hlc.FromProto(fs.GetStamp())) {
			changed = true
		}
	}
	return changed
}

// Delete forgets an object entirely (a client evicting what left its view).
func (d *Doc) Delete(id string) { delete(d.objects, id) }

// Range calls fn for every object until fn returns false.
func (d *Doc) Range(fn func(*Object) bool) {
	for _, o := range d.objects {
		if !fn(o) {
			return
		}
	}
}

// Snapshot returns every object in canonical form, sorted by id.
func (d *Doc) Snapshot() []*pb.ObjectState {
	ids := make([]string, 0, len(d.objects))
	for id := range d.objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*pb.ObjectState, len(ids))
	for i, id := range ids {
		out[i] = d.objects[id].State()
	}
	return out
}

// FromSnapshot rebuilds a document from Snapshot output.
func FromSnapshot(states []*pb.ObjectState) (*Doc, error) {
	d := New()
	for _, s := range states {
		o := &Object{ID: s.GetId(), Props: proto.CloneOf(s.GetProps())}
		if o.Props == nil {
			o.Props = &pb.ObjectProps{}
		}
		if _, dup := d.objects[o.ID]; dup {
			return nil, fmt.Errorf("duplicate object %q in snapshot", o.ID)
		}
		for _, fs := range s.GetStamps() {
			st := hlc.FromProto(fs.GetStamp())
			for n := 1; n < fieldSlots; n++ {
				if fs.GetFieldMask()&(1<<n) != 0 {
					o.stamps[n] = st
				}
			}
		}
		for i := range propFields.Len() {
			fd := propFields.Get(i)
			if o.Props.ProtoReflect().Has(fd) != !o.stamps[fd.Number()].IsZero() {
				return nil, fmt.Errorf("object %q: field %s and its stamp disagree", o.ID, fd.Name())
			}
		}
		d.objects[o.ID] = o
	}
	return d, nil
}

// Equal reports whether two documents hold the same objects, values and stamps.
func Equal(a, b *Doc) bool {
	if a.Len() != b.Len() {
		return false
	}
	for id, oa := range a.objects {
		ob := b.objects[id]
		if ob == nil || oa.stamps != ob.stamps || !proto.Equal(oa.Props, ob.Props) {
			return false
		}
	}
	return true
}

func isEmpty(p *pb.ObjectProps) bool {
	empty := true
	p.ProtoReflect().Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool {
		empty = false
		return false
	})
	return empty
}
