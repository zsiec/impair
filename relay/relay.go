// Package relay is the real-socket driver for the Sans-I/O engine: a symmetric
// UDP proxy that routes datagrams between a sender and an upstream receiver,
// applying the engine's deterministic impairment decisions and scheduling each
// forward at its computed delivery time. It is the Tier-2 datapath — real
// implementations stream through it over real sockets, so end-to-end timing is
// wall-clock (the impairment *schedule* is deterministic per arrival sequence +
// seed; absolute results are distribution-reproducible). A Tap observes every
// datagram for the wire decoder.
//
// Datapath: a read goroutine applies the engine per datagram and enqueues each
// surviving forward on a min-heap ordered by (delivery time, arrival order); a
// single egress goroutine drains the heap, sleeping on one reusable timer until
// the next forward is due. This replaces a timer+goroutine per delayed packet,
// and forward payloads come from a sync.Pool, so the steady-state hot path
// allocates ~nothing per packet.
package relay

import (
	"container/heap"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zsiec/impair/engine"
)

// Tap is invoked for every datagram the relay sees, before impairment, with the
// engine direction (C2S = sender->upstream, S2C = upstream->sender) and a copy
// of the bytes. It must not retain the slice beyond the call's intent; the relay
// hands it a fresh copy it may keep.
type Tap func(dir engine.Direction, data []byte)

// Stats are relay-side ground truth counters. Dropped is impairment (the engine
// decided to drop); TailDropped is the relay's OWN overflow (the bounded egress
// queue was full) — kept separate so relay-induced loss never masquerades as
// modeled loss.
type Stats struct {
	Forwarded   uint64
	Dropped     uint64
	TailDropped uint64
}

const maxDatagram = 2048

// maxQueued bounds the egress heap (scheduled-but-not-yet-due forwards). Beyond
// it, the relay tail-drops arriving forwards rather than grow without limit — so
// a sustained over-rate (or a huge injected delay × high ingress) degrades as
// bounded tail-drop, recorded in Stats, instead of unbounded memory. ~16K
// datagrams ≈ 32 MB at maxDatagram. A var so tests can shrink it.
var maxQueued = 16384

// socketBuf is the kernel send/recv buffer size requested per relay socket;
// larger buffers absorb bursts so kernel drops don't pollute the modeled loss.
const socketBuf = 1 << 22 // 4 MiB

// Relay proxies sender<->upstream through eng on a single read goroutine. eng is
// held atomically so it can be swapped at runtime (SetEngine) for live control:
// the loop reads the current engine per packet, and a mutation just changes which
// engine the next packet uses. A live-mutated run is no longer bit-deterministic
// (the impairment changes mid-stream) — that is the Tier-2 live-control trade-off.
type Relay struct {
	pc       *net.UDPConn
	upstream netip.AddrPort
	eng      atomic.Pointer[engine.Engine]
	tap      Tap
	base     time.Time

	sender              atomic.Pointer[netip.AddrPort] // learned from the first non-upstream datagram
	fwd, drop, tailDrop atomic.Uint64

	egMu  sync.Mutex
	eg    egHeap
	egOrd uint64
	wake  chan struct{} // buffered(1): a new head was enqueued; egress should re-check
	pool  sync.Pool     // *[]byte (cap maxDatagram) forward buffers

	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New binds a relay socket on a free 127.0.0.1 port and forwards to upstreamAddr
// through eng. The sender address is learned from the first non-upstream
// datagram. tap may be nil.
func New(eng *engine.Engine, upstreamAddr string, tap Tap) (*Relay, error) {
	return NewOn(eng, "127.0.0.1:0", upstreamAddr, tap)
}

// NewOn is New with an explicit bind address — needed for protocols that derive
// peer ports from a base (e.g. RIST Simple Profile uses RTP on an even port and
// RTCP on port+1, so a dual-port relay must bind a specific even/odd pair).
func NewOn(eng *engine.Engine, bindAddr, upstreamAddr string, tap Tap) (*Relay, error) {
	up, err := net.ResolveUDPAddr("udp", upstreamAddr)
	if err != nil {
		return nil, err
	}
	bind, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		return nil, err
	}
	pc, err := net.ListenUDP("udp", bind)
	if err != nil {
		return nil, err
	}
	_ = pc.SetReadBuffer(socketBuf) // best-effort: absorb bursts, fewer kernel drops
	_ = pc.SetWriteBuffer(socketBuf)
	r := &Relay{
		pc: pc, upstream: normAddr(up.AddrPort()), tap: tap, base: time.Now(),
		wake:   make(chan struct{}, 1),
		closed: make(chan struct{}),
		pool:   sync.Pool{New: func() any { b := make([]byte, maxDatagram); return &b }},
	}
	r.eng.Store(eng)
	r.wg.Add(2)
	go r.loop()
	go r.egress()
	return r, nil
}

// Addr is the relay's listen address (what the sender dials).
func (r *Relay) Addr() string { return r.pc.LocalAddr().String() }

// SetEngine atomically swaps the impairment engine the relay applies to
// subsequent packets — the runtime-mutation primitive behind live control
// (Toxiproxy-style toxics). The previous engine is simply no longer consulted; in
// flight scheduled forwards already committed by the old engine still fire.
func (r *Relay) SetEngine(eng *engine.Engine) { r.eng.Store(eng) }

