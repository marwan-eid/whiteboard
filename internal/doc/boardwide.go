package doc

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

// Fields each board-wide type may set, as ObjectProps field-number bits.
var boardWideFields = map[pb.ShapeType]uint32{
	pb.ShapeType_SHAPE_TYPE_TIMER:  bit(1, 16, 17),
	pb.ShapeType_SHAPE_TYPE_VOTE:   bit(1, 11, 18, 19),
	pb.ShapeType_SHAPE_TYPE_BALLOT: bit(1, 11, 20),
}

// boardWideOnly are the board-wide fields, which shapes may not set.
var boardWideOnly = bit(16, 17, 18, 19, 20)

func bit(fields ...protoreflect.FieldNumber) uint32 {
	var m uint32
	for _, f := range fields {
		m |= 1 << f
	}
	return m
}

func fieldMask(p *pb.ObjectProps) uint32 {
	var m uint32
	p.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		m |= 1 << fd.Number()
		return true
	})
	return m
}

// validateBoardWide checks an op on the timer, the vote or a ballot. Any
// editor may run the timer and the vote; a client writes only its own ballot.
func (d *Doc) validateBoardWide(op *pb.Op, clientID uint64, createdHere map[string]bool) error {
	id, p := op.GetId(), op.GetProps()
	if p == nil || isEmpty(p) {
		return errors.New("no properties")
	}
	var want pb.ShapeType
	switch {
	case id == protocol.TimerID:
		want = pb.ShapeType_SHAPE_TYPE_TIMER
	case id == protocol.VoteID:
		want = pb.ShapeType_SHAPE_TYPE_VOTE
	case id == protocol.BallotID(clientID):
		want = pb.ShapeType_SHAPE_TYPE_BALLOT
	default:
		return errors.New("unknown board-wide object")
	}
	existing := d.objects[id]
	exists := createdHere[id] || (existing != nil && existing.Created())
	switch {
	case !exists && p.GetType() != want:
		return fmt.Errorf("creating it requires type %v", want)
	case exists && p.Type != nil:
		return errors.New("type cannot change")
	}
	createdHere[id] = true
	if extra := fieldMask(p) &^ boardWideFields[want]; extra != 0 {
		return fmt.Errorf("%v objects cannot set these properties (mask %#x)", want, extra)
	}

	switch want {
	case pb.ShapeType_SHAPE_TYPE_TIMER:
		if p.EndsAtMs != nil && p.GetEndsAtMs() < 0 {
			return errors.New("ends_at_ms out of range")
		}
		if p.RemainingMs != nil && (p.GetRemainingMs() < 0 || p.GetRemainingMs() > protocol.MaxTimerMs) {
			return errors.New("remaining_ms out of range")
		}
	case pb.ShapeType_SHAPE_TYPE_VOTE:
		if p.VotesPerUser != nil && (p.GetVotesPerUser() < 1 || p.GetVotesPerUser() > protocol.MaxVotesPerUser) {
			return errors.New("votes_per_user out of range")
		}
		if p.Text != nil && (p.GetText() == "" || len(p.GetText()) > protocol.MaxSessionIDLen) {
			return errors.New("invalid session id")
		}
	case pb.ShapeType_SHAPE_TYPE_BALLOT:
		return d.validateBallot(p)
	}
	return nil
}

// validateBallot checks a ballot against the vote as it stands: it must be
// for the open session and spend at most the allowed dots.
func (d *Doc) validateBallot(p *pb.ObjectProps) error {
	vote := d.objects[protocol.VoteID]
	if vote == nil || !vote.Created() || vote.Props.GetClosed() || vote.Props.GetText() == "" {
		return errors.New("no vote is open")
	}
	if p.Text == nil || p.GetText() != vote.Props.GetText() || p.Votes == nil {
		return errors.New("a ballot must name the open session and carry its votes")
	}
	if p.GetVotes() == "" {
		return nil
	}
	ids := strings.Split(p.GetVotes(), ",")
	if len(ids) > int(vote.Props.GetVotesPerUser()) {
		return fmt.Errorf("more than %d votes", vote.Props.GetVotesPerUser())
	}
	for _, id := range ids {
		if !protocol.ValidObjectID(id) || protocol.BoardWide(id) {
			return fmt.Errorf("invalid voted-for id %q", id)
		}
	}
	return nil
}
