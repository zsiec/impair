package loss

import (
	"math"
	"testing"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/rng"
)

func sub(name string) *rng.Source { return rng.NewRoot(0xC0FFEE).Sub(name) }

func mkIn(seq uint64) engine.InFlight {
	return engine.InFlight{Seq: seq, Dir: engine.C2S, Data: []byte{1, 2, 3}, RecvAt: 1000, DeliverAt: 1000}
}

// run drives n packets through a cell and reports how many were dropped,
// plus the sequence of drop/keep outcomes (true == dropped).
func run(c engine.Cell, n int) (dropped int, outcomes []bool) {
	outcomes = make([]bool, n)
	for i := 0; i < n; i++ {
		out := c.Process(mkIn(uint64(i + 1)))
		if len(out) == 0 {
			dropped++
			outcomes[i] = true
		}
	}
	return
}

func TestNames(t *testing.T) {
	if got := New(Config{P: 0.1}, sub("b")).Name(); got != "loss.bernoulli" {
		t.Fatalf("bernoulli name = %q", got)
	}
	if got := NewGE(GEConfig{P: 0.1}, sub("g")).Name(); got != "loss.ge" {
		t.Fatalf("ge name = %q", got)
	}
}

func TestBernoulliBoundaries(t *testing.T) {
	// P <= 0 never drops; output forwards the packet unchanged.
	c := New(Config{P: 0}, sub("zero"))
	for i := 0; i < 1000; i++ {
		out := c.Process(mkIn(uint64(i)))
		if len(out) != 1 || out[0].Seq != uint64(i) {
			t.Fatalf("P=0 should forward unchanged, got %v", out)
		}
	}
	// Negative behaves like 0.
	cn := New(Config{P: -0.5}, sub("neg"))
	if d, _ := run(cn, 1000); d != 0 {
		t.Fatalf("P<0 dropped %d", d)
	}
	// P >= 1 always drops.
	c1 := New(Config{P: 1}, sub("one"))
	if d, _ := run(c1, 1000); d != 1000 {
		t.Fatalf("P>=1 dropped %d want 1000", d)
	}
}

func TestBernoulliRate(t *testing.T) {
	const n = 1_000_000
	for _, p := range []float64{0.01, 0.1, 0.3, 0.5} {
		c := New(Config{P: p}, sub("rate"))
		dropped, _ := run(c, n)
		got := float64(dropped) / n
		if math.Abs(got-p) > 0.01 {
			t.Errorf("P=%.2f: empirical loss %.4f (want within 0.01)", p, got)
		}
	}
}

func TestBernoulliDeterministic(t *testing.T) {
	c1 := New(Config{P: 0.2}, sub("det"))
	c2 := New(Config{P: 0.2}, sub("det"))
	_, o1 := run(c1, 50000)
	_, o2 := run(c2, 50000)
	for i := range o1 {
		if o1[i] != o2[i] {
			t.Fatalf("non-deterministic at %d", i)
		}
	}
}

func TestBernoulliForwardsUnchanged(t *testing.T) {
	c := New(Config{P: 0.5}, sub("unchanged"))
	in := mkIn(42)
	for i := 0; i < 1000; i++ {
		out := c.Process(in)
		if len(out) == 1 {
			f := out[0]
			if f.Seq != in.Seq || f.Dir != in.Dir || f.RecvAt != in.RecvAt || f.DeliverAt != in.DeliverAt {
				t.Fatalf("forwarded packet mutated: %+v vs %+v", f, in)
			}
		}
	}
}

func TestGEDefaults(t *testing.T) {
	// Default GE with P=0 stays in GOOD forever and (H defaults to 1) is
	// lossless.
	c := NewGE(GEConfig{P: 0}, sub("gedef"))
	if d, _ := run(c, 100000); d != 0 {
		t.Fatalf("default lossless GE dropped %d", d)
	}
}

// stationaryLoss is the theoretical long-run loss probability of a GE cell.
func stationaryLoss(p, r, h, k float64) float64 {
	pi0 := r / (p + r) // P(GOOD)
	pi1 := p / (p + r) // P(BAD)
	return pi0*(1-h) + pi1*(1-k)
}