// Stats returns a snapshot of relay ground-truth counters.
func (r *Relay) Stats() Stats {
	return Stats{Forwarded: r.fwd.Load(), Dropped: r.drop.Load(), TailDropped: r.tailDrop.Load()}
}

func (r *Relay) loop() {
	defer r.wg.Done()
	buf := make([]byte, maxDatagram)
	for {
		select {
		case <-r.closed:
			return
		default:
		}
		_ = r.pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, rawSrc, err := r.pc.ReadFromUDPAddrPort(buf) // AddrPort is a value — no per-read alloc
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		src := normAddr(rawSrc) // strip IPv4-in-IPv6 so == matches the resolved upstream
		recvAt := time.Since(r.base).Nanoseconds()

		var dir engine.Direction
		var dst netip.AddrPort
		if src == r.upstream {
			dir = engine.S2C
			sp := r.sender.Load()
			if sp == nil {
				continue // no sender learned yet
			}
			dst = *sp
		} else {
			dir = engine.C2S
			if r.sender.Load() == nil {
				s := src
				r.sender.CompareAndSwap(nil, &s)
			}
			dst = r.upstream
		}

		pkt := buf[:n]
		if r.tap != nil {
			c := make([]byte, n)
			copy(c, pkt)
			r.tap(dir, c)
		}

		// The engine returns all actions for this packet synchronously (reordering
		// is expressed via DeliverAt, not slice retention), so pkt may be reused on
		// the next read once the enqueue copies survive it.
		for _, a := range r.eng.Load().Handle(engine.Packet{Data: pkt, Dir: dir}, recvAt) {
			if a.Kind == engine.Drop {
				r.drop.Add(1)
				continue
			}
			r.enqueue(a.Data, dst, a.DeliverAt)
		}
	}
}

// enqueue copies a forward payload into a pooled buffer and pushes it on the
// egress heap, waking the egress goroutine if this forward is now the soonest.
func (r *Relay) enqueue(data []byte, dst netip.AddrPort, at int64) {
	bp := r.getBuf(len(data))
	copy((*bp)[:len(data)], data)

	r.egMu.Lock()
	if len(r.eg) >= maxQueued { // bounded: reject-on-full (tail-drop)
		r.egMu.Unlock()
		r.putBuf(bp)
		r.tailDrop.Add(1)
		return
	}
	r.egOrd++
	it := egItem{at: at, ord: r.egOrd, buf: bp, n: len(data), dst: dst}
	heap.Push(&r.eg, it)
	isHead := r.eg[0].ord == it.ord
	r.egMu.Unlock()

	if isHead {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// egress drains due forwards in (delivery time, arrival order) on one goroutine,
// sleeping on a single reusable timer until the next is due. On Close it returns
// with any not-yet-due forwards undelivered (matching the prior in-flight-drop
// semantics — a run's tail beyond its delay is not delivered after teardown).
func (r *Relay) egress() {
	defer r.wg.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		r.egMu.Lock()
		now := time.Since(r.base).Nanoseconds()
		for len(r.eg) > 0 && r.eg[0].at <= now {
			it := heap.Pop(&r.eg).(egItem)
			r.egMu.Unlock()
			r.send(it)
			r.egMu.Lock()
			now = time.Since(r.base).Nanoseconds()
		}
		wait := time.Hour
		if len(r.eg) > 0 {
			if d := time.Duration(r.eg[0].at - now); d < time.Hour {
				wait = d
			}
		}
		r.egMu.Unlock()

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-r.closed:
			return
		case <-r.wake:
		case <-timer.C:
		}
	}
}

func (r *Relay) send(it egItem) {
	select {
	case <-r.closed:
		return
	default:
	}
	_, _ = r.pc.WriteToUDPAddrPort((*it.buf)[:it.n], it.dst)
	r.fwd.Add(1)
	r.putBuf(it.buf)
}

func (r *Relay) getBuf(n int) *[]byte {
	if n > maxDatagram {
		b := make([]byte, n)
		return &b
	}
	return r.pool.Get().(*[]byte)
}

func (r *Relay) putBuf(bp *[]byte) {
	if cap(*bp) >= maxDatagram {
		*bp = (*bp)[:maxDatagram]
		r.pool.Put(bp)
	}
}

// Close stops the relay and waits for both goroutines to exit. It is idempotent
// (safe to call explicitly before reading the observer and again via defer).
func (r *Relay) Close() {
	r.closeOnce.Do(func() {
		close(r.closed)
		_ = r.pc.SetReadDeadline(time.Now())
		r.wg.Wait()
		_ = r.pc.Close()
	})
}

// normAddr strips an IPv4-in-IPv6 mapping so addresses compare equal regardless
// of how the socket surfaced them (a read may yield ::ffff:127.0.0.1 while the
// resolved upstream is 127.0.0.1).
func normAddr(a netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

// egItem is one scheduled forward. ord (relay enqueue counter) is the stable
// tiebreaker for equal delivery times, so egress order is deterministic given
// the deterministic arrival + engine-action order.
type egItem struct {
	at  int64
	ord uint64
	buf *[]byte
	n   int
	dst netip.AddrPort
}

type egHeap []egItem

func (h egHeap) Len() int { return len(h) }
func (h egHeap) Less(i, j int) bool {
	if h[i].at != h[j].at {
		return h[i].at < h[j].at
	}
	return h[i].ord < h[j].ord
}
func (h egHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *egHeap) Push(x any)   { *h = append(*h, x.(egItem)) }
func (h *egHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}
