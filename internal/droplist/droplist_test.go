package droplist

import (
	"reflect"
	"testing"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/rng"
)

func sub(name string) *rng.Source { return rng.NewRoot(1).Sub(name) }

const ms = int64(1_000_000) // ns per ms

func mkPkt(seq uint64, recvMs int64, n int) engine.InFlight {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i)
	}
	rn := recvMs * ms
	return engine.InFlight{Seq: seq, Dir: engine.C2S, Data: data, RecvAt: rn, DeliverAt: rn}
}

// ---------------------------------------------------------------------------
// DropList
// ---------------------------------------------------------------------------

func TestDropListDropsExactlyListed(t *testing.T) {
	cell := NewDropList(DropListConfig{Seqs: []uint64{2, 3, 7}}, sub("dl"))
	drop := map[uint64]bool{2: true, 3: true, 7: true}

	for seq := uint64(1); seq <= 10; seq++ {
		out := cell.Process(mkPkt(seq, 0, 100))
		if drop[seq] {
			if len(out) != 0 {
				t.Fatalf("seq %d: expected drop, got %d outputs", seq, len(out))
			}
		} else {
			if len(out) != 1 {
				t.Fatalf("seq %d: expected pass, got %d outputs", seq, len(out))
			}
			if out[0].Seq != seq {
				t.Fatalf("seq %d: passed packet had Seq %d", seq, out[0].Seq)
			}
		}
	}
}

func TestDropListEmptyPassesAll(t *testing.T) {
	cell := NewDropList(DropListConfig{}, sub("dl"))
	for seq := uint64(1); seq <= 5; seq++ {
		if out := cell.Process(mkPkt(seq, 0, 10)); len(out) != 1 {
			t.Fatalf("zero-value DropList dropped seq %d", seq)
		}
	}
}

func TestDropListDoesNotMutateOrDelay(t *testing.T) {
	cell := NewDropList(DropListConfig{Seqs: []uint64{99}}, sub("dl"))
	in := mkPkt(1, 5, 50)
	out := cell.Process(in)
	if len(out) != 1 {
		t.Fatal("expected pass")
	}
	if out[0].DeliverAt != in.RecvAt {
		t.Fatalf("DropList changed DeliverAt: got %d want %d", out[0].DeliverAt, in.RecvAt)
	}
	if !reflect.DeepEqual(out[0].Data, in.Data) {
		t.Fatal("DropList altered Data")
	}
}

func TestDropListName(t *testing.T) {
	if n := NewDropList(DropListConfig{}, sub("x")).Name(); n != "droplist" {
		t.Fatalf("Name = %q", n)
	}
}

func TestParseDropList(t *testing.T) {
	cfg, err := ParseDropList("#2, #3, #7")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Seqs, []uint64{2, 3, 7}) {
		t.Fatalf("got %v", cfg.Seqs)
	}
	cfg, err = ParseDropList(" 10 20\t30 ")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Seqs, []uint64{10, 20, 30}) {
		t.Fatalf("got %v", cfg.Seqs)
	}
	if _, err := ParseDropList("1,abc,3"); err == nil {
		t.Fatal("expected error on bad token")
	}
}

func TestDropListDeterministic(t *testing.T) {
	run := func() []bool {
		cell := NewDropList(DropListConfig{Seqs: []uint64{1, 4}}, sub("dl"))
		var got []bool
		for seq := uint64(1); seq <= 6; seq++ {
			got = append(got, len(cell.Process(mkPkt(seq, 0, 10))) == 1)
		}
		return got
	}
	if !reflect.DeepEqual(run(), run()) {
		t.Fatal("non-deterministic")
	}
}

// ---------------------------------------------------------------------------
// DeliveryTrace
// ---------------------------------------------------------------------------

func TestDeliveryTraceName(t *testing.T) {
	c := NewDeliveryTrace(DeliveryTraceConfig{Schedule: []int64{0}}, sub("dt"))
	if c.Name() != "deliverytrace" {
		t.Fatalf("Name = %q", c.Name())
	}
}

