package crowdsim

import (
	"encoding/json"
	"math"
	"sort"
	"testing"
)

// Tests of the furnished venues (venue.go, person.go, nav.go). Each runs a
// scenario for a while and checks what the literature and a hand
// calculation say should come out: door flows, row egress, evacuation
// times, staggered starts, re-routing, and nobody stuck or inside furniture.

func newVenue(t testing.TB, scn string, people int, seed int64) *World {
	t.Helper()
	w, err := New(Config{People: people, Participation: 0.6, Scenario: scn, Seed: seed})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *World) runFor(sec float64) {
	for end := w.T + sec; w.T < end-1e-9; {
		w.Step()
	}
}

// crossings counts the people who left through exit id (by the fall in
// headcount while they were its target is hard to attribute, so it watches
// each person's last target before they went).
type exitCounter struct {
	last  map[int]string
	count map[string]int
	seen  map[int]bool
}

func newExitCounter() *exitCounter {
	return &exitCounter{last: map[int]string{}, count: map[string]int{}, seen: map[int]bool{}}
}

func (c *exitCounter) tick(w *World) {
	alive := map[int]bool{}
	for _, a := range w.agents {
		alive[a.ID] = true
		c.seen[a.ID] = true
		if a.p != nil && a.p.target != nil && !a.p.target.Inner {
			c.last[a.ID] = a.p.target.ID
		}
	}
	for id := range c.seen {
		if !alive[id] {
			c.count[c.last[id]]++
			delete(c.seen, id)
		}
	}
}

// TestScenarioList: every scenario builds, in its own room, within the
// seats, and describes itself.
func TestScenarioList(t *testing.T) {
	if len(Scenarios) != 4 {
		t.Fatalf("scenarios %v", Scenarios)
	}
	for _, s := range ScenarioInfos() {
		if s.ID == "concert" {
			continue
		}
		w := newVenue(t, s.ID, s.People, 1)
		if w.G.W != s.W || w.G.H != s.H {
			t.Errorf("%s: venue %gx%g, want %gx%g", s.ID, w.G.W, w.G.H, s.W, s.H)
		}
		if len(s.Actions) < 3 || s.Desc == "" {
			t.Errorf("%s: %d actions, desc %q", s.ID, len(s.Actions), s.Desc)
		}
		if len(w.vs.furniture) == 0 {
			t.Errorf("%s: no furniture", s.ID)
		}
		st := w.Status()
		if st.Scenario != s.ID || st.Venue == nil || len(st.Furniture) == 0 {
			t.Errorf("%s: status %+v", s.ID, st)
		}
	}
	// Too many people for the seats: clamped, not an error.
	w := newVenue(t, "classroom", 500, 1)
	if n := len(w.agents); n != 64 { // 63 seats + the teacher
		t.Errorf("classroom with 500 asked: %d people", n)
	}
}

// TestSeatedStill: a seated class does not move, and its people read as seated.
func TestSeatedStill(t *testing.T) {
	w := newVenue(t, "auditorium", 300, 2)
	w.vs.nextLeaver = 1e9 // nobody slips out during this test
	start := make([][2]float64, len(w.agents))
	for i, a := range w.agents {
		start[i] = [2]float64{a.X, a.Y}
	}
	w.runFor(10)
	moved := 0.0
	for i, a := range w.agents {
		moved = math.Max(moved, math.Hypot(a.X-start[i][0], a.Y-start[i][1]))
		if w.wireState(a) != StSeated {
			t.Errorf("person %d state %d, want seated", a.ID, w.wireState(a))
		}
	}
	if moved > 0.02 {
		t.Errorf("a seated person moved %.3f m in 10 s", moved)
	}
	for _, b := range w.Bodies() {
		if len(b) != 7 || b[6] != StSeated || b[5] < 0 || b[5] >= 360 {
			t.Fatalf("body %v", b)
		}
	}
}

