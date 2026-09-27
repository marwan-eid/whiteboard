package board

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Encode-once fan-out: every op, cursor update and entering object is
// serialized once per tick, and each client's frame is assembled by copying
// the pieces it needs. The result is byte-for-byte a valid ServerMessage
// (tests check it decodes to the same message as marshaling the long way).

// Field numbers, from protocol.proto.
const (
	fieldServerMessageFrame  = 4
	fieldFrameBatches        = 1
	fieldFrameAcks           = 2
	fieldFrameCursors        = 3
	fieldFrameObjects        = 4
	fieldFrameOnline         = 5
	fieldFrameSeq            = 6
	fieldFrameLeave          = 7
	fieldSequencedBatchSeq   = 1
	fieldSequencedBatchStamp = 2
	fieldSequencedBatchOps   = 3
)

// encodedBatch is one committed batch, pre-encoded.
type encodedBatch struct {
	origin uint64
	ids    []string // each op's object id
	header []byte   // seq and stamp fields of a SequencedBatch
	ops    [][]byte // each op as a SequencedBatch.ops field entry
	lodOps [][]byte // the same, limited to LOD properties; nil entries have none
	before []rectOK // each op's object box before and after the op
	after  []rectOK
}

func encodeBatch(e LogEntry, before, after []rectOK) encodedBatch {
	eb := encodedBatch{origin: e.ClientID, before: before, after: after}
	eb.header = protowire.AppendTag(nil, fieldSequencedBatchSeq, protowire.VarintType)
	eb.header = protowire.AppendVarint(eb.header, e.Seq)
	eb.header = appendMessage(eb.header, fieldSequencedBatchStamp, e.Stamp)
	for _, op := range e.Ops {
		eb.ids = append(eb.ids, op.GetId())
		eb.ops = append(eb.ops, appendMessage(nil, fieldSequencedBatchOps, op))
		if lop := lodOp(op); lop != nil {
			eb.lodOps = append(eb.lodOps, appendMessage(nil, fieldSequencedBatchOps, lop))
		} else {
			eb.lodOps = append(eb.lodOps, nil)
		}
	}
	return eb
}

// frameBuilder assembles one client's frame.
type frameBuilder struct {
	body  []byte
	empty bool
}

func newFrameBuilder(buf []byte) *frameBuilder {
	return &frameBuilder{body: buf[:0], empty: true}
}

// batch appends a SequencedBatch holding the selected ops.
func (f *frameBuilder) batch(header []byte, ops [][]byte) {
	size := len(header)
	for _, op := range ops {
		size += len(op)
	}
	f.body = protowire.AppendTag(f.body, fieldFrameBatches, protowire.BytesType)
	f.body = protowire.AppendVarint(f.body, uint64(size))
	f.body = append(f.body, header...)
	for _, op := range ops {
		f.body = append(f.body, op...)
	}
	f.empty = false
}

// raw appends an already-encoded field entry.
func (f *frameBuilder) raw(entry []byte) {
	f.body = append(f.body, entry...)
	f.empty = false
}

func (f *frameBuilder) message(field protowire.Number, m proto.Message) {
	f.body = appendMessage(f.body, field, m)
	f.empty = false
}

func (f *frameBuilder) online(n uint32) {
	f.body = protowire.AppendTag(f.body, fieldFrameOnline, protowire.VarintType)
	f.body = protowire.AppendVarint(f.body, uint64(n))
	f.empty = false
}

func (f *frameBuilder) leave(id string) {
	f.body = protowire.AppendTag(f.body, fieldFrameLeave, protowire.BytesType)
	f.body = protowire.AppendString(f.body, id)
	f.empty = false
}

func (f *frameBuilder) seq(s uint64) {
	f.body = protowire.AppendTag(f.body, fieldFrameSeq, protowire.VarintType)
	f.body = protowire.AppendVarint(f.body, s)
}

// serverMessage wraps the frame as a ServerMessage, in a new slice.
func (f *frameBuilder) serverMessage() []byte {
	out := make([]byte, 0, len(f.body)+8)
	out = protowire.AppendTag(out, fieldServerMessageFrame, protowire.BytesType)
	out = protowire.AppendBytes(out, f.body)
	return out
}

func appendMessage(b []byte, field protowire.Number, m proto.Message) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendBytes(b, mustMarshal(m))
}

func mustMarshal(m proto.Message) []byte {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		panic(err) // only possible for invalid UTF-8, which Unmarshal already rejected
	}
	return data
}
