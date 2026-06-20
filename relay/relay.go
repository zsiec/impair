// Package relay is the real-socket driver for the Sans-I/O engine: a symmetric
// UDP proxy that routes datagrams between a sender and an upstream receiver,
// applying the engine's deterministic impairment decisions and scheduling each
// forward at its computed delivery time. It is the Tier-2 datapath — real
// implementations stream through it over real sockets, so end-to-end timing is
// wall-clock (the impairment *schedule* is deterministic per arrival sequence +
// seed; absolute results are distribution-reproducible). A Tap observes every
// datagram for the wire decoder.
package relay

import (
	"net"
	"sync"
	"time"

	"github.com/zsiec/impair/engine"
)

// Tap is invoked for every datagram the relay sees, before impairment, with the
// engine direction (C2S = sender->upstream, S2C = upstream->sender) and a copy
// of the bytes. It must not retain the slice beyond the call's intent; the relay
// hands it a fresh copy it may keep.
type Tap func(dir engine.Direction, data []byte)

// Stats are relay-side ground truth counters.
type Stats struct {
	Forwarded uint64
	Dropped   uint64
}

// Relay proxies sender<->upstream through eng on a single read goroutine.
type Relay struct {
	pc       *net.UDPConn
	upstream *net.UDPAddr
	eng      *engine.Engine
	tap      Tap
	base     time.Time

	mu     sync.Mutex
	sender *net.UDPAddr
	stats  Stats

	closed chan struct{}
	wg     sync.WaitGroup
}

// New binds a relay socket on 127.0.0.1 and forwards to upstreamAddr through eng.
// The sender address is learned from the first non-upstream datagram. tap may be
// nil.
func New(eng *engine.Engine, upstreamAddr string, tap Tap) (*Relay, error) {
	up, err := net.ResolveUDPAddr("udp", upstreamAddr)
	if err != nil {
		return nil, err
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, err
	}
	r := &Relay{pc: pc, upstream: up, eng: eng, tap: tap, base: time.Now(), closed: make(chan struct{})}
	r.wg.Add(1)
	go r.loop()
	return r, nil
}

// Addr is the relay's listen address (what the sender dials).
func (r *Relay) Addr() string { return r.pc.LocalAddr().String() }

// Stats returns a snapshot of relay ground-truth counters.
func (r *Relay) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

func (r *Relay) loop() {
	defer r.wg.Done()
	buf := make([]byte, 2048)
	for {
		select {
		case <-r.closed:
			return
		default:
		}
		_ = r.pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, src, err := r.pc.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		recvAt := time.Since(r.base).Nanoseconds()

		var dir engine.Direction
		var dst *net.UDPAddr
		if udpEqual(src, r.upstream) {
			dir = engine.S2C
			r.mu.Lock()
			dst = r.sender
			r.mu.Unlock()
			if dst == nil {
				continue // no sender learned yet
			}
		} else {
			dir = engine.C2S
			r.mu.Lock()
			if r.sender == nil {
				r.sender = src
			}
			r.mu.Unlock()
			dst = r.upstream
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		if r.tap != nil {
			r.tap(dir, data)
		}

		for _, a := range r.eng.Handle(engine.Packet{Data: data, Dir: dir}, recvAt) {
			if a.Kind == engine.Drop {
				r.mu.Lock()
				r.stats.Dropped++
				r.mu.Unlock()
				continue
			}
			r.schedule(a.Data, dst, a.DeliverAt-recvAt)
		}
	}
}

// schedule forwards pkt to dst after delay (>=0). Delay 0 forwards inline.
func (r *Relay) schedule(pkt []byte, dst *net.UDPAddr, delay int64) {
	send := func() {
		select {
		case <-r.closed:
			return
		default:
			_, _ = r.pc.WriteToUDP(pkt, dst)
			r.mu.Lock()
			r.stats.Forwarded++
			r.mu.Unlock()
		}
	}
	if delay <= 0 {
		send()
		return
	}
	r.wg.Add(1)
	time.AfterFunc(time.Duration(delay), func() {
		defer r.wg.Done()
		send()
	})
}

// Close stops the relay and waits for in-flight scheduled forwards to drain.
func (r *Relay) Close() {
	close(r.closed)
	_ = r.pc.SetReadDeadline(time.Now())
	time.Sleep(50 * time.Millisecond) // let scheduled forwards fire
	_ = r.pc.Close()
	r.wg.Wait()
}

func udpEqual(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.IP.Equal(b.IP)
}
