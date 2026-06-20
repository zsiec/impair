package profile

import (
	"bytes"
	"math"
	"testing"

	"github.com/zsiec/impair/internal/scenario"
	"github.com/zsiec/impair/internal/sim"
)

const seed = 1

// Every built-in profile must compile, build, and produce a non-empty pattern.
func TestProfilesBuildNonEmpty(t *testing.T) {
	trace := sim.SyntheticTrace(3000, 1_000_000)
	for name := range Profiles() {
		sc, err := Scenario(name, seed)
		if err != nil {
			t.Fatalf("%s: scenario: %v", name, err)
		}
		if len(sc.Pipeline) == 0 {
			t.Fatalf("%s: empty pipeline", name)
		}
		eng, err := scenario.Build(sc)
		if err != nil {
			t.Fatalf("%s: build: %v", name, err)
		}
		if got := sim.Run(eng, trace); len(got) == 0 {
			t.Fatalf("%s: empty pattern", name)
		}
	}
}

// Compiling + building the same profile/seed twice must yield byte-identical
// patterns (the P0 determinism property).
func TestDeterministicAcrossBuilds(t *testing.T) {
	trace := sim.SyntheticTrace(3000, 1_000_000)
	for name := range Profiles() {
		s1, _ := Scenario(name, seed)
		s2, _ := Scenario(name, seed)
		e1, err1 := scenario.Build(s1)
		e2, err2 := scenario.Build(s2)
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: build err: %v / %v", name, err1, err2)
		}
		if sim.Run(e1, trace) != sim.Run(e2, trace) {
			t.Fatalf("%s: pattern not deterministic across builds", name)
		}
	}
}

// Different seeds should generally produce different realized patterns for an
// impaired profile (sanity that the seed actually threads through).
func TestSeedAffectsImpairedPattern(t *testing.T) {
	trace := sim.SyntheticTrace(3000, 1_000_000)
	s1, _ := Scenario("g1050-D", 1)
	s2, _ := Scenario("g1050-D", 2)
	e1, _ := scenario.Build(s1)
	e2, _ := scenario.Build(s2)
	if sim.Run(e1, trace) == sim.Run(e2, trace) {
		t.Fatal("g1050-D: different seeds produced identical patterns")
	}
}

// The ladder must be monotonic in realized loss: a worse grade should drop at
// least as much as a better one, and the worst should clearly exceed pristine.
func TestLadderMonotonicLoss(t *testing.T) {
	trace := sim.SyntheticTrace(20000, 1_000_000)
	names := Names()
	if len(names) < 3 {
		t.Fatalf("expected >=3 profiles, got %d", len(names))
	}

	dropFrac := make([]float64, len(names))
	for i, name := range names {
		sc, _ := Scenario(name, seed)
		eng, err := scenario.Build(sc)
		if err != nil {
			t.Fatalf("%s: build: %v", name, err)
		}
		st := sim.RunStats(eng, trace)
		if st.Ingress == 0 {
			t.Fatalf("%s: no ingress", name)
		}
		dropFrac[i] = float64(st.Dropped) / float64(st.Ingress)
		t.Logf("%-8s grade order=%d dropFrac=%.4f reordered=%d", name, i, dropFrac[i], st.Reordered)
	}

	// Pristine (first) should be lossless.
	if dropFrac[0] != 0 {
		t.Fatalf("pristine profile %q dropped packets (frac=%.4f)", names[0], dropFrac[0])
	}
	// Worst should clearly lose more than pristine.
	if dropFrac[len(dropFrac)-1] <= dropFrac[0] {
		t.Fatalf("worst profile %q (frac=%.4f) did not exceed pristine (frac=%.4f)",
			names[len(names)-1], dropFrac[len(dropFrac)-1], dropFrac[0])
	}
	// Non-decreasing along the ladder (allow small statistical slack).
	for i := 1; i < len(dropFrac); i++ {
		if dropFrac[i]+0.005 < dropFrac[i-1] {
			t.Fatalf("loss not monotonic: %s(%.4f) < %s(%.4f)",
				names[i], dropFrac[i], names[i-1], dropFrac[i-1])
		}
	}
}