// TestClassroomDismiss: class ends; everyone leaves, nobody is left
// behind, and the start is staggered (pre-movement spread) and herded (the
// people around someone who has got up get up sooner).
func TestClassroomDismiss(t *testing.T) {
	w := newVenue(t, "classroom", 45, 3)
	w.vs.nextLeaver, w.vs.latecomersLeft = 1e9, 0
	w.runFor(2)
	if err := w.Apply(Action{Type: ActDismiss}); err != nil {
		t.Fatal(err)
	}
	t0 := w.T
	var up []float64 // when each person stood up
	seen := map[int]bool{}
	gone := -1.0
	for w.T-t0 < 240 && len(w.agents) > 0 {
		w.Step()
		for _, a := range w.agents {
			if !seen[a.ID] && a.p.phase != phIdle && !a.p.teacher {
				seen[a.ID] = true
				up = append(up, w.T-t0)
			}
		}
		w.checkSane(t)
	}
	if len(w.agents) > 0 {
		for _, a := range w.agents {
			t.Errorf("left behind: %d at (%.1f,%.1f) phase %d goal %d", a.ID, a.X, a.Y, a.p.phase, a.p.goal)
		}
		t.Fatalf("%d people never left in 240 s", len(w.agents))
	}
	gone = w.T - t0
	sort.Float64s(up)
	if len(up) < 40 {
		t.Fatalf("only %d people got up", len(up))
	}
	first, median, last := up[0], up[len(up)/2], up[len(up)-1]
	t.Logf("classroom dismiss: first up %.1f s, median %.1f s, last %.1f s; empty at %.0f s", first, median, last, gone)
	if last-first < 8 || median < 2 {
		t.Errorf("start not staggered: first %.1f median %.1f last %.1f", first, median, last)
	}
	// Hand calculation: 45 people through two 0.9 m doors at 1.3 persons/(m·s)
	// = 2.3 /s → 19 s of flow, plus pre-movement (median ~8 s, tail ~30 s)
	// and walking (≤ 15 m at ~1.3 m/s ≈ 12 s): 40–120 s.
	if gone < 40 || gone > 120 {
		t.Errorf("classroom emptied in %.0f s, want 40–120", gone)
	}
}

// TestClassroomDoorFlow: the fire alarm with one door closed: the whole
// class through one 0.9 m door. Specific flow in the literature band
// (SFPE 1.32 /(m·s); Kretz et al. 2006 and Seyfried et al. 2009 measured
// 1.6–1.9 /(m·s) for ~1 m, where the model's own bottleneck test sits).
func TestClassroomDoorFlow(t *testing.T) {
	w := newVenue(t, "classroom", 63, 4)
	w.vs.nextLeaver, w.vs.latecomersLeft = 1e9, 0
	closed := false
	if err := w.Apply(Action{Type: ActExit, ID: "door-back", Open: &closed}); err != nil {
		t.Fatal(err)
	}
	if err := w.Apply(Action{Type: ActAlarm, Strength: fptr(0.3)}); err != nil {
		t.Fatal(err)
	}
	door := w.G.Exit("door-front")
	mx, _ := door.mid()
	// Count people crossing the door line (x = 9.2) from the room into the corridor.
	side := map[int]bool{}
	var times []float64
	for w.T < 300 && len(w.agents) > 0 {
		w.Step()
		for _, a := range w.agents {
			in := a.X < mx
			if was, ok := side[a.ID]; ok && was && !in && math.Abs(a.Y-(door.Y0+door.Y1)/2) < 1.5 {
				times = append(times, w.T)
			}
			side[a.ID] = in
		}
	}
	if len(times) < 50 {
		t.Fatalf("only %d people came through the door (left: %d)", len(times), len(w.agents))
	}
	// Flow over the busy middle: from the 10th to the 10th-last crossing.
	a, b := times[9], times[len(times)-10]
	flow := float64(len(times)-19) / (b - a) / 0.9
	t.Logf("classroom alarm, one 0.9 m door: %d through, %.2f persons/(m·s) over the busy %.0f s; empty at %.0f s", len(times), flow, b-a, w.T)
	if flow < 1.0 || flow > 2.2 {
		t.Errorf("specific flow %.2f /(m·s), want 1.0–2.2", flow)
	}
	if len(w.agents) > 0 {
		t.Errorf("%d people still inside at %.0f s", len(w.agents), w.T)
	}
}

// TestAuditoriumRowOrder: show ends; nobody passes through someone who is
// still sitting: whenever a walker is beside a seated person in the same
// row, that person has stood to let them by (letPass) or is in the next
// seat over.
func TestAuditoriumRowOrder(t *testing.T) {
	w := newVenue(t, "auditorium", 300, 5)
	w.vs.nextLeaver = 1e9
	w.runFor(2)
	if err := w.Apply(Action{Type: ActDismiss}); err != nil {
		t.Fatal(err)
	}
	t0 := w.T
	violations, checks := 0, 0
	for w.T-t0 < 200 && len(w.agents) > 0 {
		w.Step()
		if w.ticks%10 != 0 {
			continue
		}
		for _, a := range w.agents {
			p := a.p
			if p.phase != phRow || p.sub == 0 {
				continue
			}
			for _, s := range p.seat.row.seats {
				o := s.occ
				if o == nil || o == a || !o.p.sitting {
					continue
				}
				if math.Abs(o.X-a.X) < 0.3 && math.Abs(o.Y-a.Y) < 0.5 {
					// Walking through a seated body (centres overlapping).
					violations++
				}
				checks++
			}
		}
		w.checkSane(t)
	}
	t.Logf("auditorium show end: %d seat checks, %d walkers through a seated body; %d left at %.0f s", checks, violations, len(w.agents), w.T-t0)
	if violations > 0 {
		t.Errorf("%d walkers passed through a seated neighbour", violations)
	}
	if len(w.agents) > 3 {
		t.Errorf("%d people still inside after 200 s", len(w.agents))
	}
	// Hand calculation: 300 people, four aisles (two 1.2 m, two 1.5 m =
	// 5.4 m) at 1.3 /(m·s) on tiers (~0.55 ×) ≈ 3.9 /s → 77 s of flow,
	// plus pre-movement and 20–30 m of walking: 100–200 s.
	if d := w.T - t0; d < 90 || d > 200 {
		t.Errorf("auditorium emptied in %.0f s, want 90–200", d)
	}
}

