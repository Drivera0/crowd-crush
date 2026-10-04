package locate

import (
	"os"
	"testing"
)

// With ideal phones (every position reported exactly, ten times a second)
// the estimator must change nothing: same positions, same phones counted,
// same alerts.
func TestIdealNotWorse(t *testing.T) {
	scripts := []simScript{scriptByName("stage→surge")}
	people := 120
	if os.Getenv("LOCATE_EVAL") == "ideal" {
		scripts, people = simScripts, 250
	} else if testing.Short() {
		t.Skip("short")
	}
	for _, seed := range seedsFromEnv("7") {
		for _, sc := range scripts {
			idealRun(t, sc, seed, people)
		}
	}
}

func idealRun(t *testing.T, sc simScript, seed int64, people int) {
	{
		outs := runSim(sc, seed, condIdeal, []variant{varRaw, varEst}, people)
		raw, est := outs[0], outs[1]
		re, ee := quant(raw.show.errs, 0.95), quant(est.show.errs, 0.95)
		t.Logf("%s: raw p95 %.3f m, estimator p95 %.3f m; counted %d / %d of %d; level %s / %s, red at %.1f / %.1f",
			sc.name, re, ee, raw.show.counted, est.show.counted, raw.show.phones, raw.maxLevel, est.maxLevel, raw.redAt, est.redAt)
		if ee > re+0.02 {
			t.Errorf("%s: estimator p95 error %.3f m, raw %.3f m", sc.name, ee, re)
		}
		if est.show.counted != raw.show.counted {
			t.Errorf("%s: estimator counts %d phones, raw %d", sc.name, est.show.counted, raw.show.counted)
		}
		if est.maxLevel != raw.maxLevel || est.redAt != raw.redAt {
			t.Errorf("%s: outcome changed: %s at %.1f, raw %s at %.1f", sc.name, est.maxLevel, est.redAt, raw.maxLevel, raw.redAt)
		}
		if est.show.tpPairs != raw.show.tpPairs || est.show.estPairs != raw.show.estPairs {
			t.Errorf("%s: neighbour pairs changed: %d of %d, raw %d of %d", sc.name, est.show.tpPairs, est.show.estPairs, raw.show.tpPairs, raw.show.estPairs)
		}
	}
}
