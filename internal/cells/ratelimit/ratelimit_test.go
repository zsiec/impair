package ratelimit

import (
	"testing"

	"github.com/zsiec/impair/internal/engine"
	"github.com/zsiec/impair/internal/rng"
)

func newSrc() *rng.Source { return rng.NewRoot(1).Sub("ratelimit") }

// pkt builds an InFlight as the engine would: DeliverAt starts == RecvAt.
func pkt(seq uint64, recvAt int64, size int) engine.InFlight {
	return engine.InFlight{
		Seq:       seq,
		Dir:       engine.C2S,
		Data:      make([]byte, size),
		RecvAt:    recvAt,
		DeliverAt: recvAt,
	}
}

func TestName(t *testing.T) {
	c := New(Config{RateBps: 1000}, newSrc())
	if c.Name() != "ratelimit" {
		t.Fatalf("Name() = %q, want %q", c.Name(), "ratelimit")
	}
}

// Disabled (RateBps <= 0) is a transparent pass-through.
func TestDisabledPassThrough(t *testing.T) {
	c := New(Config{RateBps: 0, QueueBytes: 10}, newSrc())
	in := pkt(1, 5000, 100)
	out := c.Process(in)
	if len(out) != 1 {
		t.Fatalf("got %d outputs, want 1", len(out))
	}
	if out[0].DeliverAt != in.RecvAt {
		t.Fatalf("DeliverAt = %d, want %d (no shaping)", out[0].DeliverAt, in.RecvAt)
	}
}

// Single packet on an idle link: delay == serialization time, DeliverAt >= RecvAt.
func TestSerializationDelay(t *testing.T) {
	// 1000 B/s => 1 byte = 1e9/1000 = 1_000_000 ns.
	c := New(Config{RateBps: 1000}, newSrc())
	out := c.Process(pkt(1, 0, 100))
	if len(out) != 1 {
		t.Fatalf("got %d outputs, want 1", len(out))
	}
	want := int64(100) * nsPerSec / 1000 // 100_000_000 ns
	if out[0].DeliverAt != want {
		t.Fatalf("DeliverAt = %d, want %d", out[0].DeliverAt, want)
	}
	if out[0].DeliverAt < out[0].RecvAt {
		t.Fatalf("DeliverAt %d < RecvAt %d", out[0].DeliverAt, out[0].RecvAt)
	}
}

// Traffic arriving below the link rate sees ~0 added queueing delay: each
// packet's delivery is just its own serialization beyond arrival.
func TestBelowRateNoQueueing(t *testing.T) {
	rate := int64(1000) // B/s
	c := New(Config{RateBps: rate}, newSrc())
	size := 10
	serialize := int64(size) * nsPerSec / rate // 10ms
	// Space arrivals well beyond serialization so the link is always idle.
	spacing := 5 * serialize
	var recvAt int64
	for i := 0; i < 50; i++ {
		out := c.Process(pkt(uint64(i+1), recvAt, size))
		if len(out) != 1 {
			t.Fatalf("packet %d dropped/dup unexpectedly", i)
		}
		// On an idle link DeliverAt == RecvAt + serialize exactly.
		if got := out[0].DeliverAt - recvAt; got != serialize {
			t.Fatalf("packet %d added delay = %d, want %d", i, got, serialize)
		}
		recvAt += spacing
	}
}

// Sustained input above the link rate is shaped down to ~RateBps and the queue
// stays bounded (overflow is dropped).
func TestAboveRateShapedAndBounded(t *testing.T) {
	rate := int64(100_000) // 100 KB/s
	queue := int64(20_000) // 20 KB buffer
	c := New(Config{RateBps: rate, QueueBytes: queue}, newSrc())

	size := 1000                               // 1 KB packets
	serialize := int64(size) * nsPerSec / rate // 10ms per packet at line rate
	// Offer at 5x the line rate.
	spacing := serialize / 5

	const n = 2000
	var passed int
	var recvAt int64
	var lastDeliver int64 = -1
	var firstDeliver, lastPassDeliver int64
	first := true

	for i := 0; i < n; i++ {
		out := c.Process(pkt(uint64(i+1), recvAt, size))
		switch len(out) {
		case 0:
			// dropped (drop-tail) - fine
		case 1:
			passed++
			d := out[0].DeliverAt
			// Monotonic DeliverAt within a direction (FIFO shaping).
			if d < lastDeliver {
				t.Fatalf("DeliverAt went backwards: %d < %d", d, lastDeliver)
			}
			lastDeliver = d
			if d < out[0].RecvAt {
				t.Fatalf("DeliverAt %d < RecvAt %d", d, out[0].RecvAt)
			}
			if first {
				firstDeliver = d
				first = false
			}
			lastPassDeliver = d
		default:
			t.Fatalf("unexpected %d outputs", len(out))
		}
		recvAt += spacing
	}

	if passed == 0 {
		t.Fatal("no packets passed")
	}
	// Many packets must have been dropped given 5x overload.
	if passed >= n {
		t.Fatalf("expected drops under 5x overload, passed all %d", passed)
	}

	// Throughput of passed traffic over the delivery span must be ~RateBps.
	span := lastPassDeliver - firstDeliver
	if span <= 0 {
		t.Fatal("zero delivery span")
	}
	// Bytes delivered between first and last (exclusive of the first's bytes is
	// negligible at this scale); approximate with passed*size.
	gotRate := float64(passed) * float64(size) * float64(nsPerSec) / float64(span)
	lo, hi := float64(rate)*0.7, float64(rate)*1.3
	if gotRate < lo || gotRate > hi {
		t.Fatalf("shaped rate = %.0f B/s, want within [%.0f, %.0f]", gotRate, lo, hi)
	}

	// Queue bounded: max observed backlog (in delivery terms) never far exceeds
	// QueueBytes + one in-flight packet. Re-derive the worst-case backlog by
	// replaying and checking the standing queue never exceeds queue + size.
	c2 := New(Config{RateBps: rate, QueueBytes: queue}, newSrc()).(*cell)
	recvAt = 0
	maxBacklog := int64(0)
	for i := 0; i < n; i++ {
		ra := recvAt
		if c2.queueFreeAt > ra {
			b := (c2.queueFreeAt - ra) * rate / nsPerSec
			if b > maxBacklog {
				maxBacklog = b
			}
		}
		c2.Process(pkt(uint64(i+1), ra, size))
		recvAt += spacing
	}
	// Backlog at admission time is <= QueueBytes; after admitting one packet the
	// standing queue is at most QueueBytes + size.
	if maxBacklog > queue+int64(size) {
		t.Fatalf("backlog %d exceeded bound %d", maxBacklog, queue+int64(size))
	}
}