func TestGEStationaryLossRate(t *testing.T) {
	const n = 1_000_000
	cases := []struct {
		name       string
		p, r, h, k float64
		explicitHK bool
	}{
		// Classic burst loss: lossless GOOD (H=1), total loss BAD (K=0).
		{name: "burst", p: 0.001, r: 0.05, h: 1, k: 0},
		{name: "burst2", p: 0.01, r: 0.1, h: 1, k: 0},
		// Leaky states: partial loss in both states.
		{name: "leaky", p: 0.02, r: 0.2, h: 0.99, k: 0.5, explicitHK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := GEConfig{P: tc.p, R: tc.r, H: tc.h, K: tc.k}
			if tc.explicitHK {
				cfg = cfg.WithH(tc.h).WithK(tc.k)
			}
			c := NewGE(cfg, sub("ge_"+tc.name))
			dropped, _ := run(c, n)
			got := float64(dropped) / n
			want := stationaryLoss(tc.p, tc.r, tc.h, tc.k)
			if math.Abs(got-want) > 0.01 {
				t.Errorf("loss=%.4f want=%.4f (within 0.01)", got, want)
			}
		})
	}
}

func TestGEMeanBurstLength(t *testing.T) {
	const n = 2_000_000
	// With H=1 (lossless GOOD) and K=0 (total loss BAD), a "loss burst" is
	// exactly a run in the BAD state, whose length is geometric with mean
	// 1/R.
	for _, r := range []float64{0.05, 0.1, 0.25} {
		c := NewGE(GEConfig{P: 0.01, R: r, H: 1, K: 0}, sub("burst"))
		_, outcomes := run(c, n)

		var bursts, totalLost int
		inBurst := false
		for _, lost := range outcomes {
			if lost {
				totalLost++
				if !inBurst {
					bursts++
					inBurst = true
				}
			} else {
				inBurst = false
			}
		}
		if bursts == 0 {
			t.Fatalf("R=%.2f produced no loss bursts", r)
		}
		mean := float64(totalLost) / float64(bursts)
		want := 1 / r
		// Generous relative tolerance.
		if math.Abs(mean-want) > 0.1*want+0.5 {
			t.Errorf("R=%.2f mean burst len=%.3f want~%.3f", r, mean, want)
		}
	}
}

func TestGEDeterministic(t *testing.T) {
	cfg := GEConfig{P: 0.05, R: 0.3, H: 0.9, K: 0.1}.WithH(0.9).WithK(0.1)
	c1 := NewGE(cfg, sub("gedet"))
	c2 := NewGE(cfg, sub("gedet"))
	_, o1 := run(c1, 100000)
	_, o2 := run(c2, 100000)
	for i := range o1 {
		if o1[i] != o2[i] {
			t.Fatalf("GE non-deterministic at %d", i)
		}
	}
}

func TestGERDefaultsToOne(t *testing.T) {
	// netem convention: an unset/zero R becomes 1 (single-packet bad
	// bursts). With P=1 the chain ping-pongs GOOD<->BAD every packet, and
	// with H=1/K=0 it loses on exactly the BAD steps -> ~half the packets.
	c := NewGE(GEConfig{P: 1, R: 0, H: 1}, sub("rdefault"))
	dropped, outcomes := run(c, 1000)
	if outcomes[0] {
		t.Fatalf("first packet evaluated in lossless GOOD should survive")
	}
	if dropped < 480 || dropped > 520 {
		t.Fatalf("R defaulting to 1 should drop ~500, got %d", dropped)
	}
}

func TestGEWithHZeroTotalLoss(t *testing.T) {
	// WithH(0) makes the GOOD state total-loss, and K default 0 makes BAD
	// total-loss, so every packet is dropped regardless of state.
	c := NewGE(GEConfig{P: 0.3, R: 0.3}.WithH(0), sub("alllost"))
	if d, _ := run(c, 10000); d != 10000 {
		t.Fatalf("H=0,K=0 should drop everything, got %d", d)
	}
}

func TestGEWithKExplicitZeroVsDefault(t *testing.T) {
	// WithK(0) must behave identically to leaving K unset (both mean total
	// loss in BAD).
	a := NewGE(GEConfig{P: 0.05, R: 0.2, H: 1}, sub("ksame"))
	b := NewGE(GEConfig{P: 0.05, R: 0.2, H: 1}.WithK(0), sub("ksame"))
	_, oa := run(a, 50000)
	_, ob := run(b, 50000)
	for i := range oa {
		if oa[i] != ob[i] {
			t.Fatalf("WithK(0) differs from default at %d", i)
		}
	}
}

func TestImplementsCell(t *testing.T) {
	var _ engine.Cell = New(Config{}, sub("a"))
	var _ engine.Cell = NewGE(GEConfig{}, sub("b"))
}
