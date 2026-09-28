package doc

import (
	"encoding/json"
	"os"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"pgregory.net/rapid"

	"whiteboard/internal/hlc"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

type vectorCase struct {
	Name     string
	Batches  []*pb.SequencedBatch
	Expected []*pb.ObjectState
}

func loadVectors(t *testing.T) []vectorCase {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/lww-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name     string            `json:"name"`
			Batches  []json.RawMessage `json:"batches"`
			Expected []json.RawMessage `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	var out []vectorCase
	for _, c := range file.Cases {
		vc := vectorCase{Name: c.Name}
		for _, b := range c.Batches {
			m := &pb.SequencedBatch{}
			if err := protojson.Unmarshal(b, m); err != nil {
				t.Fatalf("%s: batch: %v", c.Name, err)
			}
			vc.Batches = append(vc.Batches, m)
		}
		for _, e := range c.Expected {
			m := &pb.ObjectState{}
			if err := protojson.Unmarshal(e, m); err != nil {
				t.Fatalf("%s: expected: %v", c.Name, err)
			}
			vc.Expected = append(vc.Expected, m)
		}
		out = append(out, vc)
	}
	return out
}

// permutations yields every ordering of 0..n-1 (n is small in the vectors).
func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, p := range permutations(n - 1) {
		for i := 0; i <= len(p); i++ {
			q := append(append(append([]int{}, p[:i]...), n-1), p[i:]...)
			out = append(out, q)
		}
	}
	return out
}

func TestSharedVectors(t *testing.T) {
	for _, vc := range loadVectors(t) {
		t.Run(vc.Name, func(t *testing.T) {
			for _, order := range permutations(len(vc.Batches)) {
				d := New()
				for _, i := range order {
					b := vc.Batches[i]
					d.ApplyBatch(b.GetOps(), hlc.FromProto(b.GetStamp()))
				}
				got := d.Snapshot()
				if len(got) != len(vc.Expected) {
					t.Fatalf("order %v: %d objects, want %d", order, len(got), len(vc.Expected))
				}
				for i := range got {
					if !proto.Equal(got[i], vc.Expected[i]) {
						t.Fatalf("order %v:\n got  %v\n want %v", order, got[i], vc.Expected[i])
					}
				}
			}
		})
	}
}

// --- property-based tests -------------------------------------------------

type genBatch struct {
	stamp hlc.Stamp
	ops   []*pb.Op
}

var genIDs = []string{"1:a", "1:b", "2:a"}

func drawProps(t *rapid.T) *pb.ObjectProps {
	p := &pb.ObjectProps{}
	if rapid.Bool().Draw(t, "type?") {
		p.Type = pb.ShapeType(rapid.IntRange(1, 6).Draw(t, "type")).Enum()
	}
	if rapid.Bool().Draw(t, "deleted?") {
		p.Deleted = proto.Bool(rapid.Bool().Draw(t, "deleted"))
	}
	if rapid.Bool().Draw(t, "fill?") {
		p.Fill = proto.Uint32(rapid.Uint32().Draw(t, "fill"))
	}
	if rapid.Bool().Draw(t, "text?") {
		p.Text = proto.String(rapid.StringMatching(`[a-c]{0,3}`).Draw(t, "text"))
	}
	if isEmpty(p) || rapid.Bool().Draw(t, "x?") {
		p.X = proto.Float64(float64(rapid.IntRange(-5, 5).Draw(t, "x")))
	}
	return p
}

// drawBatches generates batches with unique stamps drawn from a small range,
// so ties on wall time and counter are common.
func drawBatches(t *rapid.T) []genBatch {
	n := rapid.IntRange(1, 25).Draw(t, "batches")
	seen := map[hlc.Stamp]bool{}
	var out []genBatch
	for len(out) < n {
		st := hlc.Stamp{
			WallMs:   rapid.Int64Range(1, 6).Draw(t, "wall"),
			Counter:  rapid.Uint32Range(0, 2).Draw(t, "counter"),
			ClientID: rapid.Uint64Range(1, 3).Draw(t, "client"),
		}
		if seen[st] {
			continue // a stamp identifies one write; never reuse it for different content
		}
		seen[st] = true
		b := genBatch{stamp: st}
		for range rapid.IntRange(1, 3).Draw(t, "ops") {
			b.ops = append(b.ops, &pb.Op{Id: rapid.SampledFrom(genIDs).Draw(t, "id"), Props: drawProps(t)})
		}
		out = append(out, b)
	}
	return out
}

func applyAll(d *Doc, batches []genBatch, order []int) {
	for _, i := range order {
		d.ApplyBatch(batches[i].ops, batches[i].stamp)
	}
}

// Any two delivery orders, with any duplicates, converge.
func TestConvergesRegardlessOfOrderAndDuplicates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		batches := drawBatches(t)
		n := len(batches)
		orderA := rapid.Permutation(indexes(n)).Draw(t, "orderA")
		orderB := rapid.Permutation(indexes(n)).Draw(t, "orderB")
		// Redeliver a random subset in B.
		orderB = append(orderB, rapid.SliceOfN(rapid.IntRange(0, n-1), 0, n).Draw(t, "dups")...)

		a, b := New(), New()
		applyAll(a, batches, orderA)
		applyAll(b, batches, orderB)
		if !Equal(a, b) {
			t.Fatalf("diverged:\n A %v\n B %v", a.Snapshot(), b.Snapshot())
		}
	})
}

// Each field ends up with the value from the highest-stamped write to it.
func TestMatchesLastWriterWinsModel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		batches := drawBatches(t)
		d := New()
		applyAll(d, batches, rapid.Permutation(indexes(len(batches))).Draw(t, "order"))

		type key struct {
			id    string
			field int
		}
		winner := map[key]hlc.Stamp{}
		value := map[key]any{}
		for _, b := range batches {
			for _, op := range b.ops {
				op.GetProps().ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
					k := key{op.GetId(), int(fd.Number())}
					if w, ok := winner[k]; !ok || b.stamp.Compare(w) > 0 {
						winner[k], value[k] = b.stamp, v.Interface()
					}
					return true
				})
			}
		}
		for k, want := range value {
			o := d.Get(k.id)
			fd := propFields.ByNumber(protoreflect.FieldNumber(k.field))
			if got := o.Props.ProtoReflect().Get(fd).Interface(); got != want {
				t.Fatalf("%s.%s = %v, want %v", k.id, fd.Name(), got, want)
			}
			if o.stamps[k.field] != winner[k] {
				t.Fatalf("%s.%s stamp = %v, want %v", k.id, fd.Name(), o.stamps[k.field], winner[k])
			}
		}
	})
}

// A replica restored from a snapshot midway ends identical to one that saw everything.
func TestSnapshotRoundTripMidStream(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		batches := drawBatches(t)
		order := rapid.Permutation(indexes(len(batches))).Draw(t, "order")
		cut := rapid.IntRange(0, len(order)).Draw(t, "cut")

		full := New()
		applyAll(full, batches, order)

		partial := New()
		applyAll(partial, batches, order[:cut])
		restored, err := FromSnapshot(partial.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		applyAll(restored, batches, order[cut:])
		if !Equal(full, restored) {
			t.Fatalf("restored replica diverged")
		}
	})
}

func TestFromSnapshotRejectsInconsistentStamps(t *testing.T) {
	bad := []*pb.ObjectState{{
		Id:     "1:a",
		Props:  &pb.ObjectProps{X: proto.Float64(1)},
		Stamps: []*pb.FieldStamps{{Stamp: hlc.Stamp{WallMs: 1}.Proto(), FieldMask: 1 << 4}}, // y, not x
	}}
	if _, err := FromSnapshot(bad); err == nil {
		t.Fatal("expected error for a set field without a stamp")
	}
}

func TestApplyIgnoresEmptyOps(t *testing.T) {
	d := New()
	if d.Apply(&pb.Op{Id: "1:a"}, hlc.Stamp{WallMs: 1}) || d.Apply(&pb.Op{Id: "1:a", Props: &pb.ObjectProps{}}, hlc.Stamp{WallMs: 1}) {
		t.Fatal("empty op reported a change")
	}
	if d.Len() != 0 {
		t.Fatal("empty op created an object")
	}
}

func indexes(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// Merging objects' states into an empty replica, in any grouping, rebuilds it.
func TestMergeStateRebuildsReplica(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		batches := drawBatches(t)
		full := New()
		applyAll(full, batches, indexes(len(batches)))

		// Another replica saw some batches, then receives full states.
		partial := New()
		applyAll(partial, batches, rapid.Permutation(indexes(len(batches))).Draw(t, "order")[:rapid.IntRange(0, len(batches)).Draw(t, "cut")])
		for _, s := range full.Snapshot() {
			partial.MergeState(s)
		}
		if !Equal(full, partial) {
			t.Fatalf("merge did not converge:\n full %v\n got  %v", full.Snapshot(), partial.Snapshot())
		}
	})
}

// visibleProps is a document's visible objects and their properties.
func visibleProps(d *Doc) map[string]*pb.ObjectProps {
	out := map[string]*pb.ObjectProps{}
	for id, o := range d.objects {
		if o.Visible() {
			p := proto.CloneOf(o.Props)
			p.Deleted = nil // false or unset look the same
			out[id] = p
		}
	}
	return out
}

// Applying Diff(now, past) with a newer stamp makes the present look like the past.
func TestDiffRestoresVisibleState(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		batches := drawBatches(t)
		order := indexes(len(batches))
		cut := rapid.IntRange(0, len(order)).Draw(t, "cut")
		past := New()
		applyAll(past, batches, order[:cut])
		now := past.Clone()
		applyAll(now, batches, order[cut:])

		now.ApplyBatch(Diff(now, past), hlc.Stamp{WallMs: 1 << 40})
		got, want := visibleProps(now), visibleProps(past)
		if len(got) != len(want) {
			t.Fatalf("visible objects: got %d, want %d", len(got), len(want))
		}
		for id, p := range want {
			g := got[id]
			if g == nil {
				t.Fatalf("%s missing after restore", id)
			}
			// Fields the past had must match. Fields first set later cannot be
			// unset (text is cleared to ""), so they are not compared.
			if p.Text == nil && g.GetText() != "" {
				t.Fatalf("%s: text %q not cleared", id, g.GetText())
			}
			pm, gm := p.ProtoReflect(), g.ProtoReflect()
			pm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
				// The generator may change type, which the server never allows.
				if fd.Name() != "type" && !gm.Get(fd).Equal(v) {
					t.Fatalf("%s.%s = %v, want %v", id, fd.Name(), gm.Get(fd), v)
				}
				return true
			})
		}
	})
}
