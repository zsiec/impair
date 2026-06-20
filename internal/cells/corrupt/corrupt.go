// Package corrupt implements a bit-error corruption Cell. With probability Pct
// it flips exactly one uniformly-chosen bit in a copy of the packet's payload,
// leaving the caller's slice untouched; otherwise the packet passes through
// unchanged. All randomness comes from the injected *rng.Source, so a given
// (seed, input trace) yields a byte-identical output stream on every run.
package corrupt

import (
	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/rng"
)

// Config configures the corruption Cell. The zero value (Pct == 0) never
// corrupts; it passes every packet through unchanged.
type Config struct {
	// Pct is the per-packet probability of a single-bit corruption, in [0,1].
	Pct float64
}

// cell is a single-bit-error corruption Cell.
type cell struct {
	pct float64
	src *rng.Source
}

// New returns a corruption Cell. With probability cfg.Pct each packet has one
// uniformly-chosen bit flipped in a fresh copy of its payload; the caller's
// Data slice is never aliased or mutated. Pct is clamped to [0,1].
func New(cfg Config, src *rng.Source) engine.Cell {
	p := cfg.Pct
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return &cell{pct: p, src: src}
}

// Name identifies this cell (used by the engine as a drop reason; this cell
// never drops).
func (c *cell) Name() string { return "corrupt" }

// Process passes the packet through, corrupting one bit with probability pct.
// An empty payload can hold no bit to flip, so it is forwarded unchanged even
// when selected for corruption. The single returned element keeps the packet;
// it is never dropped or duplicated.
func (c *cell) Process(in engine.InFlight) []engine.InFlight {
	if c.pct <= 0 || len(in.Data) == 0 {
		return []engine.InFlight{in}
	}
	if c.src.Float64() >= c.pct {
		return []engine.InFlight{in}
	}

	// Copy before mutating: never touch the caller's slice.
	data := make([]byte, len(in.Data))
	copy(data, in.Data)

	bit := c.src.Intn(len(data) * 8)
	data[bit/8] ^= 1 << uint(bit%8)

	out := in
	out.Data = data
	return []engine.InFlight{out}
}
