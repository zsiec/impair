package delay

import (
	"math"
	"testing"

	"github.com/zsiec/impair/internal/engine"
	"github.com/zsiec/impair/internal/rng"
)

func sub(name string) *rng.Source { return rng.NewRoot(0xD1A7).Sub(name) }

// mkIn builds an InFlight at a fixed virtual ingress time.
func mkIn(seq uint64, recvAt int64) engine.InFlight {
	return engine.InFlight{Seq: seq, Dir: engine.C2S, Data: []byte{byte(seq)}, RecvAt: recvAt, DeliverAt: recvAt}
}

// run pushes n packets (all ingressing at recvAt) and returns the added delay
// (DeliverAt - RecvAt) for each.
func run(c engine.Cell, n int, recvAt int64) []int64 {
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		res := c.Process(mkIn(uint64(i), recvAt))
		if len(res) != 1 {
			panic("delay must return exactly one packet")
		}
		out[i] = res[0].DeliverAt - res[0].RecvAt
	}
	return out
}

func meanStd(xs []int64) (mean, std float64) {
	for _, x := range xs {
		mean += float64(x)
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		d := float64(x) - mean
		std += d * d
	}
	std = math.Sqrt(std / float64(len(xs)))
	return
}

func TestName(t *testing.T) {
	c := New(Config{}, nil)
	if c.Name() != "delay" {
		t.Fatalf("Name = %q, want delay", c.Name())
	}
}

func TestNeverDropsOrDuplicates(t *testing.T) {
	c := New(Config{Base: 1000, Jitter: 5000, Distribution: Uniform}, sub("a"))
	for i := 0; i < 1000; i++ {
		res := c.Process(mkIn(uint64(i), 12345))
		if len(res) != 1 {
			t.Fatalf("got %d outputs, want exactly 1", len(res))
		}
		if res[0].Seq != uint64(i) {
			t.Fatalf("seq mutated: got %d want %d", res[0].Seq, i)
		}
	}
}

func TestZeroConfigIsNoOp(t *testing.T) {
	c := New(Config{}, nil)
	in := mkIn(1, 999)
	out := c.Process(in)[0]
	if out.DeliverAt != in.DeliverAt {
		t.Fatalf("zero config changed DeliverAt: %d -> %d", in.DeliverAt, out.DeliverAt)
	}
}

func TestFixedBaseDelay(t *testing.T) {
	const base = 30 * 1_000_000 // 30ms
	c := New(Config{Base: base}, nil)
	ds := run(c, 100, 1_000)
	for i, d := range ds {
		if d != base {
			t.Fatalf("packet %d: added %d, want %d", i, d, base)
		}
	}
}

func TestProcessAdvancesFromDeliverAtNotRecvAt(t *testing.T) {
	// If a prior cell already pushed DeliverAt forward, Base must stack on it.
	c := New(Config{Base: 100}, nil)
	in := engine.InFlight{Seq: 1, RecvAt: 1000, DeliverAt: 5000}
	out := c.Process(in)[0]
	if out.DeliverAt != 5100 {
		t.Fatalf("DeliverAt = %d, want 5100 (5000+100)", out.DeliverAt)
	}
}

func TestNeverBeforeRecvAt(t *testing.T) {
	// Large negative-capable jitter (Uniform) with zero base; RecvAt high so any
	// negative jitter would underflow without the clamp.
	c := New(Config{Jitter: 1_000_000, Distribution: Uniform}, sub("clamp"))
	const recvAt = 10_000
	for i := 0; i < 5000; i++ {
		out := c.Process(mkIn(uint64(i), recvAt))[0]
		if out.DeliverAt < out.RecvAt {
			t.Fatalf("packet %d: DeliverAt %d < RecvAt %d", i, out.DeliverAt, out.RecvAt)
		}
	}
}