// TestAuditoriumIntermission: more than half go out to the lobby and come
// back when the show resumes; nobody is stuck in the end.
func TestAuditoriumIntermission(t *testing.T) {
	w := newVenue(t, "auditorium", 300, 6)
	w.vs.nextLeaver = 1e9
	if err := w.Apply(Action{Type: ActIntermission}); err != nil {
		t.Fatal(err)
	}
	peakOut := 0
	for w.T < 150 {
		w.Step()
		out := 0
		for _, a := range w.agents {
			if a.Y > 20.2 {
				out++
			}
		}
		peakOut = max(peakOut, out)
	}
	if err := w.Apply(Action{Type: ActCalm}); err != nil {
		t.Fatal(err)
	}
	for w.T < 400 {
		w.Step()
		w.checkSane(t)
	}
	seated, out := 0, 0
	for _, a := range w.agents {
		if a.p.sitting && a.p.phase == phIdle {
			seated++
		}
		if a.Y > 20.2 {
			out++
		}
	}
	t.Logf("intermission: %d of 300 in the lobby at the peak; %d seated and %d still in the lobby 250 s after the bell", peakOut, seated, out)
	if peakOut < 120 {
		t.Errorf("only %d went out", peakOut)
	}
	if seated < 280 {
		t.Errorf("only %d back in their seats", seated)
	}
	if len(w.agents) != 300 {
		t.Errorf("%d people (nobody leaves at an intermission)", len(w.agents))
	}
}

// TestBlockedExitReroutes: the auditorium evacuates; with one lobby door
// closed, nobody heads for it and the others take its share.
func TestBlockedExitReroutes(t *testing.T) {
	w := newVenue(t, "auditorium", 300, 7)
	w.vs.nextLeaver = 1e9
	closed := false
	if err := w.Apply(Action{Type: ActExit, ID: "door-cl", Open: &closed}); err != nil {
		t.Fatal(err)
	}
	if err := w.Apply(Action{Type: ActAlarm, Strength: fptr(0.4)}); err != nil {
		t.Fatal(err)
	}
	aimedClosed := 0
	through := map[string]int{}
	lobbySide := map[int]bool{}
	for w.T < 240 && len(w.agents) > 0 {
		w.Step()
		if w.ticks%25 != 0 {
			continue
		}
		for _, a := range w.agents {
			if a.p.target != nil && a.p.target.ID == "door-cl" {
				aimedClosed++
			}
			in := a.Y < 20.2
			if was, ok := lobbySide[a.ID]; ok && was && !in {
				// Which door: the nearest by x.
				best, bd := "", math.Inf(1)
				for _, e := range w.G.Exits {
					if e.Inner {
						if mx, _ := e.mid(); math.Abs(mx-a.X) < bd {
							best, bd = e.ID, math.Abs(mx-a.X)
						}
					}
				}
				through[best]++
			}
			lobbySide[a.ID] = in
		}
	}
	t.Logf("alarm with door-cl closed: %v through the lobby doors, %d left at %.0f s", through, len(w.agents), w.T)
	if aimedClosed > 0 {
		t.Errorf("%d person-samples aimed at the closed door", aimedClosed)
	}
	if through["door-cl"] > 3 {
		t.Errorf("%d went through the closed door", through["door-cl"])
	}
	if through["door-cr"] < 40 || through["door-l"] < 20 {
		t.Errorf("the open doors did not take the closed one's share: %v", through)
	}
	if len(w.agents) > 3 {
		t.Errorf("%d people left inside", len(w.agents))
	}
}

