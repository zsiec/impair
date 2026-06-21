package relaycore

import (
	"math"
	"sort"
	"sync"

	"github.com/zsiec/impair/engine"
)

// Entry is one packet's relay-residence record, decomposed entirely from the
// relay's OWN single monotonic clock (the core's base) with NO cross-process
// sync. It is the relay-ledger substrate for attributing one-way delay. All
// times are nanoseconds since base.
//
//	RecvAt    — ingress: time.Since(base) when the datagram was read.
//	DeliverAt — the engine-scheduled forward time (Action.DeliverAt).
//	EgressAt  — actual socket-write time (time.Since(base) at send).
//
// Because all three come off the SAME clock, their differences are exact
// wire-side delay components with no clock-skew term (see DecomposedOWD).
type Entry struct {
	Seq       uint64           // engine ingress sequence (Action.Seq)
	Dir       engine.Direction // C2S / S2C
	RecvAt    int64            // ns since base: read off the socket
	DeliverAt int64            // ns since base: engine-scheduled delivery
	EgressAt  int64            // ns since base: actual socket write
}

// ledger is a bounded ring of Entry, OFF unless explicitly enabled (a nil
// *ledger on the core means no recording and zero hot-path cost). It is a
// fixed-capacity overwrite-oldest ring: once full, the next record overwrites
// the OLDEST entry, so memory is bounded at cap*sizeof(Entry) and a snapshot
// always reflects the most-recent cap packets. Recording is split: ingress
// fields are written when a forward is enqueued (returning an index handle that
// rides the egItem through the egress heap), and EgressAt is finalized when the
// forward is actually written. A generation counter guards the slot so a handle
// whose slot was overwritten before its egress (only possible once the ring has
// wrapped cap times during a single packet's residence) is simply dropped
// instead of corrupting an unrelated entry.
type ledger struct {
	mu    sync.Mutex
	buf   []Entry  // fixed cap; ring buffer
	gen   []uint64 // per-slot generation, bumped each time a slot is (re)used
	next  int      // next slot to write (mod cap)
	count int      // total records ever started (for snapshot ordering / fullness)
}

// ledgerHandle locates a started-but-not-yet-finalized entry. The zero value
// (slot -1) is the "not recording" handle, used on the hot path when the ledger
// is off so egItem carries a cheap inert value.
type ledgerHandle struct {
	slot int
	gen  uint64
}

// noHandle is the inert handle threaded through egItem when the ledger is off.
var noHandle = ledgerHandle{slot: -1}

func newLedger(capacity int) *ledger {
	if capacity < 1 {
		capacity = 1
	}
	return &ledger{
		buf: make([]Entry, capacity),
		gen: make([]uint64, capacity),
	}
}

// start records the ingress side of a forward and returns a handle to finalize
// its EgressAt later. EgressAt is left zero until finalize.
func (l *ledger) start(seq uint64, dir engine.Direction, recvAt, deliverAt int64) ledgerHandle {
	l.mu.Lock()
	slot := l.next
	l.gen[slot]++
	g := l.gen[slot]
	l.buf[slot] = Entry{Seq: seq, Dir: dir, RecvAt: recvAt, DeliverAt: deliverAt}
	l.next = (l.next + 1) % len(l.buf)
	l.count++
	l.mu.Unlock()
	return ledgerHandle{slot: slot, gen: g}
}

// finalize stamps EgressAt on the entry the handle refers to, unless that slot
// has since been reused (generation mismatch) — in which case the entry has
// already aged out of the ring and the egress time is discarded.
func (l *ledger) finalize(h ledgerHandle, egressAt int64) {
	if h.slot < 0 {
		return
	}
	l.mu.Lock()
	if l.gen[h.slot] == h.gen {
		l.buf[h.slot].EgressAt = egressAt
	}
	l.mu.Unlock()
}

// snapshot returns a copy of the recorded entries in chronological (record)
// order, oldest first. Entries whose EgressAt is still zero (forward enqueued
// but not yet sent, or tail-dropped after start) are included as-is.
func (l *ledger) snapshot() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.count
	if n > len(l.buf) {
		n = len(l.buf)
	}
	out := make([]Entry, n)
	if l.count <= len(l.buf) {
		// Not yet wrapped: slots [0, count) are in order.
		copy(out, l.buf[:n])
		return out
	}
	// Wrapped: oldest is at l.next, walk forward with wraparound.
	for i := 0; i < n; i++ {
		out[i] = l.buf[(l.next+i)%len(l.buf)]
	}
	return out
}

