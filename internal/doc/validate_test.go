package doc

import (
	"errors"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"whiteboard/internal/hlc"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

const client = 35 // base 36: "z"

func rect(id string) *pb.Op {
	return &pb.Op{Id: id, Props: &pb.ObjectProps{Type: pb.ShapeType_SHAPE_TYPE_RECT.Enum(), X: proto.Float64(1)}}
}

func set(id string, p *pb.ObjectProps) *pb.Op { return &pb.Op{Id: id, Props: p} }

func TestValidateBatch(t *testing.T) {
	d := New()
	d.ApplyBatch([]*pb.Op{rect("q:existing")}, hlc.Stamp{WallMs: 1, ClientID: 26})

	manyOps := make([]*pb.Op, protocol.MaxOpsPerBatch+1)
	for i := range manyOps {
		manyOps[i] = set("q:existing", &pb.ObjectProps{X: proto.Float64(1)})
	}

	cases := []struct {
		name    string
		ops     []*pb.Op
		wantErr string
	}{
		{"create with own prefix", []*pb.Op{rect("z:1")}, ""},
		{"create then edit in one batch", []*pb.Op{rect("z:1"), set("z:1", &pb.ObjectProps{Y: proto.Float64(2)})}, ""},
		{"edit someone else's object", []*pb.Op{set("q:existing", &pb.ObjectProps{Deleted: proto.Bool(true)})}, ""},
		{"empty batch", nil, "1..500 ops"},
		{"too many ops", manyOps, "1..500 ops"},
		{"create with another client's prefix", []*pb.Op{rect("q:new")}, `must start with "z:"`},
		{"edit unknown object", []*pb.Op{set("z:ghost", &pb.ObjectProps{X: proto.Float64(1)})}, "creating one requires type"},
		{"change type", []*pb.Op{set("q:existing", &pb.ObjectProps{Type: pb.ShapeType_SHAPE_TYPE_ELLIPSE.Enum()})}, "type cannot change"},
		{"unspecified type", []*pb.Op{set("z:1", &pb.ObjectProps{Type: pb.ShapeType_SHAPE_TYPE_UNSPECIFIED.Enum()})}, "invalid type"},
		{"unknown enum value", []*pb.Op{set("z:1", &pb.ObjectProps{Type: pb.ShapeType(99).Enum()})}, "invalid type"},
		{"bad id chars", []*pb.Op{rect("Z:1")}, "invalid object id"},
		{"no props", []*pb.Op{{Id: "q:existing"}}, "no properties"},
		{"NaN", []*pb.Op{set("q:existing", &pb.ObjectProps{X: proto.Float64(math.NaN())})}, "x out of range"},
		{"infinite", []*pb.Op{set("q:existing", &pb.ObjectProps{Y: proto.Float64(math.Inf(1))})}, "y out of range"},
		{"negative width", []*pb.Op{set("q:existing", &pb.ObjectProps{W: proto.Float64(-1)})}, "w out of range"},
		{"huge stroke", []*pb.Op{set("q:existing", &pb.ObjectProps{StrokeWidth: proto.Float32(5000)})}, "stroke_width out of range"},
		{"bad z", []*pb.Op{set("q:existing", &pb.ObjectProps{Z: proto.String("a b")})}, "invalid z"},
		{"long text", []*pb.Op{set("q:existing", &pb.ObjectProps{Text: proto.String(strings.Repeat("x", protocol.MaxTextBytes+1))})}, "text longer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := d.ValidateBatch(c.ops, client)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, c.wantErr)
			case err != nil && !errors.Is(err, ErrInvalid):
				t.Fatalf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}
