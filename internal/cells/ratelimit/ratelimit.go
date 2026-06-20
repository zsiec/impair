// Package ratelimit implements a bandwidth / rate-limit impairment Cell with a
// bounded drop-tail queue. It models a link that can transmit RateBps bytes per
// second backed by QueueBytes of buffer. Each packet is serialized onto a
// virtual transmit clock: its delivery time is when the link finishes clocking
// its bytes out. Packets that arrive while more than QueueBytes are already
// backlogged ahead of them are dropped (drop-tail), exactly as a real router's
// egress FIFO would discard them.
//
// The cell is fully deterministic from input packet timing and sizes. It still
// accepts a *rng.Source for interface uniformity with stochastic cells, but it
// never draws from it.
package ratelimit

import (
	"github.com/zsiec/impair/internal/engine"
	"github.com/zsiec/impair/internal/rng"
)

// Config parameterizes the rate-limit cell.
//
// The zero value is inert: with RateBps == 0 the cell is a no-op pass-through
// (no shaping, no drops), so it is safe to drop an unconfigured cell into a
// pipeline.
type Config struct {
	// RateBps is the link capacity in bytes per second. Values <= 0 disable
	// shaping entirely (every packet passes unchanged).
	RateBps int64
	// QueueBytes is the egress buffer depth in bytes. Once more than this many
	// bytes are already queued ahead of an arriving packet, that packet is
	// dropped (drop-tail). Values <= 0 mean an unbounded queue (shape but never
	// drop).
	QueueBytes int64
}

const nsPerSec = int64(1e9)

// cell is a single shaping FIFO. The engine builds one Cell per direction, so a
// single instance only ever sees one direction's traffic; its queueFreeAt clock
// is therefore inherently per-direction.
type cell struct {
	cfg Config
	// queueFreeAt is the virtual ns timestamp at which the link will finish
	// transmitting everything queued so far, i.e. when the next byte could
	// start clocking out.
	queueFreeAt int64
}

// New constructs a rate-limit Cell. src is accepted for interface uniformity
// but is never used: the shaping decision is a deterministic function of packet
// arrival times and sizes alone.
func New(cfg Config, src *rng.Source) engine.Cell {
	_ = src // deterministic cell; randomness intentionally unused
	return &cell{cfg: cfg}
}

func (c *cell) Name() string { return "ratelimit" }

// Process shapes one packet onto the virtual transmit clock.
//
//   - serialize := len(Data) * 1e9 / RateBps   (ns to clock the bytes out)
//   - start     := max(DeliverAt, queueFreeAt)  (when the link becomes free)
//   - DeliverAt := start + serialize
//   - advance queueFreeAt = DeliverAt
//
// The packet reaches this rate limiter at in.DeliverAt — its arrival time at
// this stage, already reflecting any upstream delay — not at in.RecvAt, so the
// queue arrival time and backlog are computed relative to in.DeliverAt.
//
// Before committing, the backlog already queued ahead of this packet is
// computed; if it exceeds QueueBytes the packet is dropped and the clock is not
// advanced (drop-tail: a dropped packet never consumed link time).
func (c *cell) Process(in engine.InFlight) []engine.InFlight {
	// Disabled / no-op: pass through untouched.
	if c.cfg.RateBps <= 0 {
		return []engine.InFlight{in}
	}

	// Backlog (bytes) still queued ahead of this arrival. The packet arrives at
	// this stage at in.DeliverAt (reflecting upstream delay), so the backlog is
	// measured from there. If the link is idle (queueFreeAt <= DeliverAt) the
	// backlog is zero.
	if c.cfg.QueueBytes > 0 && c.queueFreeAt > in.DeliverAt {
		backlog := (c.queueFreeAt - in.DeliverAt) * c.cfg.RateBps / nsPerSec
		if backlog > c.cfg.QueueBytes {
			return nil // drop-tail; do not advance the clock
		}
	}

	serialize := int64(len(in.Data)) * nsPerSec / c.cfg.RateBps

	start := in.DeliverAt
	if c.queueFreeAt > start {
		start = c.queueFreeAt
	}
	deliverAt := start + serialize
	c.queueFreeAt = deliverAt

	out := in
	out.DeliverAt = deliverAt
	return []engine.InFlight{out}
}
