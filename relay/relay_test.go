package relay

import (
	"bytes"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zsiec/impair/engine"
)

// A clean (no-cell) engine must forward both directions and tap every datagram.
func TestRelayForwardsBothDirections(t *testing.T) {
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	var taps int64
	r, err := New(engine.New(nil, nil), up.LocalAddr().String(), func(dir engine.Direction, data []byte) {
		atomic.AddInt64(&taps, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	relayAddr, _ := net.ResolveUDPAddr("udp", r.Addr())
	snd, err := net.DialUDP("udp", nil, relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer snd.Close()

	// sender -> relay -> upstream
	if _, err := snd.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	_ = up.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := up.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("upstream read: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("hello")) {
		t.Fatalf("upstream got %q", buf[:n])
	}

	// upstream -> relay -> sender (reply path; from == relay addr)
	if _, err := up.WriteToUDP([]byte("ack"), from); err != nil {
		t.Fatal(err)
	}
	_ = snd.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = snd.Read(buf)
	if err != nil {
		t.Fatalf("sender read reply: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("ack")) {
		t.Fatalf("sender got %q", buf[:n])
	}

	if atomic.LoadInt64(&taps) < 2 {
		t.Fatalf("expected >=2 taps, got %d", taps)
	}
	if st := r.Stats(); st.Forwarded < 2 {
		t.Fatalf("expected >=2 forwarded, got %+v", st)
	}
}

// A droplist engine that drops the first c2s packet must not forward it.
func TestRelayDrops(t *testing.T) {
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	// c2s pipeline drops seq 1; s2c empty.
	eng := engine.New([]engine.Cell{&dropFirst{}}, nil)
	r, err := New(eng, up.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	relayAddr, _ := net.ResolveUDPAddr("udp", r.Addr())
	snd, _ := net.DialUDP("udp", nil, relayAddr)
	defer snd.Close()

	_, _ = snd.Write([]byte("dropme"))
	_, _ = snd.Write([]byte("keepme"))

	buf := make([]byte, 1024)
	_ = up.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := up.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("upstream read: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("keepme")) {
		t.Fatalf("expected to drop first, got %q", buf[:n])
	}
}

type dropFirst struct{ seen bool }

func (d *dropFirst) Name() string { return "drop-first" }
func (d *dropFirst) Process(in engine.InFlight) []engine.InFlight {
	if !d.seen {
		d.seen = true
		return nil
	}
	return []engine.InFlight{in}
}
