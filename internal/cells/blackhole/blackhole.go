// Package blackhole drops every packet whose ingress time falls inside a
// [start, end) window — a deterministic, timed link OUTAGE. It is the primitive
// behind failover scenarios: a link goes dark mid-stream and a redundant path
// (SMPTE 2022-7 bonding) must mask the gap with zero loss.
//
// Unlike loss/GE it is NOT stochastic — the drop decision is a pure function of
// the packet's arrival time, drawing no randomness — so it is bit-deterministic
// on the Tier-1 virtual clock and distribution-reproducible on the Tier-2 wall
// clock (the window is relative to the engine clock's origin).
package blackhole

import "github.com/zsiec/impair/engine"

// Config is a single dark window. Times are nanoseconds relative to the engine
// clock origin (RecvAt). EndNs <= StartNs is an always-open (never-dark) cell.
type Config struct {
	StartNs int64 // window start, inclusive
	EndNs   int64 // window end, exclusive
}

type cell struct{ start, end int64 }

// New returns a blackhole Cell that drops every packet arriving in [start, end).
func New(cfg Config) engine.Cell { return &cell{start: cfg.StartNs, end: cfg.EndNs} }

// Name implements engine.Cell.
func (c *cell) Name() string { return "blackhole" }

// RequiresCleartext reports false: the outage is decided from the packet's
// arrival time alone, never its contents, so it impairs encrypted and cleartext
// flows identically.
func (c *cell) RequiresCleartext() bool { return false }

// Process drops (returns no packets) while the link is dark, else forwards
// unchanged. The engine records the cell Name ("blackhole") as the drop reason.
func (c *cell) Process(in engine.InFlight) []engine.InFlight {
	if in.RecvAt >= c.start && in.RecvAt < c.end {
		return nil
	}
	return []engine.InFlight{in}
}
