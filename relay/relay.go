// Package relay is the SRT real-socket driver for the Sans-I/O engine: a
// symmetric UDP proxy that routes datagrams between a sender and an upstream
// receiver on a single port, applying the engine's deterministic impairment
// decisions. The scheduling/egress datapath, the OWD ledger, and the
// ground-truth counters live in the shared relaycore.Core; this package is the
// SRT-specific transport over it — one socket, single-port direction
// classification (sender<->upstream), and the SRT Tap. A Tap observes every
// datagram for the wire decoder.
package relay

import (
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/relaycore"
)

// Tap is invoked for every datagram the relay sees, before impairment, with the
// engine direction (C2S = sender->upstream, S2C = upstream->sender) and a fresh
// copy of the bytes it may keep.
type Tap func(dir engine.Direction, data []byte)

// Stats are relay-side ground truth counters. Dropped is impairment (the engine
// decided to drop); TailDropped is the egress queue's OWN overflow — kept
// separate so relay-induced loss never masquerades as modeled loss.
type Stats struct {
	Forwarded   uint64
	Dropped     uint64
	TailDropped uint64
}

// The OWD-ledger types are re-exported from relaycore so existing callers keep
// using relay.Entry / relay.DecomposedOWD unchanged.
type (
	Entry         = relaycore.Entry
	Decomposition = relaycore.Decomposition
	Quantile      = relaycore.Quantile
	PerPacketOWD  = relaycore.PerPacketOWD
)

// DecomposedOWD decomposes wire-side one-way delay from a ledger snapshot. See
// relaycore.DecomposedOWD.
func DecomposedOWD(entries []Entry) (Decomposition, []PerPacketOWD) {
	return relaycore.DecomposedOWD(entries)
}

// socketBuf is the kernel send/recv buffer requested per relay socket; larger
// buffers absorb bursts so kernel drops don't pollute the modeled loss.
const socketBuf = 1 << 22 // 4 MiB

// maxQueued bounds the egress heap. A var so tests can shrink it; passed to the
// core at construction.
var maxQueued = 16384

// RelayOption configures a Relay at construction (New / NewOn). Options are
// additive and order-independent; the zero set leaves all behavior unchanged.
type RelayOption func(*relayConfig)

type relayConfig struct {
	ledgerCap int // >0 enables the OWD ledger with this ring capacity
}

// WithLedger enables the OWD relay-ledger at construction with a ring capacity
// of capacity entries (<=0 uses the default). Without this option (or a later
// EnableLedger call) the ledger stays OFF and adds no per-packet cost.
func WithLedger(capacity int) RelayOption {
	return func(c *relayConfig) {
		if capacity <= 0 {
			capacity = relaycore.DefaultLedgerCap
		}
		c.ledgerCap = capacity
	}
}

// Relay proxies sender<->upstream through the engine on a single read goroutine,
// over a shared relaycore.Core.
type Relay struct {
	core     *relaycore.Core[netip.AddrPort]
	pc       *net.UDPConn
	upstream netip.AddrPort
	tap      Tap
	sender   atomic.Pointer[netip.AddrPort] // learned from the first non-upstream datagram
}

// New binds a relay socket on a free 127.0.0.1 port and forwards to upstreamAddr
// through eng. The sender address is learned from the first non-upstream
// datagram. tap may be nil.
func New(eng *engine.Engine, upstreamAddr string, tap Tap, opts ...RelayOption) (*Relay, error) {
	return NewOn(eng, "127.0.0.1:0", upstreamAddr, tap, opts...)
}

// NewOn is New with an explicit bind address — needed for protocols that derive
// peer ports from a base (e.g. RIST's even/odd pair).
func NewOn(eng *engine.Engine, bindAddr, upstreamAddr string, tap Tap, opts ...RelayOption) (*Relay, error) {
	var cfg relayConfig
	for _, o := range opts {
		o(&cfg)
	}
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
	r := &Relay{pc: pc, upstream: normAddr(up.AddrPort()), tap: tap}
	r.core = relaycore.New[netip.AddrPort](eng, func(dst netip.AddrPort, data []byte) {
		_, _ = pc.WriteToUDPAddrPort(data, dst)
	}, maxQueued, cfg.ledgerCap)
	r.core.Go(r.loop)
	return r, nil
}

// EnableLedger turns on the OWD relay-ledger at runtime (the opt-in alternative
// to the WithLedger construction option), replacing any existing one.
func (r *Relay) EnableLedger(capacity int) { r.core.EnableLedger(capacity) }

// Ledger returns a snapshot of the recorded OWD entries (oldest first), or nil
// if the ledger was never enabled. Feed it to DecomposedOWD.
func (r *Relay) Ledger() []Entry { return r.core.Ledger() }

// Addr is the relay's listen address (what the sender dials).
func (r *Relay) Addr() string { return r.pc.LocalAddr().String() }

// SetEngine atomically swaps the impairment engine — the runtime-mutation
// primitive behind live control.
func (r *Relay) SetEngine(eng *engine.Engine) { r.core.SetEngine(eng) }

// Stats returns a snapshot of relay ground-truth counters.
func (r *Relay) Stats() Stats {
	f, d, t := r.core.Stats()
	return Stats{Forwarded: f, Dropped: d, TailDropped: t}
}

// loop reads datagrams, classifies direction/destination, taps, and hands each
// to the core for impairment + scheduling.
func (r *Relay) loop() {
	buf := make([]byte, 2048)
	for {
		select {
		case <-r.core.Closed():
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
		recvAt := r.core.Now()

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
		r.core.Process(pkt, dir, dst, recvAt)
	}
}

// Close stops the relay and waits for its goroutines to exit. Idempotent.
func (r *Relay) Close() {
	r.core.Close(
		func() { _ = r.pc.SetReadDeadline(time.Now()) },
		func() { _ = r.pc.Close() },
	)
}

// normAddr strips an IPv4-in-IPv6 mapping so addresses compare equal regardless
// of how the socket surfaced them.
func normAddr(a netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}
