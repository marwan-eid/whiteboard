package board

import (
	"context"
	"errors"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	pb "whiteboard/internal/pb/whiteboard/v1"
)

// Store persists a board's event log and snapshots. internal/store has the
// Postgres implementation; MemoryStore is for tests and experiments.
type Store interface {
	// Load returns the latest snapshot (nil for a new board) and every log
	// entry after it, in seq order. It creates the board if it doesn't exist.
	Load(ctx context.Context, boardID string) (Loaded, error)
	// Append durably writes entries, atomically. It returns ErrConflict if
	// any seq already exists: another writer owns the board.
	Append(ctx context.Context, boardID string, entries []LogEntry) error
	SaveSnapshot(ctx context.Context, boardID string, snap *pb.BoardSnapshot) error
}

// ErrConflict means the log already has an entry at one of the seqs being
// appended, so this node's copy of the board is stale.
var ErrConflict = errors.New("log conflict: seq already written")

// LogEntry is one sequenced batch as persisted.
type LogEntry struct {
	Seq       uint64
	ClientID  uint64
	ClientSeq uint64
	Stamp     *pb.Stamp
	Ops       []*pb.Op
}

type Loaded struct {
	Snapshot *pb.BoardSnapshot
	Tail     []LogEntry
}

// MemoryStore is an in-process Store. FailAppend and FailLoad, when set,
// make those calls fail (to test failure handling). Set them before use or
// via the setters.
type MemoryStore struct {
	mu         sync.Mutex
	logs       map[string][]LogEntry
	snaps      map[string][]*pb.BoardSnapshot
	FailAppend error
	FailLoad   error
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{logs: map[string][]LogEntry{}, snaps: map[string][]*pb.BoardSnapshot{}}
}

func (m *MemoryStore) Load(_ context.Context, boardID string) (Loaded, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailLoad != nil {
		return Loaded{}, m.FailLoad
	}
	var l Loaded
	from := uint64(0)
	if s := m.snaps[boardID]; len(s) > 0 {
		l.Snapshot = proto.CloneOf(s[len(s)-1])
		from = l.Snapshot.GetSeq()
	}
	for _, e := range m.logs[boardID] {
		if e.Seq > from {
			l.Tail = append(l.Tail, e)
		}
	}
	return l, nil
}

func (m *MemoryStore) Append(_ context.Context, boardID string, entries []LogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailAppend != nil {
		return m.FailAppend
	}
	log := m.logs[boardID]
	have := map[uint64]bool{}
	for _, e := range log {
		have[e.Seq] = true
	}
	for _, e := range entries {
		if have[e.Seq] {
			return ErrConflict
		}
	}
	log = append(log, entries...)
	sort.Slice(log, func(i, j int) bool { return log[i].Seq < log[j].Seq })
	m.logs[boardID] = log
	return nil
}

func (m *MemoryStore) SaveSnapshot(_ context.Context, boardID string, snap *pb.BoardSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snaps[boardID] = append(m.snaps[boardID], proto.CloneOf(snap))
	return nil
}

// SetFailAppend changes FailAppend safely while boards are running.
func (m *MemoryStore) SetFailAppend(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FailAppend = err
}

// AppendRaw writes entries without checks, to set up corrupt logs in tests.
func (m *MemoryStore) AppendRaw(boardID string, entries ...LogEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs[boardID] = append(m.logs[boardID], entries...)
}

// Entries returns a copy of a board's log.
func (m *MemoryStore) Entries(boardID string) []LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]LogEntry(nil), m.logs[boardID]...)
}

// LogLen reports how many entries a board's log holds.
func (m *MemoryStore) LogLen(boardID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.logs[boardID])
}

// Snapshots reports how many snapshots a board has.
func (m *MemoryStore) Snapshots(boardID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.snaps[boardID])
}
