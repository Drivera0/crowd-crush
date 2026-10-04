package app

import (
	"fmt"
	"math"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

func lineCands(n int, spacing float64) []meshCand {
	out := make([]meshCand, n)
	for i := range out {
		out[i] = meshCand{id: fmt.Sprintf("p%02d", i), x: float64(i) * spacing, y: 5, placed: true}
	}
	return out
}

func degrees(links map[meshPair]int64) map[string]int {
	d := map[string]int{}
	for pr := range links {
		d[pr[0]]++
		d[pr[1]]++
	}
	return d
}

func sameLinks(a, b map[meshPair]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func copyLinks(l map[meshPair]int64) map[meshPair]int64 {
	out := map[meshPair]int64{}
	for k, v := range l {
		out[k] = v
	}
	return out
}

func TestSelectPeersNearest(t *testing.T) {
	phones := lineCands(20, 1)
	links := selectPeers(phones, map[meshPair]int64{}, nil, 1000)
	pos := map[string]float64{}
	for _, p := range phones {
		pos[p.id] = p.x
	}
	deg := degrees(links)
	for _, p := range phones {
		if d := deg[p.id]; d < MeshPeers || d > MeshMaxPeers {
			t.Errorf("%s has %d links, want %d–%d", p.id, d, MeshPeers, MeshMaxPeers)
		}
	}
	for pr := range links {
		if d := math.Abs(pos[pr[0]] - pos[pr[1]]); d > 2*MeshPeers {
			t.Errorf("link %v spans %.0f m: not a near neighbour", pr, d)
		}
	}
	// Two phones alone get each other; one alone gets nobody.
	if l := selectPeers(lineCands(2, 30), map[meshPair]int64{}, nil, 0); len(l) != 1 {
		t.Errorf("two phones: %d links", len(l))
	}
	if l := selectPeers(lineCands(1, 1), map[meshPair]int64{}, nil, 0); len(l) != 0 {
		t.Errorf("one phone: %d links", len(l))
	}
}

func TestSelectPeersHysteresis(t *testing.T) {
	phones := lineCands(30, 1)
	links := selectPeers(phones, map[meshPair]int64{}, nil, 0)
	first := copyLinks(links)
	// People shuffle about by half a metre: nothing changes, tick after tick.
	for tick := 1; tick <= 30; tick++ {
		for i := range phones {
			phones[i].x += 0.5 * math.Sin(float64(tick*7+i*3))
			phones[i].y = 5 + 0.5*math.Cos(float64(tick*5+i))
		}
		selectPeers(phones, links, nil, int64(tick)*1000)
		for i := range phones {
			phones[i].x = float64(i)
		}
	}
	if !sameLinks(first, links) {
		t.Fatalf("links churned while people only shuffled: %d → %d links", len(first), len(links))
	}
	// One phone walks to the far end: its old links stay until they are old
	// enough, then it is linked to the people it now stands with.
	mover := phones[0].id
	phones[0].x = 29.5
	selectPeers(phones, links, nil, 31_000)
	if !sameLinks(first, links) {
		// It may gain links at once (it has room), but must not lose any yet.
		for pr := range first {
			if _, ok := links[pr]; !ok && first[pr] > 31_000-meshMinAgeMs {
				t.Fatalf("young link %v dropped", pr)
			}
		}
	}
	selectPeers(phones, links, nil, 31_000+meshMinAgeMs)
	selectPeers(phones, links, nil, 32_000+meshMinAgeMs)
	n := 0
	for pr := range links {
		if pr[0] != mover && pr[1] != mover {
			continue
		}
		other := pr[0]
		if other == mover {
			other = pr[1]
		}
		n++
		if other < "p20" {
			t.Errorf("after walking away %s is still linked to %s", mover, other)
		}
	}
	if n < 1 {
		t.Fatalf("the phone that moved has no links")
	}
}

func TestSelectPeersUnplacedBadAndGone(t *testing.T) {
	phones := lineCands(8, 1)
	phones = append(phones, meshCand{id: "u1"}, meshCand{id: "u2"})
	links := selectPeers(phones, map[meshPair]int64{}, nil, 0)
	deg := degrees(links)
	if deg["u1"] < 1 || deg["u2"] < 1 {
		t.Fatalf("unplaced phones got no peers: %v", deg)
	}
	// Stable from tick to tick (the random choice is a hash, not a dice roll).
	before := copyLinks(links)
	for tick := int64(1); tick < 40; tick++ {
		selectPeers(phones, links, nil, tick*1000)
	}
	if !sameLinks(before, links) {
		t.Fatal("links of unplaced phones churn")
	}
	// A pair that failed to connect is dropped and not tried again until it expires.
	var failed meshPair
	for pr := range links {
		failed = pr
		break
	}
	bad := map[meshPair]int64{failed: 100_000}
	selectPeers(phones, links, bad, 50_000)
	if _, ok := links[failed]; ok {
		t.Fatal("failed pair kept")
	}
	selectPeers(phones, links, bad, 60_000)
	if _, ok := links[failed]; ok {
		t.Fatal("failed pair tried again before it expired")
	}
	// A phone that left takes its links with it.
	selectPeers(phones[1:], links, nil, 70_000)
	for pr := range links {
		if pr[0] == "p00" || pr[1] == "p00" {
			t.Fatalf("link %v to a phone that left", pr)
		}
	}
}

// meshApp is an app with n phones in a row, all able to open WebRTC links.
func meshApp(t *testing.T, n int) (*App, []string) {
	t.Helper()
	a := demoApp(t)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("p%02d", i)
		a.PhoneHello(ids[i], 2+float64(i), 5, "test")
		a.PhoneSync(ids[i], 0, 20)
		a.PhoneRTC(ids[i], true)
	}
	return a, ids
}

// reportLinks makes every phone report an open link with each peer the
// server gave it.
func reportLinks(a *App) {
	a.mu.Lock()
	peers := map[string][]protocol.NearPeer{}
	for pr := range a.ms().links {
		peers[pr[0]] = append(peers[pr[0]], protocol.NearPeer{ID: pr[1], Corr: 0.7, LagMs: 100, RTT: 12})
		peers[pr[1]] = append(peers[pr[1]], protocol.NearPeer{ID: pr[0], Corr: 0.7, LagMs: -100, RTT: 12})
	}
	a.mu.Unlock()
	for id, ps := range peers {
		a.PhoneNear(id, protocol.Near{Type: protocol.TypeNear, Peers: ps, Tx: 1000, Rx: 900, Known: 5})
	}
}

func TestMeshTickTellsEachPhoneItsPeers(t *testing.T) {
	a, ids := meshApp(t, 6)
	now := hub.Now()
	a.mu.Lock()
	out := a.meshTickLocked(now)
	again := a.meshTickLocked(now + 1000)
	a.mu.Unlock()
	if len(out) != len(ids) {
		t.Fatalf("%d phones told their peers, want %d", len(out), len(ids))
	}
	if len(again) != 0 {
		t.Fatalf("unchanged lists sent again after 1 s: %d", len(again))
	}
	init := map[meshPair]int{}
	for _, o := range out {
		if o.msg.Me != hub.Handle(o.id) || len(o.msg.Peers) == 0 || len(o.msg.ICE) == 0 {
			t.Fatalf("message for %s: %+v", o.id, o.msg)
		}
		for _, p := range o.msg.Peers {
			if len(p.ID) != 12 {
				t.Fatalf("peer named %q: phones must only see handles", p.ID)
			}
			if p.Init {
				init[pairOf(o.msg.Me, p.ID)]++
			}
		}
	}
	a.mu.Lock()
	links := len(a.ms().links)
	a.mu.Unlock()
	if len(init) != links {
		t.Fatalf("%d of %d links have an offering side", len(init), links)
	}
	for pr, n := range init {
		if n != 1 {
			t.Fatalf("link %v: %d offering sides", pr, n)
		}
	}
	if !a.MeshPair(ids[0], ids[1]) || a.MeshPair(ids[0], "nobody") {
		t.Fatal("MeshPair")
	}
	// A phone reports it could not reach a peer: the pair is dropped.
	a.PhoneNear(ids[0], protocol.Near{Failed: []string{ids[1]}})
	a.mu.Lock()
	a.meshTickLocked(now + 2000)
	a.mu.Unlock()
	if a.MeshPair(ids[0], ids[1]) {
		t.Fatal("failed pair still assigned")
	}
}

func TestMeshFrameAndEvidence(t *testing.T) {
	a, ids := meshApp(t, 5)
	now := hub.Now()
	a.meshTick(now)
	reportLinks(a)
	hops := 2
	a.PhoneMPos(ids[0], protocol.MPos{X: 2.2, Y: 5.1, Acc: 1.4, Hops: &hops, N: 3, Src: "gps"})
	a.PhoneMPos(ids[1], protocol.MPos{X: math.NaN(), Y: 1, Acc: 1})
	a.PhoneRoute(ids[0], ids[1], 1)
	a.PhoneJam(ids[0], true)
	a.PhoneMotion(ids[0], protocol.Motion{T: now, AX: 0.1}, now)

	a.mu.Lock()
	s := a.snapshotLocked(now)
	a.mu.Unlock()
	f := s.Mesh
	if f == nil {
		t.Fatal("no mesh frame")
	}
	a.mu.Lock()
	want := len(a.ms().links)
	a.mu.Unlock()
	if len(f.Links) != want || want == 0 {
		t.Fatalf("%d links in the frame, %d assigned", len(f.Links), want)
	}
	for _, l := range f.Links {
		if l[0] < 0 || l[1] >= len(s.Nodes) || l[0] >= l[1] {
			t.Fatalf("link %v is not a pair of node indexes", l)
		}
	}
	if f.Phones != 5 || f.Reporting != 5 || f.ViaMesh != 1 {
		t.Fatalf("counter: %d phones, %d reporting, %d via the mesh", f.Phones, f.Reporting, f.ViaMesh)
	}
	var n0 *protocol.MeshNode
	for i := range f.Nodes {
		if f.Nodes[i].ID == ids[0] {
			n0 = &f.Nodes[i]
		}
		if f.Nodes[i].ID == ids[1] && f.Nodes[i].Pos != nil {
			t.Fatal("a NaN position was kept")
		}
	}
	if n0 == nil || n0.Via != ids[1] || n0.Hops != 1 || !n0.Jam || n0.Peers == 0 || n0.RTT != 12 || n0.Tx != 1000 {
		t.Fatalf("node 0: %+v", n0)
	}
	if n0.Pos == nil || n0.Pos.X != 2.2 || n0.Pos.Acc != 1.4 || *n0.Pos.Hops != 2 || n0.Pos.N != 3 {
		t.Fatalf("node 0 position: %+v", n0.Pos)
	}
	ev := a.NearEvidence()
	if len(ev) != 5 || len(ev[ids[2]]) == 0 || ev[ids[2]][0].Hops != 1 || ev[ids[2]][0].Corr != 0.7 {
		t.Fatalf("evidence: %v", ev)
	}
	if mp := a.MeshPositions(); len(mp) != 1 || mp[ids[0]].X != 2.2 {
		t.Fatalf("mesh positions: %v", mp)
	}
	// Back on its own socket: no longer relayed or jammed.
	a.PhoneRoute(ids[0], "", 0)
	a.mu.Lock()
	f = a.meshFrameLocked(a.live, s.Nodes, now)
	a.mu.Unlock()
	for _, n := range f.Nodes {
		if n.ID == ids[0] && (n.Via != "" || n.Jam) {
			t.Fatalf("still relayed after reconnecting: %+v", n)
		}
	}
	if f.ViaMesh != 0 {
		t.Fatalf("via the mesh: %d", f.ViaMesh)
	}
}

func TestJam(t *testing.T) {
	a, ids := meshApp(t, 8)
	if err := a.JamPhone("nobody", true); err != ErrNoPhone {
		t.Fatalf("unknown phone: %v", err)
	}
	// No link reported yet: nothing to fall back on.
	if err := a.JamPhone(ids[0], true); err != errJamNoLink {
		t.Fatalf("jam without a link: %v", err)
	}
	if got := a.JamHalf(true); len(got) != 0 {
		t.Fatalf("jammed %d phones that have no links", len(got))
	}
	a.meshTick(hub.Now())
	reportLinks(a)
	if err := a.JamPhone(ids[0], true); err != nil {
		t.Fatalf("jam: %v", err)
	}
	jam := a.JamHalf(true)
	if len(jam) != len(ids)/2 {
		t.Fatalf("jammed %d of %d", len(jam), len(ids))
	}
	// Every jammed phone keeps a neighbour that still has its own connection.
	isJam := map[string]bool{}
	for _, id := range jam {
		isJam[id] = true
	}
	ev := a.NearEvidence()
	for _, id := range jam {
		ok := false
		for _, p := range ev[id] {
			ok = ok || !isJam[p.ID]
		}
		if !ok {
			t.Fatalf("%s would be jammed with no connected neighbour", id)
		}
	}
	// The phones answer (hub: jam on = it is leaving its socket).
	for _, id := range jam {
		a.PhoneJam(id, true)
	}
	if st := a.MeshStatus(); len(st.Jammed) != len(jam) || st.Links == 0 || st.TxBytes != 8000 {
		t.Fatalf("status: %+v", st)
	}
	// A second round never jams the rest: they are what the jammed ones rely on.
	if more := a.JamHalf(true); len(more) > (len(ids)-len(jam))/2 {
		t.Fatalf("second round jammed %d more", len(more))
	}
	if off := a.JamHalf(false); len(off) != len(jam) {
		t.Fatalf("restore told %d phones, want %d", len(off), len(jam))
	}
	// One refuses (no route): the flag clears.
	a.PhoneJam(jam[0], false)
	if st := a.MeshStatus(); len(st.Jammed) != len(jam)-1 {
		t.Fatalf("after a refusal: %v", st.Jammed)
	}
}

// Simulated phones have no browsers: while a simulation is on screen they
// get stand-in links (picked like real ones) and the detector's own pair
// correlations as evidence.
func TestSimVirtualMesh(t *testing.T) {
	r := startSim(t, 80, 1)
	r.until(3, nil)
	r.a.mu.Lock()
	s := r.a.snapshotLocked(r.now)
	ev := r.a.nearLocked(r.a.active(), r.now)
	r.a.mu.Unlock()
	if s.Mesh == nil || !s.Mesh.Virtual || len(s.Mesh.Links) < len(s.Nodes) {
		t.Fatalf("virtual mesh: %+v for %d nodes", s.Mesh, len(s.Nodes))
	}
	if s.Mesh.Phones != 0 || len(s.Mesh.Nodes) != 0 {
		t.Fatalf("simulated phones counted as real ones: %+v", s.Mesh)
	}
	if len(ev) < len(s.Nodes)/2 {
		t.Fatalf("evidence for %d of %d simulated phones", len(ev), len(s.Nodes))
	}
	for id, ps := range ev {
		if len(ps) > MeshMaxPeers {
			t.Fatalf("%s has %d stand-in links", id, len(ps))
		}
		for _, p := range ps {
			if p.Hops != 1 || p.Corr < 0 || p.Corr > 1 || p.ID == id {
				t.Fatalf("evidence %+v", p)
			}
		}
	}
	// Back to live: the stand-in links go.
	if err := r.a.StopSim(); err != nil {
		t.Fatal(err)
	}
	r.a.mu.Lock()
	s = r.a.snapshotLocked(r.now)
	r.a.mu.Unlock()
	if s.Mesh == nil || s.Mesh.Virtual || len(s.Mesh.Links) != 0 {
		t.Fatalf("after the simulation: %+v", s.Mesh)
	}
}
