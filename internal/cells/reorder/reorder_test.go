package reorder

import (
	"testing"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/rng"
)

func src(name string) *rng.Source {
	return rng.NewRoot(0xDEADBEEF).Sub(name)
}

func mkIn(seq uint64, recvAt int64) engine.InFlight {
	return engine.InFlight{
		Seq:       seq,
		Dir:       engine.C2S,
		Data:      []byte{0xAA, 0xBB, 0xCC, 0xDD},
		RecvAt:    recvAt,
		DeliverAt: recvAt,
	}
}

// TestZeroValueNoOp: the zero Config delivers every packet unchanged, once, with
// a fresh copy of Data.
func TestZeroValueNoOp(t *testing.T) {
	c := New(Config{}, src("noop"))
	in := mkIn(1, 1000)
	out := c.Process(in)
	if len(out) != 1 {
		t.Fatalf("zero config: want 1 output, got %d", len(out))
	}
	if out[0].DeliverAt != in.RecvAt {
		t.Fatalf("zero config: DeliverAt moved: got %d want %d", out[0].DeliverAt, in.RecvAt)
	}
	if &out[0].Data[0] == &in.Data[0] {
		t.Fatal("zero config: output aliases caller's Data")
	}
}

func TestName(t *testing.T) {
	if got := New(Config{}, src("n")).Name(); got != "reorder" {
		t.Fatalf("Name() = %q", got)
	}
}

// TestReorderFraction: with Gap>0, a non-reordered packet has DeliverAt pushed
// back by Gap; a reordered one keeps DeliverAt==RecvAt. Count which is which.
func TestReorderFraction(t *testing.T) {
	const gap = 5_000_000
	const n = 200_000
	cfg := Config{ReorderPct: 0.30, Gap: gap}
	c := New(cfg, src("frac"))

	reordered := 0
	for i := 0; i < n; i++ {
		recv := int64(i) * 1_000_000
		out := c.Process(mkIn(uint64(i+1), recv))
		if len(out) < 1 {
			t.Fatalf("packet %d dropped", i)
		}
		d := out[0]
		switch d.DeliverAt {
		case recv:
			reordered++
		case recv + gap:
			// not reordered
		default:
			t.Fatalf("packet %d unexpected DeliverAt: got %d (recv %d gap %d)", i, d.DeliverAt, recv, gap)
		}
		if d.DeliverAt < d.RecvAt {
			t.Fatalf("packet %d DeliverAt %d < RecvAt %d", i, d.DeliverAt, d.RecvAt)
		}
	}
	frac := float64(reordered) / float64(n)
	if frac < 0.27 || frac > 0.33 {
		t.Fatalf("reorder fraction = %.4f, want ~0.30", frac)
	}
}

// TestDupRate: fraction of packets that produce two outputs ~= DupPct.
func TestDupRate(t *testing.T) {
	const n = 200_000
	cfg := Config{DupPct: 0.10}
	c := New(cfg, src("dup"))

	dups := 0
	for i := 0; i < n; i++ {
		out := c.Process(mkIn(uint64(i+1), int64(i)*1000))
		switch len(out) {
		case 1:
		case 2:
			dups++
		default:
			t.Fatalf("packet %d produced %d outputs", i, len(out))
		}
	}
	rate := float64(dups) / float64(n)
	if rate < 0.085 || rate > 0.115 {
		t.Fatalf("dup rate = %.4f, want ~0.10", rate)
	}
}

