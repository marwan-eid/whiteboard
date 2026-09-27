package protocol

import (
	"strings"
	"testing"
)

func TestValidBoardID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"demo", true},
		{"a-B_9", true},
		{strings.Repeat("x", MaxBoardIDLen), true},
		{"", false},
		{strings.Repeat("x", MaxBoardIDLen+1), false},
		{"has space", false},
		{"slash/", false},
		{"dot.", false},
		{"ünicode", false},
	}
	for _, c := range cases {
		if got := ValidBoardID(c.id); got != c.want {
			t.Errorf("ValidBoardID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
