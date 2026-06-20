package relay

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/zsiec/impair/engine"
)

// DecomposedOWD must be EXACT on a synthetic ledger: every component is a plain
// difference of two same-clock timestamps, and the aggregate p50/p99 are
// nearest-rank over the per-packet values. No sockets, no clock — pure math.
func TestDecomposedOWDExact(t *testing.T) {
	// Three packets with hand-chosen timestamps so the arithmetic is checkable by
	// eye. (RecvAt, DeliverAt, EgressAt) in ns.
	entries := []Entry{
		{Seq: 1, Dir: engine.C2S, RecvAt: 100, DeliverAt: 200, EgressAt: 210}, // q=100 o=10 res=110
		{Seq: 2, Dir: engine.C2S, RecvAt: 100, DeliverAt: 300, EgressAt: 305}, // q=200 o=5  res=205
		{Seq: 3, Dir: engine.S2C, RecvAt: 100, DeliverAt: 700, EgressAt: 730}, // q=600 o=30 res=630
	}
	dec, per := DecomposedOWD(entries)

	if len(per) != 3 {
		t.Fatalf("per-packet len = %d, want 3", len(per))
	}
	want := []PerPacketOWD{
		{Seq: 1, Dir: engine.C2S, Queue: 100, Overhead: 10, Residence: 110},
		{Seq: 2, Dir: engine.C2S, Queue: 200, Overhead: 5, Residence: 205},
		{Seq: 3, Dir: engine.S2C, Queue: 600, Overhead: 30, Residence: 630},
	}
	for i := range want {
		if per[i] != want[i] {
			t.Fatalf("per[%d] = %+v, want %+v", i, per[i], want[i])
		}
	}

	if dec.N != 3 {
		t.Fatalf("N = %d, want 3", dec.N)
	}
	// nearest-rank over 3 samples: p50 -> idx round(0.5*2)=1 (middle), p99 ->
	// idx round(0.99*2)=2 (max).
	// queue sorted: [100,200,600] -> p50=200 p99=600
	if dec.Queue.P50 != 200 || dec.Queue.P99 != 600 {
		t.Fatalf("queue p50/p99 = %d/%d, want 200/600", dec.Queue.P50, dec.Queue.P99)
	}
	// overhead sorted: [5,10,30] -> p50=10 p99=30
	if dec.Overhead.P50 != 10 || dec.Overhead.P99 != 30 {
		t.Fatalf("overhead p50/p99 = %d/%d, want 10/30", dec.Overhead.P50, dec.Overhead.P99)
	}
	// residence sorted: [110,205,630] -> p50=205 p99=630
	if dec.Residence.P50 != 205 || dec.Residence.P99 != 630 {
		t.Fatalf("residence p50/p99 = %d/%d, want 205/630", dec.Residence.P50, dec.Residence.P99)
	}
	// Residence == Queue + Overhead per packet (the decomposition invariant).
	for _, p := range per {
		if p.Residence != p.Queue+p.Overhead {
			t.Fatalf("seq %d: residence %d != queue %d + overhead %d", p.Seq, p.Residence, p.Queue, p.Overhead)
		}
	}
}

// Entries with no finalized egress (EgressAt == 0) are excluded — they left no
// residence to attribute. An all-unsent ledger yields a zero decomposition.
func TestDecomposedOWDSkipsUnsent(t *testing.T) {
	entries := []Entry{
		{Seq: 1, RecvAt: 10, DeliverAt: 20, EgressAt: 25}, // sent
		{Seq: 2, RecvAt: 10, DeliverAt: 30, EgressAt: 0},  // enqueued, never sent
	}
	dec, per := DecomposedOWD(entries)
	if len(per) != 1 || per[0].Seq != 1 {
		t.Fatalf("expected only seq 1 included, got %+v", per)
	}
	if dec.N != 1 || dec.Residence.N != 1 {
		t.Fatalf("N mismatch: dec.N=%d residence.N=%d", dec.N, dec.Residence.N)
	}

	none, perNone := DecomposedOWD([]Entry{{EgressAt: 0}})
	if none.N != 0 || len(perNone) != 0 {
		t.Fatalf("all-unsent should decompose to zero, got N=%d per=%d", none.N, len(perNone))
	}
	empty, perEmpty := DecomposedOWD(nil)
	if empty.N != 0 || len(perEmpty) != 0 {
		t.Fatalf("nil should decompose to zero, got N=%d per=%d", empty.N, len(perEmpty))
	}
}

