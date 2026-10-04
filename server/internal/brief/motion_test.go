package brief

import (
	"strings"
	"testing"
)

// A density watch says why it matters: what the crowd at the spot is doing.
func TestTemplateSaysCrowdMotion(t *testing.T) {
	for _, tc := range []struct {
		motion, want string
	}{
		{"packed and barely moving", "people bunching up near 9, 10, packed and barely moving."},
		{"", "people bunching up near 9, 10."},
	} {
		b := Template(Info{Kind: "density", Zone: "a1", Where: "Aisle", Level: "yellow", X: 9, Y: 10, Motion: tc.motion})
		if !strings.HasSuffix(b.Headline, tc.want) {
			t.Errorf("motion %q: headline %q, want it to end %q", tc.motion, b.Headline, tc.want)
		}
	}
}