// Packets all arrive at t=0; opportunities pace them out one per timestamp.
func TestDeliveryTracePacesToSchedule(t *testing.T) {
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule: []int64{0, 10, 25, 40},
		MTU:      1500,
	}, sub("dt"))

	wantMs := []int64{0, 10, 25, 40}
	for i, w := range wantMs {
		out := cell.Process(mkPkt(uint64(i+1), 0, 100))
		if len(out) != 1 {
			t.Fatalf("pkt %d dropped unexpectedly", i)
		}
		if got := out[0].DeliverAt; got != w*ms {
			t.Fatalf("pkt %d: DeliverAt %d ms, want %d ms", i, got/ms, w)
		}
	}
	// 5th packet has no opportunity (non-looping) => dropped.
	if out := cell.Process(mkPkt(5, 0, 100)); len(out) != 0 {
		t.Fatalf("5th packet should drop (schedule exhausted), got %d", len(out))
	}
}

// A packet larger than MTU consumes several opportunities; DeliverAt is the
// last one.
func TestDeliveryTraceMultiMTUPacket(t *testing.T) {
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule: []int64{0, 10, 20, 30},
		MTU:      100,
	}, sub("dt"))
	// 250 bytes => ceil(250/100)=3 opportunities: 0,10,20 -> deliver at 20.
	out := cell.Process(mkPkt(1, 0, 250))
	if len(out) != 1 {
		t.Fatal("dropped")
	}
	if out[0].DeliverAt != 20*ms {
		t.Fatalf("DeliverAt %d ms, want 20", out[0].DeliverAt/ms)
	}
	// next packet uses opportunity index 3 (30ms).
	out = cell.Process(mkPkt(2, 0, 50))
	if len(out) != 1 || out[0].DeliverAt != 30*ms {
		t.Fatalf("2nd pkt: got %v", out)
	}
}

// Opportunities before a packet's arrival are missed (idle link).
func TestDeliveryTraceIdleLinkSkipsPastOpportunities(t *testing.T) {
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule: []int64{0, 5, 10, 100, 200},
		MTU:      1500,
	}, sub("dt"))
	// First packet arrives at 0 -> opp 0 (0ms).
	if out := cell.Process(mkPkt(1, 0, 10)); out[0].DeliverAt != 0 {
		t.Fatalf("pkt1 DeliverAt %d", out[0].DeliverAt)
	}
	// Second packet arrives at 50ms. Opps 5 and 10 are in the past (link idle,
	// no traffic) and were not consumed; next opp >= 50 is 100ms.
	out := cell.Process(mkPkt(2, 50, 10))
	if len(out) != 1 || out[0].DeliverAt != 100*ms {
		t.Fatalf("pkt2: want deliver 100ms, got %v", out)
	}
}

func TestDeliveryTraceNeverDeliversBeforeArrival(t *testing.T) {
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule: []int64{0, 100},
		MTU:      1500,
	}, sub("dt"))
	// Packet arrives at 100ms; nearest opp at-or-after is 100ms. base is set
	// from THIS first packet (RecvAt 100ms => trace 0). So opp 0 maps to 100ms.
	out := cell.Process(mkPkt(1, 100, 10))
	if len(out) != 1 {
		t.Fatal("dropped")
	}
	if out[0].DeliverAt < out[0].RecvAt {
		t.Fatalf("DeliverAt %d < RecvAt %d", out[0].DeliverAt, out[0].RecvAt)
	}
}

// Backlog: many packets arrive simultaneously; only those whose queued bytes
// stay within MaxBacklog are admitted, the rest are tail-dropped.
func TestDeliveryTraceBacklogBound(t *testing.T) {
	// 10 opportunities, MTU 1500, MaxBacklog 4500 => at most 3 MTUs may be
	// queued ahead of (and including) an admitted packet at its arrival instant.
	sched := []int64{0, 10, 20, 30, 40, 50, 60, 70, 80, 90}
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule:   sched,
		MTU:        1500,
		MaxBacklog: 4500,
	}, sub("dt"))

	admitted := 0
	dropped := 0
	// 8 packets all arrive at t=0.
	for i := 0; i < 8; i++ {
		out := cell.Process(mkPkt(uint64(i+1), 0, 1500))
		if len(out) == 1 {
			admitted++
		} else {
			dropped++
		}
	}
	// queuedBytes for the k-th admitted (0-based) packet at arrival = (k+1)*MTU.
	// Admit while (k+1)*1500 <= 4500 => k+1 <= 3 => 3 admitted, 5 dropped.
	if admitted != 3 || dropped != 5 {
		t.Fatalf("backlog bound: admitted=%d dropped=%d, want 3/5", admitted, dropped)
	}
}