// TestDuplicateIsIndependentCopy: the two emitted packets carry distinct Data
// backing arrays; mutating one must not affect the other or the caller.
func TestDuplicateIsIndependentCopy(t *testing.T) {
	// DupPct=1 so every packet is duplicated.
	c := New(Config{DupPct: 1.0}, src("dupcopy"))
	in := mkIn(7, 2000)
	orig := append([]byte(nil), in.Data...)

	out := c.Process(in)
	if len(out) != 2 {
		t.Fatalf("DupPct=1: want 2 outputs, got %d", len(out))
	}
	a, b := out[0], out[1]

	if len(a.Data) == 0 || len(b.Data) == 0 {
		t.Fatal("empty data")
	}
	if &a.Data[0] == &b.Data[0] {
		t.Fatal("duplicate aliases the other copy")
	}
	if &a.Data[0] == &in.Data[0] || &b.Data[0] == &in.Data[0] {
		t.Fatal("duplicate aliases caller's Data")
	}

	// Mutate a; b and the caller's input must be unchanged.
	a.Data[0] ^= 0xFF
	for i := range b.Data {
		if b.Data[i] != orig[i] {
			t.Fatalf("mutating one copy changed the other at %d", i)
		}
	}
	for i := range in.Data {
		if in.Data[i] != orig[i] {
			t.Fatalf("mutating one copy changed caller's Data at %d", i)
		}
	}
}

// TestNeverEarly: DeliverAt is never set before RecvAt, including negative Gap
// (which must clamp to 0).
func TestNeverEarly(t *testing.T) {
	c := New(Config{ReorderPct: 0.5, Gap: -1_000_000}, src("early"))
	for i := 0; i < 1000; i++ {
		recv := int64(i) * 1000
		out := c.Process(mkIn(uint64(i+1), recv))
		for _, o := range out {
			if o.DeliverAt < o.RecvAt {
				t.Fatalf("DeliverAt %d < RecvAt %d", o.DeliverAt, o.RecvAt)
			}
			if o.DeliverAt != recv { // clamped gap == 0
				t.Fatalf("negative gap not clamped: DeliverAt %d recv %d", o.DeliverAt, recv)
			}
		}
	}
}

// TestDeterminism: same seed + same input => byte-identical output across two
// independent cells.
func TestDeterminism(t *testing.T) {
	c1 := New(Config{ReorderPct: 0.4, Gap: 3_000_000, Correlation: 0.5, DupPct: 0.2}, src("det"))
	c2 := New(Config{ReorderPct: 0.4, Gap: 3_000_000, Correlation: 0.5, DupPct: 0.2}, src("det"))

	for i := 0; i < 5000; i++ {
		recv := int64(i) * 1000
		o1 := c1.Process(mkIn(uint64(i+1), recv))
		o2 := c2.Process(mkIn(uint64(i+1), recv))
		if len(o1) != len(o2) {
			t.Fatalf("packet %d: len %d != %d", i, len(o1), len(o2))
		}
		for j := range o1 {
			if o1[j].DeliverAt != o2[j].DeliverAt {
				t.Fatalf("packet %d out %d: DeliverAt %d != %d", i, j, o1[j].DeliverAt, o2[j].DeliverAt)
			}
			if string(o1[j].Data) != string(o2[j].Data) {
				t.Fatalf("packet %d out %d: Data differs", i, j)
			}
		}
	}
}

// TestCorrelationIncreasesRunLength: positive correlation should make reorder
// decisions stickier, i.e. fewer transitions between reordered/non-reordered
// than the independent case at the same ReorderPct.
func TestCorrelationIncreasesRunLength(t *testing.T) {
	const n = 200_000
	const p = 0.5

	transitions := func(corr float64, name string) int {
		c := New(Config{ReorderPct: p, Gap: 1_000_000, Correlation: corr}, src(name)).(*Reorder)
		prev := false
		first := true
		trans := 0
		for i := 0; i < n; i++ {
			recv := int64(i) * 1000
			out := c.Process(mkIn(uint64(i+1), recv))
			reordered := out[0].DeliverAt == recv
			if !first && reordered != prev {
				trans++
			}
			prev = reordered
			first = false
		}
		return trans
	}

	indep := transitions(0.0, "corr-indep")
	sticky := transitions(0.9, "corr-sticky")

	if sticky >= indep {
		t.Fatalf("correlation did not reduce transitions: indep=%d sticky=%d", indep, sticky)
	}
}

