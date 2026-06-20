// Package loss provides packet-loss impairment Cells: an independent
// per-packet Bernoulli model and the Gilbert-Elliott burst-loss model
// (the same "gemodel" used by Linux netem).
//
// Both cells are stateful and deterministic: their only source of
// randomness is the *rng.Source handed to the constructor. Loss is
// expressed by returning an empty slice from Process (the engine records
// the cell Name as the drop reason); a surviving packet is forwarded
// unchanged.
package loss

import (
	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/rng"
)

// Config configures a Bernoulli loss cell.
//
// The zero value drops nothing (P == 0).
type Config struct {
	// P is the independent per-packet drop probability, in [0,1].
	// Values <= 0 never drop; values >= 1 always drop.
	P float64
}

// bernoulli is an independent per-packet loss cell.
type bernoulli struct {
	p   float64
	src *rng.Source
}

// New returns a Bernoulli loss Cell that drops each packet independently
// with probability cfg.P, drawing from src.
func New(cfg Config, src *rng.Source) engine.Cell {
	return &bernoulli{p: cfg.P, src: src}
}

// Name implements engine.Cell.
func (b *bernoulli) Name() string { return "loss.bernoulli" }

// Process drops the packet with probability p, otherwise forwards it
// unchanged.
func (b *bernoulli) Process(in engine.InFlight) []engine.InFlight {
	if b.p <= 0 {
		return []engine.InFlight{in}
	}
	if b.p >= 1 {
		return nil
	}
	if b.src.Float64() < b.p {
		return nil
	}
	return []engine.InFlight{in}
}

// GEConfig configures a Gilbert-Elliott burst-loss cell.
//
// The model has two states, GOOD and BAD. Per packet the chain may
// transition GOOD->BAD with probability P and BAD->GOOD with probability
// R. In the GOOD state a packet is lost with probability 1-H; in the BAD
// state it is lost with probability 1-K.
//
// Defaults follow netem's gemodel and are applied in NewGE: an
// unspecified R becomes 1, an unspecified H becomes 1 (lossless GOOD),
// and K stays 0 (total loss in BAD). Use WithH / WithK to set those
// fields to an explicit 0.
type GEConfig struct {
	// P is the GOOD->BAD transition probability, in [0,1].
	P float64
	// R is the BAD->GOOD transition probability, in [0,1].
	// If R <= 0 it defaults to 1 (one-packet bad bursts), matching netem.
	R float64
	// H is 1 minus the GOOD-state loss probability, in [0,1].
	// If unset it defaults to 1 (a lossless GOOD state).
	H float64
	// K is 1 minus the BAD-state loss probability, in [0,1].
	// Defaults to 0 (total loss in the BAD state).
	K float64

	// hSet/kSet record that the caller supplied an explicit value via
	// WithH / WithK, so the netem defaults are only applied to truly
	// unset fields.
	hSet bool
	kSet bool
}

// WithH returns a copy of cfg with H explicitly set (so the default is
// not applied even when v == 0).
func (cfg GEConfig) WithH(v float64) GEConfig { cfg.H = v; cfg.hSet = true; return cfg }

// WithK returns a copy of cfg with K explicitly set (so the default is
// not applied even when v == 0).
func (cfg GEConfig) WithK(v float64) GEConfig { cfg.K = v; cfg.kSet = true; return cfg }

const (
	geGood = 0
	geBad  = 1
)

// ge is the Gilbert-Elliott burst-loss cell. It keeps a single state
// (this instance models one direction), so call it once per packet in
// arrival order.
type ge struct {
	p, r, h, k float64
	src        *rng.Source
	state      int
}

// NewGE returns a Gilbert-Elliott loss Cell. Defaults follow netem's
// gemodel: an unspecified R becomes 1, an unspecified H becomes 1
// (lossless GOOD), and K stays 0 (total loss in BAD). Use GEConfig.WithH
// / WithK to set those fields to an explicit 0.
func NewGE(cfg GEConfig, src *rng.Source) engine.Cell {
	r := cfg.R
	if r <= 0 {
		r = 1
	}
	h := cfg.H
	if !cfg.hSet && h == 0 {
		h = 1
	}
	k := cfg.K // defaults to 0
	return &ge{p: cfg.P, r: r, h: h, k: k, src: src, state: geGood}
}

// Name implements engine.Cell.
func (g *ge) Name() string { return "loss.ge" }

// Process advances the Gilbert-Elliott chain by one step and drops the
// packet according to the current state's loss probability, then
// evaluates the state transition for the next packet (matching netem's
// gemodel ordering).
func (g *ge) Process(in engine.InFlight) []engine.InFlight {
	var lossProb float64
	switch g.state {
	case geGood:
		lossProb = 1 - g.h
	default:
		lossProb = 1 - g.k
	}

	lost := false
	if lossProb >= 1 {
		lost = true
	} else if lossProb > 0 && g.src.Float64() < lossProb {
		lost = true
	}

	// Advance the chain for the next packet.
	switch g.state {
	case geGood:
		if g.p > 0 && (g.p >= 1 || g.src.Float64() < g.p) {
			g.state = geBad
		}
	default:
		if g.r >= 1 || (g.r > 0 && g.src.Float64() < g.r) {
			g.state = geGood
		}
	}

	if lost {
		return nil
	}
	return []engine.InFlight{in}
}
