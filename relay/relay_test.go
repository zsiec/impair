package relay

import (
	"bytes"
	"encoding/binary"
	"net"
	"sort"
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

// fixedDelay delays every packet by exactly d (ns) — a clean target for the
// self-overhead probe: observed latency minus d is the relay's own overhead.
type fixedDelay struct{ d int64 }

func (f *fixedDelay) Name() string { return "fixed-delay" }
func (f *fixedDelay) Process(in engine.InFlight) []engine.InFlight {
	in.DeliverAt = in.RecvAt + f.d
	return []engine.InFlight{in}
}

// TestRelaySelfOverhead is the P0.f CI gate: the relay's own scheduling overhead
// must be a small fraction of the injected delay. We inject a fixed 20 ms delay
// and require the MEDIAN delivered latency to land within 5% of it — i.e. the
// timer-heap egress adds < ~1 ms over the injected delay (median is robust to
// occasional OS scheduling spikes that a Tier-2 wall-clock datapath can't avoid).
func TestRelaySelfOverhead(t *testing.T) {
	const injected = 20 * time.Millisecond
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	eng := engine.New([]engine.Cell{&fixedDelay{d: injected.Nanoseconds()}}, nil)
	r, err := New(eng, up.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	relayAddr, _ := net.ResolveUDPAddr("udp", r.Addr())
	snd, _ := net.DialUDP("udp", nil, relayAddr)
	defer snd.Close()

	const n = 120
	recv := make([]time.Duration, n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64)
		_ = up.SetReadDeadline(time.Now().Add(10 * time.Second))
		for got := 0; got < n; got++ {
			m, _, err := up.ReadFromUDP(buf)
			if err != nil || m < 12 {
				return
			}
			seq := int(binary.BigEndian.Uint32(buf[:4]))
			sentNs := int64(binary.BigEndian.Uint64(buf[4:12]))
			if seq >= 0 && seq < n {
				recv[seq] = time.Duration(time.Now().UnixNano() - sentNs)
			}
		}
	}()

	// Pace sends ~2 ms apart so the upstream reader never backlogs (which would
	// add serialization latency unrelated to the relay).
	msg := make([]byte, 16)
	for i := 0; i < n; i++ {
		binary.BigEndian.PutUint32(msg[:4], uint32(i))
		binary.BigEndian.PutUint64(msg[4:12], uint64(time.Now().UnixNano()))
		if _, err := snd.Write(msg); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for delivered packets")
	}

	lat := make([]time.Duration, 0, n)
	for _, d := range recv {
		if d > 0 {
			lat = append(lat, d)
		}
	}
	if len(lat) < n*9/10 {
		t.Fatalf("only %d/%d packets delivered", len(lat), n)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	median := lat[len(lat)/2]
	overhead := median - injected
	budget := injected / 20 // 5%
	t.Logf("injected=%v median=%v overhead=%v (budget %v)", injected, median, overhead, budget)
	if median < injected {
		t.Fatalf("median latency %v below injected %v — delay not applied", median, injected)
	}
	if overhead > budget {
		t.Fatalf("relay self-overhead %v exceeds 5%% of injected delay (%v)", overhead, budget)
	}
}

// BenchmarkRelayThroughput pushes datagrams through a clean (no-delay) relay and
// measures per-packet cost; run with -benchmem to confirm the sync.Pool keeps
// steady-state allocations off the forward path.
func BenchmarkRelayThroughput(b *testing.B) {
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		b.Fatal(err)
	}
	defer up.Close()
	// Drain upstream continuously so the socket buffer never blocks the relay.
	stop := make(chan struct{})
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = up.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			_, _, _ = up.ReadFromUDP(buf)
		}
	}()
	defer close(stop)

	r, err := New(engine.New(nil, nil), up.LocalAddr().String(), nil)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	relayAddr, _ := net.ResolveUDPAddr("udp", r.Addr())
	snd, _ := net.DialUDP("udp", nil, relayAddr)
	defer snd.Close()

	msg := make([]byte, 1316) // SRT-sized payload
	b.SetBytes(int64(len(msg)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := snd.Write(msg); err != nil {
			b.Fatal(err)
		}
	}
}