func TestMeanAddedDelayApproxBase(t *testing.T) {
	// Symmetric jitter => mean added delay ~= Base (with enough headroom above
	// RecvAt that the clamp does not bias the mean).
	const base = 50 * 1_000_000
	const jit = 5 * 1_000_000
	c := New(Config{Base: base, Jitter: jit, Distribution: Uniform}, sub("mean"))
	ds := run(c, 200_000, 0)
	mean, _ := meanStd(ds)
	if math.Abs(mean-base) > 0.02*base {
		t.Fatalf("mean added delay %.0f deviates from base %d by >2%%", mean, base)
	}
}

func TestUniformSpread(t *testing.T) {
	const base = 100 * 1_000_000
	const jit = 10 * 1_000_000
	c := New(Config{Base: base, Jitter: jit, Distribution: Uniform}, sub("uni"))
	ds := run(c, 200_000, 0)
	mean, std := meanStd(ds)
	if math.Abs(mean-base) > 0.02*base {
		t.Fatalf("uniform mean %.0f, want ~%d", mean, base)
	}
	// Uniform[-J,J] has std = J/sqrt(3).
	wantStd := float64(jit) / math.Sqrt(3)
	if rel := math.Abs(std-wantStd) / wantStd; rel > 0.05 {
		t.Fatalf("uniform std %.0f, want ~%.0f (rel err %.3f)", std, wantStd, rel)
	}
	// Range should be within [-J, +J] of base.
	for _, d := range ds {
		off := d - base
		if off < -jit || off >= jit {
			t.Fatalf("uniform offset %d out of [-J,J)", off)
		}
	}
}

func TestNormalSpread(t *testing.T) {
	const base = 100 * 1_000_000
	const sigma = 8 * 1_000_000
	c := New(Config{Base: base, Sigma: sigma, Distribution: Normal}, sub("norm"))
	ds := run(c, 200_000, 0)
	mean, std := meanStd(ds)
	if math.Abs(mean-base) > 0.02*base {
		t.Fatalf("normal mean %.0f, want ~%d", mean, base)
	}
	// Clamping at 4 sigma trims the std only marginally (<0.1%).
	if rel := math.Abs(std-float64(sigma)) / float64(sigma); rel > 0.05 {
		t.Fatalf("normal std %.0f, want ~%d (rel err %.3f)", std, sigma, rel)
	}
	// Clamp: no sample beyond 4*sigma.
	for _, d := range ds {
		if off := math.Abs(float64(d - base)); off > clampSigma*sigma+1 {
			t.Fatalf("normal offset %.0f exceeds clamp %v*sigma", off, clampSigma)
		}
	}
}

func TestParetoHeavyTailNonNegative(t *testing.T) {
	const base = 10 * 1_000_000
	const scale = 2 * 1_000_000
	c := New(Config{Base: base, Sigma: scale, Distribution: Pareto}, sub("par"))
	ds := run(c, 200_000, 0)
	maxOff := int64(math.MinInt64)
	var sumOff float64
	for _, d := range ds {
		off := d - base
		if off < 0 {
			t.Fatalf("pareto produced negative jitter offset %d", off)
		}
		if off > maxOff {
			maxOff = off
		}
		sumOff += float64(off)
	}
	meanOff := sumOff / float64(len(ds))
	// Excess-Pareto(alpha=3) mean = xm/(alpha-1) = scale/2.
	wantMean := float64(scale) / (paretoShape - 1)
	if rel := math.Abs(meanOff-wantMean) / wantMean; rel > 0.10 {
		t.Fatalf("pareto mean offset %.0f, want ~%.0f (rel %.3f)", meanOff, wantMean, rel)
	}
	// Heavy tail: max offset should comfortably exceed the mean.
	if float64(maxOff) < 5*meanOff {
		t.Fatalf("pareto tail too light: max %d vs mean %.0f", maxOff, meanOff)
	}
}

// autocorr returns the lag-1 sample autocorrelation of the added-delay series.
func autocorr(xs []int64) float64 {
	mean, std := meanStd(xs)
	if std == 0 {
		return 0
	}
	var cov float64
	for i := 1; i < len(xs); i++ {
		cov += (float64(xs[i]) - mean) * (float64(xs[i-1]) - mean)
	}
	cov /= float64(len(xs) - 1)
	return cov / (std * std)
}

