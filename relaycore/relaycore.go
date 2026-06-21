// Package relaycore is the protocol-agnostic datapath shared by the real-socket
// relay drivers (SRT `relay`, RIST `ristrelay`, and any future ones). A Core
// applies the live-swappable Sans-I/O engine to each classified datagram and
// schedules the surviving forwards on a min-heap egress (ordered by delivery
// time, then arrival order), draining them on a single goroutine with one
// reusable timer — so the steady-state hot path allocates ~nothing per packet.
// It also owns the optional OWD ledger and the ground-truth counters.
//
// A protocol-specific Transport supplies the sockets, runs the read loops,
// classifies each datagram into an engine Direction and a destination, taps it,
// and writes a forward via the send callback. The core owns everything from
// engine dispatch downstream; the transport owns the wire topology. Dest is the
// transport's opaque destination handle (a netip.AddrPort for a single-port
// relay, a *net.UDPAddr for a dual-port one), kept as a type parameter so the
// scheduler stays allocation-free (no boxing of the destination per packet).
package relaycore

import (
	"container/heap"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zsiec/impair/engine"
)

const maxDatagram = 2048

// defaultMaxQueued bounds the egress heap (scheduled-but-not-yet-due forwards).
// Beyond it the core tail-drops arriving forwards rather than grow without
// limit, so a sustained over-rate degrades as bounded, recorded tail-drop
// instead of unbounded memory. ~16K datagrams ≈ 32 MB at maxDatagram.
const defaultMaxQueued = 16384

// DefaultLedgerCap bounds the OWD ledger ring when enabled without an explicit
// size — sized to span the in-flight set plus recent history.
const DefaultLedgerCap = 16384

