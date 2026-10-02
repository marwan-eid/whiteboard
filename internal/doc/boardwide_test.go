package doc

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"whiteboard/internal/hlc"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

func TestValidateBoardWide(t *testing.T) {
	timer := pb.ShapeType_SHAPE_TYPE_TIMER.Enum()
	vote := pb.ShapeType_SHAPE_TYPE_VOTE.Enum()
	ballot := pb.ShapeType_SHAPE_TYPE_BALLOT.Enum()
	mine := protocol.BallotID(client) // "_ballot:z"

	open := New() // a board with an open 2-dot vote "s1" and a shape to vote for
	open.ApplyBatch([]*pb.Op{
		rect("q:1"),
		set(protocol.VoteID, &pb.ObjectProps{Type: vote, Text: proto.String("s1"), VotesPerUser: proto.Uint32(2), Closed: proto.Bool(false)}),
	}, hlc.Stamp{WallMs: 1, ClientID: 26})
	closed := New()
	closed.ApplyBatch([]*pb.Op{
		set(protocol.VoteID, &pb.ObjectProps{Type: vote, Text: proto.String("s1"), VotesPerUser: proto.Uint32(2), Closed: proto.Bool(true)}),
	}, hlc.Stamp{WallMs: 1, ClientID: 26})

	cases := []struct {
		name    string
		d       *Doc
		ops     []*pb.Op
		wantErr string
	}{
		{"start a timer", New(), []*pb.Op{set(protocol.TimerID, &pb.ObjectProps{Type: timer, EndsAtMs: proto.Int64(5000), RemainingMs: proto.Int64(60_000)})}, ""},
		{"timer without type", New(), []*pb.Op{set(protocol.TimerID, &pb.ObjectProps{EndsAtMs: proto.Int64(5000)})}, "requires type"},
		{"timer with the wrong type", New(), []*pb.Op{set(protocol.TimerID, &pb.ObjectProps{Type: vote})}, "requires type"},
		{"timer with geometry", New(), []*pb.Op{set(protocol.TimerID, &pb.ObjectProps{Type: timer, X: proto.Float64(1)})}, "cannot set"},
		{"timer too long", New(), []*pb.Op{set(protocol.TimerID, &pb.ObjectProps{Type: timer, RemainingMs: proto.Int64(protocol.MaxTimerMs + 1)})}, "remaining_ms"},
		{"unknown board-wide id", New(), []*pb.Op{set("_other", &pb.ObjectProps{Type: timer})}, "unknown board-wide"},
		{"no props", New(), []*pb.Op{{Id: protocol.TimerID}}, "no properties"},
		{"timer fields on a shape", New(), []*pb.Op{set("z:1", &pb.ObjectProps{Type: pb.ShapeType_SHAPE_TYPE_RECT.Enum(), EndsAtMs: proto.Int64(1)})}, "board-wide objects only"},
		{"start a vote", New(), []*pb.Op{set(protocol.VoteID, &pb.ObjectProps{Type: vote, Text: proto.String("s2"), VotesPerUser: proto.Uint32(3)})}, ""},
		{"too many dots per user", New(), []*pb.Op{set(protocol.VoteID, &pb.ObjectProps{Type: vote, VotesPerUser: proto.Uint32(protocol.MaxVotesPerUser + 1)})}, "votes_per_user"},
		{"vote", open, []*pb.Op{set(mine, &pb.ObjectProps{Type: ballot, Text: proto.String("s1"), Votes: proto.String("q:1,q:1")})}, ""},
		{"take votes back", open, []*pb.Op{set(mine, &pb.ObjectProps{Type: ballot, Text: proto.String("s1"), Votes: proto.String("")})}, ""},
		{"too many votes", open, []*pb.Op{set(mine, &pb.ObjectProps{Type: ballot, Text: proto.String("s1"), Votes: proto.String("q:1,q:1,q:1")})}, "more than 2"},
		{"vote in an old session", open, []*pb.Op{set(mine, &pb.ObjectProps{Type: ballot, Text: proto.String("s0"), Votes: proto.String("q:1")})}, "open session"},
		{"vote for a board-wide object", open, []*pb.Op{set(mine, &pb.ObjectProps{Type: ballot, Text: proto.String("s1"), Votes: proto.String(protocol.TimerID)})}, "invalid voted-for"},
		{"someone else's ballot", open, []*pb.Op{set("_ballot:q", &pb.ObjectProps{Type: ballot, Text: proto.String("s1"), Votes: proto.String("q:1")})}, "unknown board-wide"},
		{"vote after it closed", closed, []*pb.Op{set(mine, &pb.ObjectProps{Type: ballot, Text: proto.String("s1"), Votes: proto.String("q:1")})}, "no vote is open"},
		{"vote with no session", New(), []*pb.Op{set(mine, &pb.ObjectProps{Type: ballot, Text: proto.String("s1"), Votes: proto.String("")})}, "no vote is open"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.d.ValidateBatch(c.ops, client)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, c.wantErr)
			}
		})
	}
}