// TestCorrelatedFractionStillMatches: even with correlation, the marginal
// reorder fraction should remain near ReorderPct.
func TestCorrelatedFractionStillMatches(t *testing.T) {
	const n = 300_000
	c := New(Config{ReorderPct: 0.25, Gap: 1_000_000, Correlation: 0.6}, src("corrfrac"))
	reordered := 0
	for i := 0; i < n; i++ {
		recv := int64(i) * 1000
		out := c.Process(mkIn(uint64(i+1), recv))
		if out[0].DeliverAt == recv {
			reordered++
		}
	}
	frac := float64(reordered) / float64(n)
	if frac < 0.21 || frac > 0.29 {
		t.Fatalf("correlated reorder fraction = %.4f, want ~0.25", frac)
	}
}

// TestClampHighProbs: probabilities >1 clamp so every packet is reordered and
// duplicated.
func TestClampHighProbs(t *testing.T) {
	c := New(Config{ReorderPct: 5, Gap: 9, DupPct: 5}, src("clamp"))
	for i := 0; i < 100; i++ {
		recv := int64(i)
		out := c.Process(mkIn(uint64(i+1), recv))
		if len(out) != 2 {
			t.Fatalf("DupPct>1 should always dup: got %d", len(out))
		}
		for _, o := range out {
			if o.DeliverAt != recv {
				t.Fatalf("ReorderPct>1 should always reorder: DeliverAt %d recv %d", o.DeliverAt, recv)
			}
		}
	}
}

// TestNilData: a nil payload survives without panic and stays nil.
func TestNilData(t *testing.T) {
	c := New(Config{DupPct: 1}, src("nil"))
	in := engine.InFlight{Seq: 1, RecvAt: 5, DeliverAt: 5}
	out := c.Process(in)
	if len(out) != 2 {
		t.Fatalf("want 2 outputs, got %d", len(out))
	}
	for _, o := range out {
		if o.Data != nil {
			t.Fatalf("nil data became %v", o.Data)
		}
	}
}

// TestKeysOffDeliverAtUnderUpstreamDelay verifies the TIME-BASE CONVENTION: when
// an upstream cell has delayed the packet (DeliverAt strictly > RecvAt), a
// reordered packet is sent "now" = at its arrival time at this stage
// (in.DeliverAt), NOT teleported back to in.RecvAt, while a non-reordered packet
// is in.DeliverAt + Gap. ReorderPct=1 forces the reordered branch; ReorderPct=0
// forces the non-reordered branch.
func TestKeysOffDeliverAtUnderUpstreamDelay(t *testing.T) {
	const recvAt = 1_000
	const upstreamDelay = 7_000_000 // upstream cell pushed DeliverAt this far past RecvAt
	const deliverAt = recvAt + upstreamDelay
	const gap = 5_000_000

	mk := func() engine.InFlight {
		in := mkIn(1, recvAt)
		in.DeliverAt = deliverAt // simulate upstream delay: DeliverAt > RecvAt
		return in
	}

	// Reordered branch: DeliverAt must stay at in.DeliverAt, not snap to RecvAt.
	cReorder := New(Config{ReorderPct: 1.0, Gap: gap}, src("comp-reorder"))
	got := cReorder.Process(mk())
	if len(got) != 1 {
		t.Fatalf("reordered: want 1 output, got %d", len(got))
	}
	if got[0].DeliverAt != deliverAt {
		t.Fatalf("reordered: DeliverAt = %d, want in.DeliverAt %d (must not key off RecvAt %d)",
			got[0].DeliverAt, deliverAt, recvAt)
	}
	if got[0].DeliverAt == recvAt {
		t.Fatal("reordered: DeliverAt teleported back to RecvAt")
	}

	// Non-reordered branch: DeliverAt == in.DeliverAt + Gap (already correct).
	cKeep := New(Config{ReorderPct: 0.0, Gap: gap}, src("comp-keep"))
	got2 := cKeep.Process(mk())
	if len(got2) != 1 {
		t.Fatalf("non-reordered: want 1 output, got %d", len(got2))
	}
	if got2[0].DeliverAt != deliverAt+gap {
		t.Fatalf("non-reordered: DeliverAt = %d, want in.DeliverAt+Gap %d",
			got2[0].DeliverAt, deliverAt+gap)
	}
}