// Core is the shared relay datapath. Construct one per relay via New, drive it
// from the transport's read loops (Process), and tear it down with Close.
type Core[Dest any] struct {
	eng  atomic.Pointer[engine.Engine]
	base time.Time
	send func(Dest, []byte) // protocol-specific socket write of a forward

	fwd, drop, tailDrop atomic.Uint64

	egMu      sync.Mutex
	eg        egHeap[Dest]
	egOrd     uint64
	wake      chan struct{} // buffered(1): a new head was enqueued; egress re-checks
	pool      sync.Pool     // *[]byte (cap maxDatagram) forward buffers
	maxQueued int

	// ledger is the opt-in OWD relay-ledger. nil == OFF: the hot path checks for
	// nil and does nothing. Held atomically so EnableLedger can flip it at runtime
	// without racing the goroutines that read it per packet.
	ledger atomic.Pointer[ledger]

	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New creates a Core and starts its egress goroutine. send writes a forward's
// bytes to dst on the transport's socket. maxQueued bounds the egress heap (<=0
// uses the default); ledgerCap > 0 enables the OWD ledger at that ring capacity.
func New[Dest any](eng *engine.Engine, send func(Dest, []byte), maxQueued, ledgerCap int) *Core[Dest] {
	if maxQueued <= 0 {
		maxQueued = defaultMaxQueued
	}
	c := &Core[Dest]{
		base:      time.Now(),
		send:      send,
		wake:      make(chan struct{}, 1),
		pool:      sync.Pool{New: func() any { b := make([]byte, maxDatagram); return &b }},
		maxQueued: maxQueued,
		closed:    make(chan struct{}),
	}
	c.eng.Store(eng)
	if ledgerCap > 0 {
		c.ledger.Store(newLedger(ledgerCap))
	}
	c.wg.Add(1)
	go c.egress()
	return c
}

// SetEngine atomically swaps the impairment engine applied to subsequent
// packets — the runtime-mutation primitive behind live control. In-flight
// scheduled forwards already committed by the old engine still fire.
func (c *Core[Dest]) SetEngine(eng *engine.Engine) { c.eng.Store(eng) }

// Now is ns since the core's base clock; the transport stamps recvAt with it.
func (c *Core[Dest]) Now() int64 { return time.Since(c.base).Nanoseconds() }

// Closed is closed when the core is shutting down; transport read loops select
// on it to exit.
func (c *Core[Dest]) Closed() <-chan struct{} { return c.closed }

// Go runs fn as a core-tracked goroutine (a transport read loop), so Close waits
// for it before closing sockets.
func (c *Core[Dest]) Go(fn func()) {
	c.wg.Add(1)
	go func() { defer c.wg.Done(); fn() }()
}

// Process runs one classified datagram through the current engine and schedules
// each surviving forward to dst. recvAt is ns since base (from Now). data need
// only stay valid for the duration of the call (enqueue copies into a pooled
// buffer), so the transport may reuse its read buffer on the next read.
func (c *Core[Dest]) Process(data []byte, dir engine.Direction, dst Dest, recvAt int64) {
	for _, a := range c.eng.Load().Handle(engine.Packet{Data: data, Dir: dir}, recvAt) {
		if a.Kind == engine.Drop {
			c.drop.Add(1)
			continue
		}
		c.enqueue(a.Data, dst, a.DeliverAt, a.Seq, dir, recvAt)
	}
}

// Stats snapshots the ground-truth counters. Dropped is impairment (the engine
// decided to drop); TailDropped is the core's OWN egress-queue overflow — kept
// separate so relay-induced loss never masquerades as modeled loss.
func (c *Core[Dest]) Stats() (forwarded, dropped, tailDropped uint64) {
	return c.fwd.Load(), c.drop.Load(), c.tailDrop.Load()
}

// EnableLedger turns the OWD ledger on at runtime, replacing any existing one
// with a fresh ring of capacity entries (<=0 uses DefaultLedgerCap).
func (c *Core[Dest]) EnableLedger(capacity int) {
	if capacity <= 0 {
		capacity = DefaultLedgerCap
	}
	c.ledger.Store(newLedger(capacity))
}

// Ledger snapshots the recorded OWD entries (oldest first), or nil if the ledger
// was never enabled. The copy is independent of the ring.
func (c *Core[Dest]) Ledger() []Entry {
	l := c.ledger.Load()
	if l == nil {
		return nil
	}
	return l.snapshot()
}

// Close stops the core: it closes the done channel, calls wake (the transport
// unblocks its read loops, e.g. by setting read deadlines to now), waits for the
// egress and read goroutines to exit, then calls closeSocks (the transport
// closes its sockets). It is idempotent.
func (c *Core[Dest]) Close(wake, closeSocks func()) {
	c.closeOnce.Do(func() {
		close(c.closed)
		wake()
		c.wg.Wait()
		closeSocks()
	})
}

// enqueue copies a forward payload into a pooled buffer and pushes it on the
// egress heap, waking the egress goroutine if this forward is now the soonest.
// seq/dir/recvAt are the OWD-ledger fields, recorded only when the ledger is
// enabled and the forward is actually admitted (tail-dropped forwards are not).
func (c *Core[Dest]) enqueue(data []byte, dst Dest, at int64, seq uint64, dir engine.Direction, recvAt int64) {
	bp := c.getBuf(len(data))
	copy((*bp)[:len(data)], data)

	c.egMu.Lock()
	if len(c.eg) >= c.maxQueued { // bounded: reject-on-full (tail-drop)
		c.egMu.Unlock()
		c.putBuf(bp)
		c.tailDrop.Add(1)
		return
	}
	c.egOrd++
	it := egItem[Dest]{at: at, ord: c.egOrd, buf: bp, n: len(data), dst: dst, led: noHandle}
	if l := c.ledger.Load(); l != nil { // OFF unless enabled: no cost otherwise
		it.led = l.start(seq, dir, recvAt, at)
	}
	heap.Push(&c.eg, it)
	isHead := c.eg[0].ord == it.ord
	c.egMu.Unlock()

	if isHead {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

// egress drains due forwards in (delivery time, arrival order) on one goroutine,
// sleeping on a single reusable timer until the next is due. On Close it returns
// with any not-yet-due forwards undelivered (a run's tail beyond its delay is
// not delivered after teardown).
func (c *Core[Dest]) egress() {
	defer c.wg.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		c.egMu.Lock()
		now := time.Since(c.base).Nanoseconds()
		for len(c.eg) > 0 && c.eg[0].at <= now {
			it := heap.Pop(&c.eg).(egItem[Dest])
			c.egMu.Unlock()
			c.sendItem(it)
			c.egMu.Lock()
			now = time.Since(c.base).Nanoseconds()
		}
		wait := time.Hour
		if len(c.eg) > 0 {
			if d := time.Duration(c.eg[0].at - now); d < time.Hour {
				wait = d
			}
		}
		c.egMu.Unlock()

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-c.closed:
			return
		case <-c.wake:
		case <-timer.C:
		}
	}
}

func (c *Core[Dest]) sendItem(it egItem[Dest]) {
	select {
	case <-c.closed:
		return
	default:
	}
	c.send(it.dst, (*it.buf)[:it.n])
	if it.led.slot >= 0 { // ledger was on at enqueue: stamp actual egress time
		if l := c.ledger.Load(); l != nil {
			l.finalize(it.led, time.Since(c.base).Nanoseconds())
		}
	}
	c.fwd.Add(1)
	c.putBuf(it.buf)
}

func (c *Core[Dest]) getBuf(n int) *[]byte {
	if n > maxDatagram {
		b := make([]byte, n)
		return &b
	}
	return c.pool.Get().(*[]byte)
}

func (c *Core[Dest]) putBuf(bp *[]byte) {
	if cap(*bp) >= maxDatagram {
		*bp = (*bp)[:maxDatagram]
		c.pool.Put(bp)
	}
}

// egItem is one scheduled forward. ord (the core enqueue counter) is the stable
// tiebreaker for equal delivery times, so egress order is deterministic given
// the deterministic arrival + engine-action order.
type egItem[Dest any] struct {
	at  int64
	ord uint64
	buf *[]byte
	n   int
	dst Dest
	led ledgerHandle // OWD-ledger handle; noHandle (slot -1) when the ledger is off
}

type egHeap[Dest any] []egItem[Dest]

func (h egHeap[Dest]) Len() int { return len(h) }
func (h egHeap[Dest]) Less(i, j int) bool {
	if h[i].at != h[j].at {
		return h[i].at < h[j].at
	}
	return h[i].ord < h[j].ord
}
func (h egHeap[Dest]) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *egHeap[Dest]) Push(x any)   { *h = append(*h, x.(egItem[Dest])) }
func (h *egHeap[Dest]) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}