// Drop-tail: a dropped packet must not advance the transmit clock.
func TestDropDoesNotAdvanceClock(t *testing.T) {
	rate := int64(1000) // B/s => 1 byte = 1ms
	queue := int64(5)   // tiny buffer
	c := New(Config{RateBps: rate, QueueBytes: queue}, newSrc()).(*cell)

	// First packet (10 bytes) admitted on idle link -> DeliverAt = 10ms.
	if out := c.Process(pkt(1, 0, 10)); len(out) != 1 {
		t.Fatalf("first packet not admitted: %d outs", len(out))
	}
	freeAfterFirst := c.queueFreeAt
	want := int64(10) * nsPerSec / rate
	if freeAfterFirst != want {
		t.Fatalf("queueFreeAt = %d, want %d", freeAfterFirst, want)
	}

	// Second packet arrives immediately: backlog == 10 bytes > queue(5) -> drop.
	if out := c.Process(pkt(2, 0, 10)); len(out) != 0 {
		t.Fatalf("expected drop, got %d outputs", len(out))
	}
	if c.queueFreeAt != freeAfterFirst {
		t.Fatalf("clock advanced on drop: %d != %d", c.queueFreeAt, freeAfterFirst)
	}
}

// Backlog exactly at QueueBytes is admitted; just over is dropped (boundary).
func TestBacklogBoundary(t *testing.T) {
	rate := int64(1000) // 1 byte = 1ms = 1_000_000 ns
	c := New(Config{RateBps: rate, QueueBytes: 10}, newSrc()).(*cell)

	// Prime the queue to exactly 10 bytes backlog at t=0.
	c.Process(pkt(1, 0, 10)) // queueFreeAt = 10ms; backlog at t=0 = 10 bytes.

	// Arrival at t=0: backlog == 10 == QueueBytes => admitted (not > bound).
	if out := c.Process(pkt(2, 0, 1)); len(out) != 1 {
		t.Fatalf("backlog==QueueBytes should be admitted, got %d outs", len(out))
	}
	// Now backlog is 11 bytes. Next arrival at t=0 => 11 > 10 => dropped.
	if out := c.Process(pkt(3, 0, 1)); len(out) != 0 {
		t.Fatalf("backlog>QueueBytes should drop, got %d outs", len(out))
	}
}

// Unbounded queue (QueueBytes <= 0) shapes but never drops.
func TestUnboundedNeverDrops(t *testing.T) {
	c := New(Config{RateBps: 1000, QueueBytes: 0}, newSrc())
	for i := 0; i < 1000; i++ {
		// All arrive at t=0: huge backlog, but no bound => admit every one.
		if out := c.Process(pkt(uint64(i+1), 0, 100)); len(out) != 1 {
			t.Fatalf("unbounded queue dropped packet %d", i)
		}
	}
}

// Determinism: identical input + identical Source => byte-identical DeliverAt
// stream, regardless of the Source contents (cell never draws from it).
func TestDeterministic(t *testing.T) {
	run := func() []int64 {
		c := New(Config{RateBps: 100_000, QueueBytes: 20_000}, newSrc())
		var ds []int64
		var recvAt int64
		for i := 0; i < 500; i++ {
			out := c.Process(pkt(uint64(i+1), recvAt, 1000))
			if len(out) == 1 {
				ds = append(ds, out[0].DeliverAt)
			} else {
				ds = append(ds, -1) // marker for drop
			}
			recvAt += 2_000_000 // 2ms spacing (above the 10ms line rate)
		}
		return ds
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("length mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic at %d: %d != %d", i, a[i], b[i])
		}
	}
}

// DeliverAt is never set before RecvAt across a mixed trace (invariant from the
// contract).
func TestNeverDeliverBeforeRecv(t *testing.T) {
	c := New(Config{RateBps: 50_000, QueueBytes: 8_000}, newSrc())
	var recvAt int64
	for i := 0; i < 1000; i++ {
		size := 200 + (i%7)*100
		out := c.Process(pkt(uint64(i+1), recvAt, size))
		for _, o := range out {
			if o.DeliverAt < o.RecvAt {
				t.Fatalf("packet %d: DeliverAt %d < RecvAt %d", i, o.DeliverAt, o.RecvAt)
			}
		}
		recvAt += int64(i%4) * 1_000_000 // jittery, sometimes-bursty arrivals
	}
}
