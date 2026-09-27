// Package protocol holds wire-protocol constants and helpers shared by the
// server, the Go client and the load generator. Message types live in
// internal/pb. Limits here are mirrored in web/src/sync/limits.ts.
package protocol

import "strconv"

// Version is bumped on any incompatible change to proto/whiteboard/v1.
// Keep in sync with PROTOCOL_VERSION in web/src/net/protocol.ts.
const Version uint32 = 1

const (
	// MaxBoardIDLen bounds board ids so they are safe in URLs, logs and keys.
	MaxBoardIDLen = 64
	// MaxClientID keeps client ids exact as JavaScript numbers.
	MaxClientID = 1<<53 - 1

	MaxOpsPerBatch = 500
	MaxObjectIDLen = 64
	MaxTextBytes   = 10_000
	MaxZLen        = 64
	MaxCoord       = 1e9
	MaxSize        = 1e6
	MaxStrokeWidth = 1_000
	MaxPointsBytes = 32 << 10
	MinFontSize    = 4
	MaxFontSize    = 512
)

// ValidBoardID reports whether id is 1..MaxBoardIDLen chars of [A-Za-z0-9_-].
func ValidBoardID(id string) bool {
	if id == "" || len(id) > MaxBoardIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func ValidClientID(id uint64) bool { return id >= 1 && id <= MaxClientID }

// ObjectIDPrefix is the prefix of every object id a client creates, which
// keeps ids unique without coordination: "<client id in base 36>:".
func ObjectIDPrefix(clientID uint64) string {
	return strconv.FormatUint(clientID, 36) + ":"
}

// ValidObjectID reports whether id is 1..MaxObjectIDLen chars of [a-z0-9:_-].
func ValidObjectID(id string) bool {
	if id == "" || len(id) > MaxObjectIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == ':', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