func TestDeliveryTraceUnboundedBacklog(t *testing.T) {
	sched := []int64{0, 10, 20, 30, 40}
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule:   sched,
		MTU:        1500,
		MaxBacklog: 0, // unbounded
	}, sub("dt"))
	for i := 0; i < len(sched); i++ {
		if out := cell.Process(mkPkt(uint64(i+1), 0, 1500)); len(out) != 1 {
			t.Fatalf("pkt %d dropped under unbounded backlog", i)
		}
	}
}

// Looping trace replays its schedule cyclically.
func TestDeliveryTraceLoop(t *testing.T) {
	// period = 30ms, opps at 0,10,20 each cycle.
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule: []int64{0, 10, 20, 30},
		MTU:      1500,
		Loop:     true,
	}, sub("dt"))
	// First 4 opps: 0,10,20,30. 5th opp loops: cycle 1 => period*1 + sched[0] =
	// 30 + 0 = 30; 6th => 30+10=40 ...
	wantMs := []int64{0, 10, 20, 30, 30, 40, 50, 60}
	for i, w := range wantMs {
		out := cell.Process(mkPkt(uint64(i+1), 0, 100))
		if len(out) != 1 {
			t.Fatalf("loop pkt %d dropped", i)
		}
		if out[0].DeliverAt != w*ms {
			t.Fatalf("loop pkt %d: DeliverAt %d ms, want %d", i, out[0].DeliverAt/ms, w)
		}
	}
}

func TestDeliveryTraceEmptyScheduleDropsAll(t *testing.T) {
	cell := NewDeliveryTrace(DeliveryTraceConfig{Schedule: nil, MTU: 1500}, sub("dt"))
	if out := cell.Process(mkPkt(1, 0, 10)); len(out) != 0 {
		t.Fatal("empty schedule must drop everything")
	}
}

func TestDeliveryTraceDefaultMTU(t *testing.T) {
	cell := NewDeliveryTrace(DeliveryTraceConfig{Schedule: []int64{0, 1}}, sub("dt")).(*deliveryTrace)
	if cell.mtu != defaultMTU {
		t.Fatalf("default MTU = %d, want %d", cell.mtu, defaultMTU)
	}
}

func TestDeliveryTraceUnsortedScheduleSorted(t *testing.T) {
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule: []int64{20, 0, 10},
		MTU:      1500,
	}, sub("dt"))
	wantMs := []int64{0, 10, 20}
	for i, w := range wantMs {
		out := cell.Process(mkPkt(uint64(i+1), 0, 100))
		if out[0].DeliverAt != w*ms {
			t.Fatalf("pkt %d: got %d ms want %d", i, out[0].DeliverAt/ms, w)
		}
	}
}

func TestDeliveryTraceDeterministic(t *testing.T) {
	run := func() []int64 {
		cell := NewDeliveryTrace(DeliveryTraceConfig{
			Schedule: []int64{0, 7, 13, 13, 40}, MTU: 1500, MaxBacklog: 9000,
		}, sub("dt"))
		var got []int64
		for i := 0; i < 6; i++ {
			out := cell.Process(mkPkt(uint64(i+1), 0, 1500))
			if len(out) == 1 {
				got = append(got, out[0].DeliverAt)
			} else {
				got = append(got, -1)
			}
		}
		return got
	}
	if !reflect.DeepEqual(run(), run()) {
		t.Fatal("DeliveryTrace non-deterministic")
	}
}