// Realized loss for each impaired profile should land in the right ballpark of
// its configured LossPct (loose bounds — burst loss + finite samples are noisy).
func TestRealizedLossInBallpark(t *testing.T) {
	trace := sim.SyntheticTrace(40000, 1_000_000)
	for _, name := range Names() {
		p, _ := Get(name)
		if p.LossPct == 0 {
			continue
		}
		sc, _ := Scenario(name, seed)
		eng, _ := scenario.Build(sc)
		st := sim.RunStats(eng, trace)
		got := 100 * float64(st.Dropped) / float64(st.Ingress)
		want := p.LossPct
		// Within a factor of ~2.5 in either direction.
		if got < want/2.5 || got > want*2.5 {
			t.Errorf("%s: realized loss %.2f%% far from configured %.2f%%", name, got, want)
		}
	}
}

// geParams must hit the GE steady-state loss target P/(P+R) for a given R.
func TestGEParamsHitTarget(t *testing.T) {
	cases := []struct {
		lossPct, r float64
	}{
		{1.0, 0.5}, {3.0, 0.35}, {8.0, 0.25},
	}
	for _, c := range cases {
		p, r := geParams(c.lossPct, c.r)
		steady := 100 * p / (p + r)
		if math.Abs(steady-c.lossPct) > 1e-9 {
			t.Errorf("geParams(%.2f,%.2f): steady=%.6f want %.6f", c.lossPct, c.r, steady, c.lossPct)
		}
		if r != c.r {
			t.Errorf("geParams kept R=%.4f want %.4f", r, c.r)
		}
	}
}

// Reorder-carrying profiles must actually produce reordered deliveries.
func TestReorderProfilesReorder(t *testing.T) {
	trace := sim.SyntheticTrace(20000, 1_000_000)
	for _, name := range Names() {
		p, _ := Get(name)
		if p.ReorderPct == 0 {
			continue
		}
		sc, _ := Scenario(name, seed)
		eng, _ := scenario.Build(sc)
		st := sim.RunStats(eng, trace)
		if st.Reordered == 0 {
			t.Errorf("%s: configured ReorderPct=%.1f but no reordering observed", name, p.ReorderPct)
		}
	}
}

// Names() must be sorted by grade, pristine first.
func TestNamesOrderedByGrade(t *testing.T) {
	names := Names()
	last := -1
	for _, n := range names {
		p, _ := Get(n)
		if p.Grade < last {
			t.Fatalf("Names() not grade-ordered at %q (grade %d < %d)", n, p.Grade, last)
		}
		last = p.Grade
	}
}

// Unknown profile names must error.
func TestScenarioUnknown(t *testing.T) {
	if _, err := Scenario("does-not-exist", seed); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

// A Profile must round-trip through JSON and compile to an identical-behavior
// scenario.
func TestJSONRoundTrip(t *testing.T) {
	trace := sim.SyntheticTrace(2000, 1_000_000)
	for _, name := range Names() {
		p, _ := Get(name)
		var buf bytes.Buffer
		if err := Save(&buf, p); err != nil {
			t.Fatalf("%s: save: %v", name, err)
		}
		got, err := Load(&buf)
		if err != nil {
			t.Fatalf("%s: load: %v", name, err)
		}
		e1, err1 := scenario.Build(p.Compile(seed))
		e2, err2 := scenario.Build(got.Compile(seed))
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: build err: %v / %v", name, err1, err2)
		}
		if sim.Run(e1, trace) != sim.Run(e2, trace) {
			t.Fatalf("%s: JSON round-trip changed behavior", name)
		}
	}
}

// Bernoulli profiles must emit a Loss stage, GE profiles a GE stage.
func TestLossModelMapsToStage(t *testing.T) {
	for _, name := range Names() {
		p, _ := Get(name)
		if p.LossPct == 0 {
			continue
		}
		sc := p.Compile(seed)
		var sawLoss, sawGE bool
		for _, st := range sc.Pipeline {
			if st.Loss != nil {
				sawLoss = true
			}
			if st.GE != nil {
				sawGE = true
			}
		}
		switch p.LossModel {
		case LossGE:
			if !sawGE || sawLoss {
				t.Errorf("%s: LossGE should emit GE stage only (ge=%v loss=%v)", name, sawGE, sawLoss)
			}
		default:
			if !sawLoss || sawGE {
				t.Errorf("%s: Bernoulli should emit Loss stage only (loss=%v ge=%v)", name, sawLoss, sawGE)
			}
		}
	}
}
