package doc

import (
	"errors"
	"fmt"
	"math"
	"strings"

	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid batch")

// ValidateBatch checks a client's batch against the current document before
// the server applies it. Validation is server-side only: replicas apply
// whatever the server sequenced.
func (d *Doc) ValidateBatch(ops []*pb.Op, clientID uint64) error {
	if len(ops) == 0 || len(ops) > protocol.MaxOpsPerBatch {
		return fmt.Errorf("%w: batch must have 1..%d ops, has %d", ErrInvalid, protocol.MaxOpsPerBatch, len(ops))
	}
	prefix := protocol.ObjectIDPrefix(clientID)
	createdHere := map[string]bool{}
	for i, op := range ops {
		if err := d.validateOp(op, prefix, createdHere); err != nil {
			return fmt.Errorf("%w: op %d (%q): %w", ErrInvalid, i, op.GetId(), err)
		}
	}
	return nil
}

func (d *Doc) validateOp(op *pb.Op, prefix string, createdHere map[string]bool) error {
	id, p := op.GetId(), op.GetProps()
	if !protocol.ValidObjectID(id) {
		return errors.New("invalid object id")
	}
	if p == nil || isEmpty(p) {
		return errors.New("no properties")
	}
	existing := d.objects[id]
	exists := createdHere[id] || (existing != nil && existing.Created())
	switch {
	case !exists && p.Type == nil:
		return errors.New("unknown object; creating one requires type")
	case !exists && !strings.HasPrefix(id, prefix):
		return fmt.Errorf("new object ids must start with %q", prefix)
	case !exists:
		if _, ok := pb.ShapeType_name[int32(p.GetType())]; !ok || p.GetType() == pb.ShapeType_SHAPE_TYPE_UNSPECIFIED {
			return errors.New("invalid type")
		}
		createdHere[id] = true
	case p.Type != nil:
		return errors.New("type cannot change")
	}
	return validateProps(p)
}

func validateProps(p *pb.ObjectProps) error {
	for _, c := range []struct {
		name  string
		v     *float64
		lo, h float64
	}{
		{"x", p.X, -protocol.MaxCoord, protocol.MaxCoord},
		{"y", p.Y, -protocol.MaxCoord, protocol.MaxCoord},
		{"w", p.W, 0, protocol.MaxSize},
		{"h", p.H, 0, protocol.MaxSize},
	} {
		if c.v != nil && !inRange(*c.v, c.lo, c.h) {
			return fmt.Errorf("%s out of range", c.name)
		}
	}
	if p.StrokeWidth != nil && !inRange(float64(*p.StrokeWidth), 0, protocol.MaxStrokeWidth) {
		return errors.New("stroke_width out of range")
	}
	if p.Z != nil && !validZ(*p.Z) {
		return errors.New("invalid z")
	}
	if p.Text != nil && len(*p.Text) > protocol.MaxTextBytes {
		return fmt.Errorf("text longer than %d bytes", protocol.MaxTextBytes)
	}
	if len(p.GetPoints()) > protocol.MaxPointsBytes {
		return fmt.Errorf("points longer than %d bytes", protocol.MaxPointsBytes)
	}
	if p.FontSize != nil && !inRange(float64(*p.FontSize), protocol.MinFontSize, protocol.MaxFontSize) {
		return errors.New("font_size out of range")
	}
	for name, b := range map[string]*pb.Binding{"from": p.GetFrom(), "to": p.GetTo()} {
		if b == nil {
			continue
		}
		if id := b.GetObjectId(); id != "" && !protocol.ValidObjectID(id) {
			return fmt.Errorf("%s: invalid object id", name)
		}
		if !inRange(float64(b.GetAnchorX()), 0, 1) || !inRange(float64(b.GetAnchorY()), 0, 1) {
			return fmt.Errorf("%s: anchor out of range", name)
		}
	}
	return nil
}

func inRange(v, lo, hi float64) bool {
	return !math.IsNaN(v) && v >= lo && v <= hi
}

// validZ accepts fractional-index keys: 1..MaxZLen printable ASCII characters.
func validZ(z string) bool {
	if z == "" || len(z) > protocol.MaxZLen {
		return false
	}
	for i := 0; i < len(z); i++ {
		if z[i] < '!' || z[i] > '~' {
			return false
		}
	}
	return true
}