func TestDeliveryTraceNeverDeliverBeforeRecvInvariant(t *testing.T) {
	cell := NewDeliveryTrace(DeliveryTraceConfig{
		Schedule: []int64{0, 10, 20, 30, 40, 50}, MTU: 1500, Loop: true,
	}, sub("dt"))
	for i := 0; i < 20; i++ {
		in := mkPkt(uint64(i+1), int64(i), 1500)
		out := cell.Process(in)
		if len(out) == 1 && out[0].DeliverAt < in.RecvAt {
			t.Fatalf("pkt %d delivers before recv: %d < %d", i, out[0].DeliverAt, in.RecvAt)
		}
	}
}

// ---------------------------------------------------------------------------
// Trace format round-trip.
// ---------------------------------------------------------------------------

func TestTraceRoundTrip(t *testing.T) {
	orig := DeliveryTraceConfig{
		Schedule:   []int64{5, 0, 5, 12, 30},
		MTU:        1200,
		MaxBacklog: 65536,
		Loop:       true,
	}
	s := Serialize(orig)
	got, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantSched := []int64{0, 5, 5, 12, 30} // serialized sorted
	if !reflect.DeepEqual(got.Schedule, wantSched) {
		t.Fatalf("schedule round-trip: got %v want %v", got.Schedule, wantSched)
	}
	if got.MTU != orig.MTU {
		t.Fatalf("MTU round-trip: got %d want %d", got.MTU, orig.MTU)
	}
	if got.MaxBacklog != orig.MaxBacklog {
		t.Fatalf("MaxBacklog round-trip: got %d want %d", got.MaxBacklog, orig.MaxBacklog)
	}
	if got.Loop != orig.Loop {
		t.Fatalf("Loop round-trip: got %v want %v", got.Loop, orig.Loop)
	}

	// Re-serialize must be identical (stable).
	if s2 := Serialize(got); s2 != s {
		t.Fatalf("Serialize not stable:\n%q\n%q", s, s2)
	}
}

func TestParsePlainMahimahiTrace(t *testing.T) {
	// No config comment, bare timestamps, blank lines + comments tolerated.
	in := "# my mahimahi trace\n0\n\n3\n3\n9\n"
	cfg, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Schedule, []int64{0, 3, 3, 9}) {
		t.Fatalf("schedule = %v", cfg.Schedule)
	}
	if cfg.MTU != defaultMTU {
		t.Fatalf("default MTU not applied: %d", cfg.MTU)
	}
	if cfg.Loop {
		t.Fatal("Loop should default false")
	}
}

func TestParseRejectsBadAndUnsorted(t *testing.T) {
	if _, err := Parse("0\nfoo\n"); err == nil {
		t.Fatal("expected error on non-integer line")
	}
	if _, err := Parse("0\n10\n5\n"); err == nil {
		t.Fatal("expected error on out-of-order timestamps")
	}
	if _, err := Parse("0\n-1\n"); err == nil {
		t.Fatal("expected error on negative timestamp")
	}
}

func TestSerializeSortsAndComments(t *testing.T) {
	s := Serialize(DeliveryTraceConfig{Schedule: []int64{30, 10, 20}, MTU: 1500, Loop: false})
	cfg, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Schedule, []int64{10, 20, 30}) {
		t.Fatalf("got %v", cfg.Schedule)
	}
}

// Parsed trace must drive a cell consistently with the original config.
func TestTraceParsedConfigDrivesCell(t *testing.T) {
	orig := DeliveryTraceConfig{Schedule: []int64{0, 10, 20}, MTU: 1500, MaxBacklog: 3000}
	cfg, err := Parse(Serialize(orig))
	if err != nil {
		t.Fatal(err)
	}
	cell := NewDeliveryTrace(cfg, sub("dt"))
	// MaxBacklog 3000 / 1500 => 2 admitted of packets arriving together.
	admitted := 0
	for i := 0; i < 3; i++ {
		if len(cell.Process(mkPkt(uint64(i+1), 0, 1500))) == 1 {
			admitted++
		}
	}
	if admitted != 2 {
		t.Fatalf("admitted=%d want 2", admitted)
	}
}