// Decomposition is the per-aggregate one-way-delay breakdown over a set of
// ledger entries, every component computed from the relay's own clock alone:
//
//	Queue    = DeliverAt - RecvAt  — scheduled / queueing delay (the modeled
//	           impairment + time spent waiting for its delivery slot).
//	Overhead = EgressAt  - DeliverAt — the relay's OWN scheduling overhead (how
//	           late the egress goroutine fired versus the scheduled time).
//	Residence= EgressAt  - RecvAt   — total time the packet spent inside the
//	           relay (Queue + Overhead), i.e. the relay's contribution to OWD.
//
// Each component carries the sample count and p50/p99 (nearest-rank) over the
// per-packet values. This is the substrate the latency-comparison view consumes
// to separate modeled delay from relay self-overhead WITHOUT any cross-process
// clock sync.
type Decomposition struct {
	N         int      // packets with a complete record (non-zero EgressAt)
	Queue     Quantile // DeliverAt - RecvAt
	Overhead  Quantile // EgressAt  - DeliverAt
	Residence Quantile // EgressAt  - RecvAt
}

// Quantile is the p50/p99 summary (and sample count) of one delay component, in
// nanoseconds.
type Quantile struct {
	N   int
	P50 int64
	P99 int64
}

// PerPacketOWD is one packet's decomposed residence, in nanoseconds.
type PerPacketOWD struct {
	Seq       uint64
	Dir       engine.Direction
	Queue     int64 // DeliverAt - RecvAt
	Overhead  int64 // EgressAt  - DeliverAt
	Residence int64 // EgressAt  - RecvAt
}

// DecomposedOWD decomposes wire-side one-way delay into its components from the
// ledger alone. It is PURE (no clock, no I/O) and exact: every value is a
// difference of two timestamps taken off the same monotonic clock. Entries
// without a finalized egress (EgressAt == 0, i.e. enqueued-but-not-sent or
// dropped after start) are skipped — they have no residence to attribute. The
// returned per-packet slice preserves the input order; the aggregate carries
// p50/p99 over the included packets.
func DecomposedOWD(entries []Entry) (Decomposition, []PerPacketOWD) {
	perPkt := make([]PerPacketOWD, 0, len(entries))
	queue := make([]int64, 0, len(entries))
	overhead := make([]int64, 0, len(entries))
	residence := make([]int64, 0, len(entries))
	for _, e := range entries {
		if e.EgressAt == 0 {
			continue // never sent — no residence to attribute
		}
		p := PerPacketOWD{
			Seq:       e.Seq,
			Dir:       e.Dir,
			Queue:     e.DeliverAt - e.RecvAt,
			Overhead:  e.EgressAt - e.DeliverAt,
			Residence: e.EgressAt - e.RecvAt,
		}
		perPkt = append(perPkt, p)
		queue = append(queue, p.Queue)
		overhead = append(overhead, p.Overhead)
		residence = append(residence, p.Residence)
	}
	d := Decomposition{
		N:         len(perPkt),
		Queue:     quantileOf(queue),
		Overhead:  quantileOf(overhead),
		Residence: quantileOf(residence),
	}
	return d, perPkt
}

// quantileOf summarizes a sample into p50/p99. It sorts a COPY so the caller's
// per-packet ordering is untouched.
func quantileOf(xs []int64) Quantile {
	q := Quantile{N: len(xs)}
	if len(xs) == 0 {
		return q
	}
	s := make([]int64, len(xs))
	copy(s, xs)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	q.P50 = nearestRank(s, 0.50)
	q.P99 = nearestRank(s, 0.99)
	return q
}

// nearestRank returns the q-quantile (0..1) of a sorted slice via nearest-rank,
// matching stat.percentile's convention so quantiles are consistent repo-wide.
func nearestRank(sorted []int64, q float64) int64 {
	idx := int(math.Round(q * float64(len(sorted)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
