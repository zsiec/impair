package ristrelay

import (
	"net"
	"testing"
	"time"

	"github.com/zsiec/impair/engine"
)

// fixedDelay is a minimal engine cell that delays every packet by d ns — enough
// to exercise the shared min-heap scheduler the RIST relay now forwards through.
type fixedDelay struct{ d int64 }

func (fixedDelay) Name() string            { return "delay" }
func (fixedDelay) RequiresCleartext() bool { return false }
func (c fixedDelay) Process(in engine.InFlight) []engine.InFlight {
	in.DeliverAt = in.RecvAt + c.d
	return []engine.InFlight{in}
}

func loopback(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A clean engine forwards RTP media sender->receiver and passes RTCP through the
// odd port cleanly — the end-to-end smoke test of the dual-port datapath over the
// shared core.
func TestRistRelayForwardsMediaAndPassesRTCP(t *testing.T) {
	// The receiver needs an even/odd pair too (the relay derives recvRTCP = recvRTP+1).
	recvRTP, recvRTCP, err := bindEvenPair()
	if err != nil {
		t.Fatal(err)
	}
	defer recvRTP.Close()
	defer recvRTCP.Close()

	r, err := New(engine.New(nil, nil), recvRTP.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	relayRTP, _ := net.ResolveUDPAddr("udp", r.RTPAddr())
	relayRTCP := &net.UDPAddr{IP: relayRTP.IP, Port: relayRTP.Port + 1}

	// sender -> relay RTP -> receiver media.
	sender := loopback(t)
	defer sender.Close()
	if _, err := sender.WriteToUDP([]byte("rtp-media"), relayRTP); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = recvRTP.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := recvRTP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("receiver media read: %v", err)
	}
	if string(buf[:n]) != "rtp-media" {
		t.Fatalf("media = %q, want rtp-media", buf[:n])
	}

	// Stats.Forwarded should reflect the media forward (the egress bumps it async).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && r.Stats().Forwarded == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if r.Stats().Forwarded == 0 {
		t.Fatalf("Forwarded = 0, want >= 1")
	}

	// sender RTCP -> relay RTCP -> receiver RTCP (clean pass-through; learns the
	// sender's RTCP source so the reverse path could reach it).
	senderRTCP := loopback(t)
	defer senderRTCP.Close()
	if _, err := senderRTCP.WriteToUDP([]byte("rtcp-fb"), relayRTCP); err != nil {
		t.Fatal(err)
	}
	_ = recvRTCP.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = recvRTCP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("receiver RTCP read: %v", err)
	}
	if string(buf[:n]) != "rtcp-fb" {
		t.Fatalf("rtcp = %q, want rtcp-fb", buf[:n])
	}
}

// A delay cell must actually hold the forward in the min-heap and release it
// after the scheduled time — verifying RIST now forwards through the shared
// scheduler (not the old timer-per-packet path).
func TestRistRelaySchedulesDelay(t *testing.T) {
	recvRTP, recvRTCP, err := bindEvenPair()
	if err != nil {
		t.Fatal(err)
	}
	defer recvRTP.Close()
	defer recvRTCP.Close()

	const delay = 120 * time.Millisecond
	r, err := New(engine.New([]engine.Cell{fixedDelay{d: delay.Nanoseconds()}}, nil), recvRTP.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	sender := loopback(t)
	defer sender.Close()
	relayRTP, _ := net.ResolveUDPAddr("udp", r.RTPAddr())

	start := time.Now()
	if _, err := sender.WriteToUDP([]byte("delayed"), relayRTP); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = recvRTP.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := recvRTP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("delayed media read: %v", err)
	}
	elapsed := time.Since(start)
	if string(buf[:n]) != "delayed" {
		t.Fatalf("media = %q, want delayed", buf[:n])
	}
	// Allow scheduler slack but require most of the injected delay to have elapsed.
	if elapsed < delay*3/4 {
		t.Fatalf("forward arrived after %v, want >= ~%v (scheduler did not delay)", elapsed, delay*3/4)
	}
}