func TestCorrelationIncreasesAutocorrelation(t *testing.T) {
	const base = 100 * 1_000_000
	const jit = 10 * 1_000_000
	const n = 200_000

	uncorr := New(Config{Base: base, Jitter: jit, Distribution: Uniform, Correlation: 0}, sub("ac0"))
	corr := New(Config{Base: base, Jitter: jit, Distribution: Uniform, Correlation: 0.8}, sub("ac8"))

	acUncorr := autocorr(run(uncorr, n, 0))
	acCorr := autocorr(run(corr, n, 0))

	if math.Abs(acUncorr) > 0.05 {
		t.Fatalf("uncorrelated lag-1 autocorr %.3f, want ~0", acUncorr)
	}
	if acCorr < 0.5 {
		t.Fatalf("correlated lag-1 autocorr %.3f, want clearly positive", acCorr)
	}
	if acCorr <= acUncorr {
		t.Fatalf("correlation did not raise autocorr: %.3f <= %.3f", acCorr, acUncorr)
	}
}

func TestCorrelationClampedBelowOne(t *testing.T) {
	// Correlation >= 1 must be clamped strictly below 1 so the blend always
	// admits a nonzero fraction of each fresh draw (never fully frozen).
	c := New(Config{Base: 0, Jitter: 1_000_000, Distribution: Uniform, Correlation: 1.5}, sub("c1")).(*Cell)
	if c.cfg.Correlation >= 1 {
		t.Fatalf("correlation not clamped below 1: %v", c.cfg.Correlation)
	}
	if 1-c.cfg.Correlation <= 0 {
		t.Fatalf("clamped correlation leaves no room for fresh draws: %v", c.cfg.Correlation)
	}
}

func TestCorrelationBlendNeverFrozen(t *testing.T) {
	// At a high (but realistic) correlation the underlying float jitter must
	// keep drifting with each fresh draw rather than locking to one value.
	c := New(Config{Base: 0, Jitter: 1_000_000, Distribution: Uniform, Correlation: 0.95}, sub("blend")).(*Cell)
	seen := map[float64]bool{}
	for i := 0; i < 1000; i++ {
		seen[c.nextJitter()] = true
	}
	if len(seen) < 100 {
		t.Fatalf("blended jitter appears frozen: only %d distinct values", len(seen))
	}
}

func TestDeterminism(t *testing.T) {
	cfg := Config{Base: 7_000_000, Sigma: 3_000_000, Distribution: Normal, Correlation: 0.3}
	a := run(New(cfg, sub("det")), 5000, 1234)
	b := run(New(cfg, sub("det")), 5000, 1234)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at %d: %d != %d", i, a[i], b[i])
		}
	}
}

func TestNegativeConfigNormalized(t *testing.T) {
	c := New(Config{Base: -5, Jitter: -10, Sigma: -20, Correlation: -0.5}, sub("neg")).(*Cell)
	if c.cfg.Base != 0 {
		t.Fatalf("negative Base not clamped to 0: %d", c.cfg.Base)
	}
	if c.cfg.Jitter != 10 {
		t.Fatalf("negative Jitter not made positive: %d", c.cfg.Jitter)
	}
	if c.cfg.Sigma != 20 {
		t.Fatalf("negative Sigma not made positive: %d", c.cfg.Sigma)
	}
	if c.cfg.Correlation != 0 {
		t.Fatalf("negative Correlation not clamped to 0: %v", c.cfg.Correlation)
	}
}

func TestDataNotAliasedConcern(t *testing.T) {
	// Delay never mutates or copies Data; it must pass the same bytes through
	// untouched (no corruption responsibility here).
	orig := []byte{1, 2, 3}
	in := engine.InFlight{Seq: 1, Data: orig, RecvAt: 0, DeliverAt: 0}
	out := New(Config{Base: 5}, nil).Process(in)[0]
	if &out.Data[0] != &orig[0] {
		t.Fatal("delay unexpectedly copied/replaced Data slice")
	}
}