// The ring is bounded and overwrite-oldest: more records than capacity keep only
// the most-recent cap entries, in chronological order, oldest first.
func TestLedgerRingBounded(t *testing.T) {
	l := newLedger(3)
	for i := 1; i <= 7; i++ { // 7 records into a cap-3 ring
		h := l.start(uint64(i), engine.C2S, int64(i*10), int64(i*10+1))
		l.finalize(h, int64(i*10+2))
	}
	snap := l.snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3 (bounded by cap)", len(snap))
	}
	// Only seqs 5,6,7 survive, oldest first.
	for i, want := range []uint64{5, 6, 7} {
		if snap[i].Seq != want {
			t.Fatalf("snap[%d].Seq = %d, want %d", i, snap[i].Seq, want)
		}
		if snap[i].EgressAt == 0 {
			t.Fatalf("snap[%d] not finalized", i)
		}
	}
}

// Before the ring wraps, the snapshot is the records in insertion order.
func TestLedgerSnapshotOrderUnwrapped(t *testing.T) {
	l := newLedger(8)
	for i := 1; i <= 4; i++ {
		h := l.start(uint64(i), engine.C2S, int64(i), int64(i))
		l.finalize(h, int64(i))
	}
	snap := l.snapshot()
	if len(snap) != 4 {
		t.Fatalf("len = %d, want 4", len(snap))
	}
	for i, want := range []uint64{1, 2, 3, 4} {
		if snap[i].Seq != want {
			t.Fatalf("snap[%d].Seq = %d, want %d", i, snap[i].Seq, want)
		}
	}
}

// A finalize against a slot that has since been reused (generation mismatch) is
// a no-op — it must never corrupt the entry that now occupies the slot.
func TestLedgerFinalizeStaleHandle(t *testing.T) {
	l := newLedger(1) // cap 1: every start reuses slot 0
	h1 := l.start(1, engine.C2S, 10, 20)
	h2 := l.start(2, engine.C2S, 30, 40) // overwrites slot 0, bumps generation
	l.finalize(h1, 999)                  // stale: must be ignored
	l.finalize(h2, 50)
	snap := l.snapshot()
	if len(snap) != 1 {
		t.Fatalf("len = %d, want 1", len(snap))
	}
	if snap[0].Seq != 2 || snap[0].EgressAt != 50 {
		t.Fatalf("stale finalize corrupted entry: %+v", snap[0])
	}
}

