package names

import (
	"fmt"
	"strings"
	"testing"
)

func TestDeterministic(t *testing.T) {
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("3f2a%04d-aaaa-bbbb", i)
		a, b := For(id), For(id)
		if a != b || a.Name == "" || !strings.HasPrefix(a.Color, "#") || len(strings.Fields(a.Name)) != 2 {
			t.Fatalf("For(%q) = %+v, %+v", id, a, b)
		}
	}
	if For("a") == For("b") && For("b") == For("c") {
		t.Error("every id gets the same name")
	}
}

// A small group (the judges) never shares a name, and up to len(colours)
// people never share a colour, whatever their ids.
func TestNoCollisionsInASmallGroup(t *testing.T) {
	for seed := 0; seed < 300; seed++ {
		usedN, usedC := map[string]bool{}, map[string]bool{}
		for i := 0; i < 30; i++ {
			id := fmt.Sprintf("%d-phone-%d", seed, i)
			n := Pick(id, func(s string) bool { return usedN[s] }, func(s string) bool { return usedC[s] })
			if usedN[n.Name] {
				t.Fatalf("seed %d: name %q given twice", seed, n.Name)
			}
			if i < len(colours) && usedC[n.Color] {
				t.Fatalf("seed %d: phone %d shares colour %s", seed, i, n.Color)
			}
			usedN[n.Name], usedC[n.Color] = true, true
		}
	}
}

// Alone, a phone gets its deterministic name.
func TestPickAloneIsFor(t *testing.T) {
	no := func(string) bool { return false }
	for _, id := range []string{"x", "abc-123", "0f0f"} {
		if Pick(id, no, no) != For(id) {
			t.Errorf("Pick(%q) differs from For", id)
		}
	}
}

func TestAllNamesTaken(t *testing.T) {
	yes := func(string) bool { return true }
	if Pick("x", yes, yes) != For("x") {
		t.Error("want For(id) when everything is taken")
	}
}
