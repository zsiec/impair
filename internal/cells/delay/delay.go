// Package delay implements a delay + jitter impairment Cell.
//
// Each packet's DeliverAt is advanced by a fixed Base delay plus a random
// jitter term drawn from a configurable Distribution. Jitter draws may be
// correlated with the previous draw (netem-style), and the result is always
// clamped so DeliverAt never precedes the immutable RecvAt.
//
// The cell is deterministic: its only source of randomness is the *rng.Source
// supplied to New. Given the same input sequence and Source, it produces a
// byte-identical DeliverAt stream on every run and machine.
package delay

import (
	"math"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/rng"
)

// Distribution selects the shape of the jitter term added to Base.
type Distribution uint8

const (
	// None adds no jitter (constant Base delay). This is the zero value, so a
	// zero Config yields a fixed-delay cell.
	None Distribution = iota
	// Uniform draws jitter uniformly from [-Jitter, +Jitter].
	Uniform
	// Normal draws jitter from a Gaussian with mean 0 and standard deviation
	// Sigma, clamped to +/- clampSigma standard deviations.
	Normal
	// Pareto draws a heavy-tailed, non-negative jitter (only ever adds delay).
	// Sigma is used as the scale (xm); shape is fixed at paretoShape.
	Pareto
)

func (d Distribution) String() string {
	switch d {
	case None:
		return "none"
	case Uniform:
		return "uniform"
	case Normal:
		return "normal"
	case Pareto:
		return "pareto"
	default:
		return "?"
	}
}

const (
	// clampSigma bounds Normal draws to a few standard deviations so a single
	// extreme tail value cannot produce an absurd delay.
	clampSigma = 4.0
	// paretoShape (alpha) gives the Pareto distribution a finite mean
	// (alpha > 1) while remaining visibly heavy-tailed.
	paretoShape = 3.0
	// paretoTailClamp bounds the Pareto multiplier so the heavy tail cannot
	// emit a pathological delay; jitter is capped at scale*paretoTailClamp.
	paretoTailClamp = 50.0
)

// Config configures a delay Cell.
//
// The zero value is a valid no-op-ish cell: Base 0, Distribution None,
// Correlation 0 — it leaves DeliverAt unchanged.
type Config struct {
	// Base is the fixed delay added to every packet, in nanoseconds.
	// Negative values are treated as 0.
	Base int64
	// Jitter is the half-range for the Uniform distribution, in nanoseconds.
	Jitter int64
	// Sigma is the standard deviation for Normal, or the scale (xm) for
	// Pareto, in nanoseconds.
	Sigma int64
	// Distribution selects the jitter model.
	Distribution Distribution
	// Correlation in [0,1) blends the current jitter draw with the previous
	// one (netem-style): jitter = corr*prev + (1-corr)*draw. Values outside
	// [0,1) are clamped into range.
	Correlation float64
}

// Cell is a delay + jitter impairment stage.
type Cell struct {
	cfg      Config
	src      *rng.Source
	prevJit  float64
	havePrev bool
}

// New returns a delay Cell using cfg and src as its only randomness source.
// src may be nil only if cfg.Distribution is None (no draws are ever taken).
func New(cfg Config, src *rng.Source) engine.Cell {
	if cfg.Base < 0 {
		cfg.Base = 0
	}
	if cfg.Jitter < 0 {
		cfg.Jitter = -cfg.Jitter
	}
	if cfg.Sigma < 0 {
		cfg.Sigma = -cfg.Sigma
	}
	if cfg.Correlation < 0 {
		cfg.Correlation = 0
	}
	if cfg.Correlation >= 1 {
		// Keep strictly < 1 so correlation can never fully freeze the draw.
		cfg.Correlation = math.Nextafter(1, 0)
	}
	return &Cell{cfg: cfg, src: src}
}

// Name implements engine.Cell.
func (c *Cell) Name() string { return "delay" }

// Process advances in.DeliverAt by Base + jitter and returns the single packet.
// It never drops or duplicates, and never sets DeliverAt below in.RecvAt.
func (c *Cell) Process(in engine.InFlight) []engine.InFlight {
	jit := c.nextJitter()

	// Round-to-nearest conversion of the (possibly fractional ns) jitter.
	add := c.cfg.Base + int64(math.Round(jit))
	in.DeliverAt += add

	// Hard floor: delivery may never precede ingress.
	if in.DeliverAt < in.RecvAt {
		in.DeliverAt = in.RecvAt
	}
	return []engine.InFlight{in}
}

// nextJitter returns the next (correlated) jitter value in nanoseconds.
func (c *Cell) nextJitter() float64 {
	draw := c.drawJitter()

	corr := c.cfg.Correlation
	if corr > 0 && c.havePrev {
		draw = corr*c.prevJit + (1-corr)*draw
	}
	c.prevJit = draw
	c.havePrev = true
	return draw
}

// drawJitter samples one raw jitter value (pre-correlation) per Distribution.
func (c *Cell) drawJitter() float64 {
	switch c.cfg.Distribution {
	case Uniform:
		if c.cfg.Jitter == 0 {
			return 0
		}
		// u in [0,1) -> [-J, +J).
		u := c.src.Float64()
		return (2*u - 1) * float64(c.cfg.Jitter)
	case Normal:
		if c.cfg.Sigma == 0 {
			return 0
		}
		z := c.normal()
		return z * float64(c.cfg.Sigma)
	case Pareto:
		if c.cfg.Sigma == 0 {
			return 0
		}
		return c.pareto() * float64(c.cfg.Sigma)
	default: // None
		return 0
	}
}

// normal returns a standard-normal sample via Box-Muller, clamped to
// +/- clampSigma.
func (c *Cell) normal() float64 {
	// Avoid log(0) by drawing u1 in (0,1].
	u1 := 1 - c.src.Float64()
	u2 := c.src.Float64()
	z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
	if z > clampSigma {
		z = clampSigma
	} else if z < -clampSigma {
		z = -clampSigma
	}
	return z
}

// pareto returns a non-negative Pareto(shape=paretoShape, xm=1) excess sample
// in [0, paretoTailClamp]. The "excess" form (value-1) means the typical
// contribution is small but the tail can add a large delay.
func (c *Cell) pareto() float64 {
	// Inverse-CDF: x = xm / U^(1/alpha), U in (0,1].
	u := 1 - c.src.Float64() // (0,1]
	x := 1 / math.Pow(u, 1/paretoShape)
	x -= 1 // shift so the minimum jitter is 0, not xm.
	if x > paretoTailClamp {
		x = paretoTailClamp
	}
	return x
}
