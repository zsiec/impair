// Package ristrelay is the dual-port real-socket driver for RIST Simple Profile,
// which (unlike SRT) does NOT multiplex: RTP media flows on an even port and
// RTCP (retransmit requests + reports) on the next odd port, with RTCP flowing
// receiver->sender. This relay binds an even/odd pair (R, R+1), impairs the RTP
// media through the Sans-I/O engine, and passes RTCP through cleanly so the ARQ
// requests survive. The sender's ephemeral RTCP address is DERIVED from its
// learned RTP source (port+1), per ristgo/RIST port pairing — so we never have
// to wait for the sender to speak RTCP first. A Tap observes every datagram.
package ristrelay

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/zsiec/impair/engine"
)

// Tap is invoked for every datagram seen (either port, either direction).
type Tap func(data []byte)

// Stats are relay-side ground-truth counters (media path).
type Stats struct {
	Forwarded uint64
	Dropped   uint64
}

// Relay proxies a RIST Simple-Profile sender<->receiver through eng.
type Relay struct {
	rtp, rtcp         *net.UDPConn
	recvRTP, recvRTCP *net.UDPAddr // the receiver's media (P) and RTCP (P+1)
	eng               *engine.Engine
	tap               Tap
	base              time.Time

	mu         sync.Mutex
	senderRTP  *net.UDPAddr // learned from incoming media on R
	senderRTCP *net.UDPAddr // learned from the sender's outgoing RTCP (ristgo's caller uses an INDEPENDENT ephemeral RTCP port, not media+1)
	stats      Stats
	tapMu      sync.Mutex // serializes tap across the two loops (Observer isn't concurrent-safe)

	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
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
	r := &Relay{
		rtp: rtp, rtcp: rtcp, recvRTP: rp, recvRTCP: rc,
		eng: eng, tap: tap, base: time.Now(), closed: make(chan struct{}),
	}
	r.wg.Add(2)
	go r.mediaLoop()
	go r.rtcpLoop()
	return r, nil
}

// RTPAddr is the even media port the sender dials; it derives RTCP as RTPAddr+1.
func (r *Relay) RTPAddr() string { return r.rtp.LocalAddr().String() }

// Stats snapshots the media-path counters.
func (r *Relay) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

// mediaLoop impairs RTP: sender->receiver through the engine, receiver->sender
// passed back to the learned sender.
func (r *Relay) mediaLoop() {
	defer r.wg.Done()
	buf := make([]byte, 2048)
	for {
		select {
		case <-r.closed:
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
		recvAt := time.Since(r.base).Nanoseconds()
		data := make([]byte, n)
		copy(data, buf[:n])
		r.observe(data)

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

		for _, a := range r.eng.Handle(engine.Packet{Data: data, Dir: dir}, recvAt) {
			if a.Kind == engine.Drop {
				r.mu.Lock()
				r.stats.Dropped++
				r.mu.Unlock()
				continue
			}
			r.scheduleRTP(a.Data, dst, a.DeliverAt-recvAt)
		}
	}
}

// rtcpLoop passes RTCP through cleanly (so retransmit requests survive),
// deriving the sender's RTCP address as its learned RTP source port + 1.
func (r *Relay) rtcpLoop() {
	defer r.wg.Done()
	buf := make([]byte, 2048)
	for {
		select {
		case <-r.closed:
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
		data := make([]byte, n)
		copy(data, buf[:n])
		r.observe(data)

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
		_, _ = r.rtcp.WriteToUDP(data, dst)
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

func (r *Relay) scheduleRTP(pkt []byte, dst *net.UDPAddr, delay int64) {
	send := func() {
		select {
		case <-r.closed:
			return
		default:
			_, _ = r.rtp.WriteToUDP(pkt, dst)
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

// Close stops both loops and drains scheduled forwards.
func (r *Relay) Close() {
	r.closeOnce.Do(func() {
		close(r.closed)
		_ = r.rtp.SetReadDeadline(time.Now())
		_ = r.rtcp.SetReadDeadline(time.Now())
		time.Sleep(50 * time.Millisecond)
		_ = r.rtp.Close()
		_ = r.rtcp.Close()
		r.wg.Wait()
	})
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
