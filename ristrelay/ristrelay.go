// Package ristrelay is the dual-port real-socket driver for RIST Simple Profile,
// which (unlike SRT) does NOT multiplex: RTP media flows on an even port and
// RTCP (retransmit requests + reports) on the next odd port. This relay binds an
// even/odd pair (R, R+1), impairs the RTP media through the shared relaycore.Core
// (the same min-heap scheduler the SRT relay uses), and passes RTCP through
// cleanly so the ARQ requests survive. The RIST-specific parts live here — the
// even/odd socket topology, the RTCP clean pass-through, and learning the
// sender's RTP/RTCP source addresses; the scheduling datapath is shared. A Tap
// observes every datagram.
package ristrelay

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/relaycore"
)

// Tap is invoked for every datagram seen (either port, either direction).
type Tap func(data []byte)

// Stats are relay-side ground-truth counters (media path).
type Stats struct {
	Forwarded uint64
	Dropped   uint64
}

// Relay proxies a RIST Simple-Profile sender<->receiver through eng, over a
// shared relaycore.Core for the impaired RTP media path.
type Relay struct {
	core              *relaycore.Core[*net.UDPAddr]
	rtp, rtcp         *net.UDPConn
	recvRTP, recvRTCP *net.UDPAddr // the receiver's media (P) and RTCP (P+1)
	tap               Tap

	mu         sync.Mutex
	senderRTP  *net.UDPAddr // learned from incoming media on R
	senderRTCP *net.UDPAddr // learned from the sender's outgoing RTCP (an INDEPENDENT ephemeral port, not media+1)
	tapMu      sync.Mutex   // serializes tap across the two loops (Observer isn't concurrent-safe)
}

// New binds an even/odd relay pair on 127.0.0.1 and bridges to the receiver's
// media address recvMediaAddr (its RTCP is taken as media port+1). eng impairs
// the RTP media path only. tap may be nil.
func New(eng *engine.Engine, recvMediaAddr string, tap Tap) (*Relay, error) {
	rp, err := net.ResolveUDPAddr("udp", recvMediaAddr)
	if err != nil {
		return nil, err
	}
	rc := &net.UDPAddr{IP: rp.IP, Port: rp.Port + 1}

	rtp, rtcp, err := bindEvenPair()
	if err != nil {
		return nil, err
	}
	r := &Relay{rtp: rtp, rtcp: rtcp, recvRTP: rp, recvRTCP: rc, tap: tap}
	r.core = relaycore.New[*net.UDPAddr](eng, func(dst *net.UDPAddr, data []byte) {
		_, _ = rtp.WriteToUDP(data, dst)
	}, 0, 0) // default queue bound; no OWD ledger on the RIST path
	r.core.Go(r.mediaLoop)
	r.core.Go(r.rtcpLoop)
	return r, nil
}

// SetEngine swaps the impairment engine live (interactive tuning); the next RTP
// packet is impaired by eng.
func (r *Relay) SetEngine(eng *engine.Engine) { r.core.SetEngine(eng) }

// RTPAddr is the even media port the sender dials; it derives RTCP as RTPAddr+1.
func (r *Relay) RTPAddr() string { return r.rtp.LocalAddr().String() }

// Stats snapshots the media-path counters.
func (r *Relay) Stats() Stats {
	f, d, _ := r.core.Stats()
	return Stats{Forwarded: f, Dropped: d}
}

// mediaLoop impairs RTP through the core: sender->receiver (C2S) is the media we
// impair; receiver->sender (S2C, rare) is passed to the learned sender.
func (r *Relay) mediaLoop() {
	buf := make([]byte, 2048)
	for {
		select {
		case <-r.core.Closed():
			return
		default:
		}
		_ = r.rtp.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, src, err := r.rtp.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		recvAt := r.core.Now()
		pkt := buf[:n]
		r.observe(pkt)

		var dir engine.Direction
		var dst *net.UDPAddr
		if udpEqual(src, r.recvRTP) {
			dir = engine.S2C // receiver -> sender (reverse media, rare)
			r.mu.Lock()
			dst = r.senderRTP
			r.mu.Unlock()
			if dst == nil {
				continue
			}
		} else {
			dir = engine.C2S // sender -> receiver (the media we impair)
			r.mu.Lock()
			if r.senderRTP == nil {
				r.senderRTP = src
			}
			r.mu.Unlock()
			dst = r.recvRTP
		}
		r.core.Process(pkt, dir, dst, recvAt)
	}
}

// rtcpLoop passes RTCP through cleanly (so retransmit requests survive),
// learning the sender's RTCP source so receiver->sender RTCP can reach it.
func (r *Relay) rtcpLoop() {
	buf := make([]byte, 2048)
	for {
		select {
		case <-r.core.Closed():
			return
		default:
		}
		_ = r.rtcp.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, src, err := r.rtcp.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		pkt := buf[:n]
		r.observe(pkt)

		var dst *net.UDPAddr
		if udpEqual(src, r.recvRTCP) {
			// receiver -> sender: forward to the sender's LEARNED RTCP address.
			r.mu.Lock()
			s := r.senderRTCP
			r.mu.Unlock()
			if s == nil {
				continue // sender hasn't spoken RTCP yet (its first SR establishes it)
			}
			dst = s
		} else {
			// sender -> receiver: learn the sender's RTCP source on the way.
			r.mu.Lock()
			if r.senderRTCP == nil {
				r.senderRTCP = src
			}
			r.mu.Unlock()
			dst = r.recvRTCP
		}
		_, _ = r.rtcp.WriteToUDP(pkt, dst)
	}
}

// observe serializes tap calls across the media and RTCP loops.
func (r *Relay) observe(data []byte) {
	if r.tap == nil {
		return
	}
	r.tapMu.Lock()
	r.tap(data)
	r.tapMu.Unlock()
}

// Close stops both loops and the egress, then closes the sockets.
func (r *Relay) Close() {
	r.core.Close(
		func() {
			_ = r.rtp.SetReadDeadline(time.Now())
			_ = r.rtcp.SetReadDeadline(time.Now())
		},
		func() {
			_ = r.rtp.Close()
			_ = r.rtcp.Close()
		},
	)
}

// bindEvenPair binds an even media port and the next odd port on 127.0.0.1.
func bindEvenPair() (rtp, rtcp *net.UDPConn, err error) {
	for tries := 0; tries < 64; tries++ {
		c, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if e != nil {
			continue
		}
		p := c.LocalAddr().(*net.UDPAddr).Port
		if p%2 != 0 {
			c.Close()
			continue
		}
		odd, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p + 1})
		if e != nil {
			c.Close()
			continue
		}
		return c, odd, nil
	}
	return nil, nil, fmt.Errorf("ristrelay: no free even/odd UDP port pair")
}

func udpEqual(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.IP.Equal(b.IP)
}