// Ledger OFF (default): Ledger() is nil and the hot path records nothing. This
// is the zero-overhead guarantee — an un-enabled relay carries no entries.
func TestLedgerOffByDefault(t *testing.T) {
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	r, err := New(engine.New(nil, nil), up.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Ledger() != nil {
		t.Fatalf("ledger must be nil when not enabled, got %v", r.Ledger())
	}

	relayAddr, _ := net.ResolveUDPAddr("udp", r.Addr())
	snd, _ := net.DialUDP("udp", nil, relayAddr)
	defer snd.Close()

	_, _ = snd.Write([]byte("hello"))
	buf := make([]byte, 64)
	_ = up.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := up.ReadFromUDP(buf); err != nil {
		t.Fatalf("upstream read: %v", err)
	}
	if got := r.Ledger(); got != nil {
		t.Fatalf("ledger still off after forwarding, got %d entries", len(got))
	}
}

// Under a KNOWN fixed-delay scenario the ledger records each forward with the
// expected component structure: queue ~= the injected delay, residence >= queue,
// overhead = residence - queue >= 0. Exact wall-clock values are non-determin-
// istic (Tier-2), so we assert the structural invariants the decomposition
// guarantees, plus that queue tracks the injected delay closely.
func TestLedgerRecordsUnderFixedDelay(t *testing.T) {
	const injected = 15 * time.Millisecond
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	eng := engine.New([]engine.Cell{&fixedDelay{d: injected.Nanoseconds()}}, nil)
	r, err := New(eng, up.LocalAddr().String(), nil, WithLedger(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Ledger() == nil {
		t.Fatalf("ledger should be enabled (non-nil snapshot) via WithLedger")
	}

	relayAddr, _ := net.ResolveUDPAddr("udp", r.Addr())
	snd, _ := net.DialUDP("udp", nil, relayAddr)
	defer snd.Close()

	const n = 20
	// Drain upstream so writes don't backlog.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64)
		_ = up.SetReadDeadline(time.Now().Add(10 * time.Second))
		for got := 0; got < n; got++ {
			if _, _, err := up.ReadFromUDP(buf); err != nil {
				return
			}
		}
	}()

	msg := make([]byte, 8)
	for i := 0; i < n; i++ {
		binary.BigEndian.PutUint64(msg, uint64(i))
		if _, err := snd.Write(msg); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out draining upstream")
	}
	// Let the last egress finalize.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(r.Ledger()) >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	entries := r.Ledger()
	if len(entries) < n {
		t.Fatalf("recorded %d entries, want >= %d", len(entries), n)
	}

	dec, per := DecomposedOWD(entries)
	if dec.N < n {
		t.Fatalf("decomposed N=%d, want >= %d (entries finalized)", dec.N, n)
	}
	for _, p := range per {
		if p.Dir != engine.C2S {
			t.Fatalf("seq %d dir = %v, want C2S", p.Seq, p.Dir)
		}
		if p.Residence != p.Queue+p.Overhead {
			t.Fatalf("seq %d: residence %d != queue %d + overhead %d", p.Seq, p.Residence, p.Queue, p.Overhead)
		}
		if p.Overhead < 0 {
			t.Fatalf("seq %d: negative overhead %d (egress before scheduled time)", p.Seq, p.Overhead)
		}
		// Queue is the engine-scheduled delay = DeliverAt-RecvAt, which the
		// fixedDelay cell pins to exactly the injected delay.
		if p.Queue != injected.Nanoseconds() {
			t.Fatalf("seq %d: queue %d != injected %d (scheduled delay is exact)", p.Seq, p.Queue, injected.Nanoseconds())
		}
	}
	// Median residence is queue + a small relay self-overhead; it must be at
	// least the injected delay.
	if dec.Residence.P50 < injected.Nanoseconds() {
		t.Fatalf("p50 residence %d below injected %d", dec.Residence.P50, injected.Nanoseconds())
	}
	t.Logf("queue p50=%v overhead p50=%v residence p50=%v",
		time.Duration(dec.Queue.P50), time.Duration(dec.Overhead.P50), time.Duration(dec.Residence.P50))
}

// EnableLedger turns recording on at runtime: forwards after the call are
// recorded, and Ledger() goes from nil to a live snapshot.
func TestEnableLedgerRuntime(t *testing.T) {
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	r, err := New(engine.New(nil, nil), up.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Ledger() != nil {
		t.Fatalf("ledger should start off")
	}

	r.EnableLedger(256) // flip on at runtime
	if r.Ledger() == nil {
		t.Fatalf("ledger should be on after EnableLedger")
	}

	relayAddr, _ := net.ResolveUDPAddr("udp", r.Addr())
	snd, _ := net.DialUDP("udp", nil, relayAddr)
	defer snd.Close()

	_, _ = snd.Write([]byte("after-enable"))
	buf := make([]byte, 64)
	_ = up.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := up.ReadFromUDP(buf); err != nil {
		t.Fatalf("upstream read: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(r.Ledger()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.Ledger(); len(got) < 1 {
		t.Fatalf("expected >=1 recorded entry after EnableLedger, got %d", len(got))
	}
}
