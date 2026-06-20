// Package reorder implements a netem-style reorder + duplication impairment
// Cell. It models Linux tc-netem's `reorder` (which works in conjunction with a
// base delay): with probability ReorderPct a packet is sent "now" (its
// DeliverAt is left unchanged at its arrival time at this stage), jumping ahead
// of its delayed peers, while every other packet has a fixed Gap added to its
// DeliverAt. An optional correlation makes
// the reorder decision sticky across successive packets, like netem's
// correlation parameter. Independently, with probability DupPct the packet is
// emitted twice (an independent byte copy), modelling netem's `duplicate`.
//
// Like every Impair cell it is deterministic: its only entropy source is the
// *rng.Source handed to New, so a given (seed, input trace) yields a
// byte-identical output stream on every run and every machine.
package reorder

import (
	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/rng"
)

// Config parameterises the reorder/duplication cell. The zero value is a no-op:
// ReorderPct and DupPct default to 0, so every packet passes through unchanged.
type Config struct {
	// ReorderPct is the probability in [0,1] that a packet is reordered, i.e.
	// delivered immediately (DeliverAt left at its arrival time at this stage)
	// while non-reordered packets are pushed back by Gap.
	ReorderPct float64
	// Gap is the delay (ns) added to a packet that is NOT selected for
	// reordering. In netem this is the base delay the reordered packet jumps
	// ahead of. Negative values are clamped to 0.
	Gap int64
	// Correlation is the netem-style correlation in [0,1] applied to the
	// reorder decision: with this probability the previous packet's reorder
	// outcome is reused instead of drawing a fresh one. 0 means independent.
	Correlation float64
	// DupPct is the probability in [0,1] that a packet is duplicated (emitted
	// twice with independent copies of Data).
	DupPct float64
}

// Reorder is a stateful reorder + duplication Cell. Construct it with New.
type Reorder struct {
	cfg Config
	src *rng.Source

	hasPrev  bool // whether a previous reorder decision exists (for correlation)
	prevReor bool // the previous reorder decision
}

// New returns a reorder/duplication Cell driven solely by src. Out-of-range
// probabilities are clamped to [0,1] and a negative Gap to 0 so callers cannot
// produce a packet delivered before it was received.
func New(cfg Config, src *rng.Source) engine.Cell {
	cfg.ReorderPct = clamp01(cfg.ReorderPct)
	cfg.Correlation = clamp01(cfg.Correlation)
	cfg.DupPct = clamp01(cfg.DupPct)
	if cfg.Gap < 0 {
		cfg.Gap = 0
	}
	return &Reorder{cfg: cfg, src: src}
}

// Name implements engine.Cell.
func (r *Reorder) Name() string { return "reorder" }

// Process applies reorder then duplication semantics.
//
//   - Reorder: if the packet is selected for reordering its DeliverAt is left at
//     in.DeliverAt (sent "now" = its arrival time at this stage, reflecting any
//     upstream delay); otherwise Gap is added to its DeliverAt. This never moves
//     DeliverAt before RecvAt.
//   - Duplicate: if selected, the result is two InFlight whose Data are
//     independent copies, so the caller (and downstream) can mutate one without
//     affecting the other.
func (r *Reorder) Process(in engine.InFlight) []engine.InFlight {
	reordered := r.decideReorder()
	if !reordered {
		in.DeliverAt += r.cfg.Gap
	}

	dup := r.cfg.DupPct > 0 && r.src.Float64() < r.cfg.DupPct
	if !dup {
		// Single output: copy Data so we never alias the caller's slice when a
		// downstream cell mutates it.
		out := in
		out.Data = copyBytes(in.Data)
		return []engine.InFlight{out}
	}

	a := in
	a.Data = copyBytes(in.Data)
	b := in
	b.Data = copyBytes(in.Data)
	return []engine.InFlight{a, b}
}

// decideReorder returns whether the current packet is reordered, honouring the
// correlation: with probability Correlation it reuses the previous decision,
// otherwise it draws a fresh Bernoulli(ReorderPct). The correlation draw is
// taken first and unconditionally so the rng substream advances by exactly one
// Uint64 per packet for the reorder decision regardless of branch taken (keeps
// the stream position predictable).
func (r *Reorder) decideReorder() bool {
	corrDraw := r.src.Float64()
	freshDraw := r.src.Float64()

	var reordered bool
	if r.hasPrev && r.cfg.Correlation > 0 && corrDraw < r.cfg.Correlation {
		reordered = r.prevReor
	} else {
		reordered = freshDraw < r.cfg.ReorderPct
	}

	r.hasPrev = true
	r.prevReor = reordered
	return reordered
}

func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