// TestGateQueueCrush: fans arrive faster than the turnstiles take them and
// the back pushes: the queue packs to a crush at the front (the truth goes
// dangerous), and the turnstiles' throughput is what the Green Guide says.
func TestGateQueueCrush(t *testing.T) {
	w := newVenue(t, "gate", 150, 8)
	through := 0
	last := len(w.agents)
	// Calm first: the turnstiles' flow alone.
	for w.T < 60 {
		w.Step()
		n := len(w.agents)
		through += last + int(math.Round(w.vs.arrivalCarry*0)) - n // arrivals are added before leave() in the same tick; count below instead
		last = n
	}
	// Count departures directly: everyone who is gone had a turnstile as target.
	c := newExitCounter()
	w.runFor(0)
	t1 := w.T
	for w.T-t1 < 60 {
		w.Step()
		c.tick(w)
	}
	gone := 0
	for _, n := range c.count {
		gone += n
	}
	rate := float64(gone) / 60
	t.Logf("gate: %d through four turnstiles in 60 s = %.2f/s (Green Guide 4 × 660/h = 0.73/s)", gone, rate)
	if rate < 0.5 || rate > 0.9 {
		t.Errorf("turnstile throughput %.2f/s, want 0.5–0.9", rate)
	}
	if w.Truth().DangerAt >= 0 {
		t.Errorf("dangerous already at %.0f s, with steady arrivals", w.Truth().DangerAt)
	}
	if err := w.Apply(Action{Type: ActRush}); err != nil {
		t.Fatal(err)
	}
	w.runFor(40)
	if err := w.Apply(Action{Type: ActSurge, Strength: fptr(0.8)}); err != nil {
		t.Fatal(err)
	}
	w.runFor(90)
	tr := w.Truth()
	pushing := 0
	for _, a := range w.agents {
		if a.p.push {
			pushing++
		}
	}
	t.Logf("gate after rush + surge: %d people, %d pushing, max pressure %.0f N/m, max density %.1f/m², dangerous at %.0f s", len(w.agents), pushing, tr.MaxPressure, tr.MaxDensity, tr.DangerAt)
	if tr.DangerAt < 0 {
		t.Errorf("the queue never became dangerous")
	}
	if pushing < 50 {
		t.Errorf("only %d pushing", pushing)
	}
	// Open the relief gate: the pressure comes off within a minute.
	open := true
	if err := w.Apply(Action{Type: ActExit, ID: "gate-relief", Open: &open}); err != nil {
		t.Fatal(err)
	}
	if err := w.Apply(Action{Type: ActCalm}); err != nil {
		t.Fatal(err)
	}
	before := len(w.agents)
	w.runFor(60)
	t.Logf("relief gate open, calm: %d → %d people in 60 s, max pressure now %.0f N/m", before, len(w.agents), w.Truth().MaxPressure)
	if len(w.agents) > before-40 {
		t.Errorf("the relief gate did not drain the queue: %d → %d", before, len(w.agents))
	}
}

// TestNobodyInFurniture: through a classroom dismissal and an auditorium
// evacuation nobody is ever inside a desk, on the stage or through a wall.
func TestNobodyInFurniture(t *testing.T) {
	for _, tc := range []struct {
		scn string
		n   int
		act Action
	}{{"classroom", 60, Action{Type: ActDismiss}}, {"auditorium", 400, Action{Type: ActAlarm}}} {
		w := newVenue(t, tc.scn, tc.n, 9)
		w.vs.nextLeaver = 1e9
		if err := w.Apply(tc.act); err != nil {
			t.Fatal(err)
		}
		for w.T < 150 && len(w.agents) > 0 {
			w.Step()
			if w.ticks%10 == 0 {
				w.checkSane(t)
			}
		}
	}
}

// checkSane fails the test if someone is inside a desk / the stage, deep in
// a wall, or outside the venue without being on the way out.
func (w *World) checkSane(t testing.TB) {
	for _, a := range w.agents {
		if a.X < -0.5 || a.Y < -0.5 || a.X > w.G.W+0.5 || a.Y > w.G.H+0.5 {
			t.Fatalf("t=%.1f person %d outside the venue at (%.2f,%.2f)", w.T, a.ID, a.X, a.Y)
		}
		if w.G.inStage(a.X, a.Y) {
			t.Fatalf("t=%.1f person %d on the stage at (%.2f,%.2f)", w.T, a.ID, a.X, a.Y)
		}
		for _, f := range w.vs.furniture {
			if f.Kind == "desk" || f.Kind == "counter" {
				if a.X > f.X0+0.05 && a.X < f.X1-0.05 && a.Y > f.Y0+0.05 && a.Y < f.Y1-0.05 {
					t.Fatalf("t=%.1f person %d inside the %s at (%.2f,%.2f)", w.T, a.ID, f.Kind, a.X, a.Y)
				}
			}
		}
	}
}

func fptr(v float64) *float64 { return &v }

// TestBodiesWireSize: what 500 simulated people cost per snapshot.
func TestBodiesWireSize(t *testing.T) {
	w := newVenue(t, "auditorium", 500, 10)
	b, err := json.Marshal(w.Bodies())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bodies JSON for %d people: %d bytes (%.1f per person)", len(w.agents), len(b), float64(len(b))/float64(len(w.agents)))
	if len(b) > 20000 {
		t.Errorf("bodies JSON %d bytes for 500 people", len(b))
	}
}
