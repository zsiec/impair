package corrupt

import (
	"bytes"
	"math/bits"
	"testing"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/rng"
)

func sub(name string) *rng.Source { return rng.NewRoot(1234).Sub(name) }

func mkIn(seq uint64, data []byte) engine.InFlight {
	return engine.InFlight{Seq: seq, Dir: engine.C2S, Data: data, RecvAt: 1000, DeliverAt: 1000}
}

// bitDiff returns the number of differing bits between two equal-length slices.
func bitDiff(a, b []byte) int {
	if len(a) != len(b) {
		return -1
	}
	n := 0
	for i := range a {
		n += bits.OnesCount8(a[i] ^ b[i])
	}
	return n
}

func TestName(t *testing.T) {
	c := New(Config{Pct: 0.5}, sub("corrupt"))
	if c.Name() != "corrupt" {
		t.Fatalf("Name = %q, want %q", c.Name(), "corrupt")
	}
}

// A corrupted packet must differ from the original in exactly one bit, and the
// original input slice must be byte-identical after Process (no aliasing).
func TestExactlyOneBitAndNoAliasing(t *testing.T) {
	c := New(Config{Pct: 1.0}, sub("corrupt")) // always corrupt
	orig := []byte{0x00, 0xFF, 0xA5, 0x3C, 0x11, 0x80, 0x01, 0x7E}

	for i := 0; i < 5000; i++ {
		input := make([]byte, len(orig))
		copy(input, orig)
		snapshot := make([]byte, len(orig))
		copy(snapshot, orig)

		out := c.Process(mkIn(uint64(i), input))
		if len(out) != 1 {
			t.Fatalf("iter %d: got %d outputs, want 1 (keep)", i, len(out))
		}

		// Caller's slice untouched.
		if !bytes.Equal(input, snapshot) {
			t.Fatalf("iter %d: caller slice mutated: %x != %x", i, input, snapshot)
		}

		got := out[0].Data
		// Output must be a distinct backing array.
		if len(got) > 0 && &got[0] == &input[0] {
			t.Fatalf("iter %d: output aliases caller's slice", i)
		}
		if d := bitDiff(snapshot, got); d != 1 {
			t.Fatalf("iter %d: bit diff = %d, want exactly 1 (%x vs %x)", i, d, snapshot, got)
		}

		// Metadata preserved.
		if out[0].Seq != uint64(i) || out[0].RecvAt != 1000 || out[0].DeliverAt != 1000 || out[0].Dir != engine.C2S {
			t.Fatalf("iter %d: metadata changed: %+v", i, out[0])
		}
		if out[0].DeliverAt < out[0].RecvAt {
			t.Fatalf("iter %d: DeliverAt < RecvAt", i)
		}
	}
}

// Corruption rate should approximate Pct.
func TestCorruptionRate(t *testing.T) {
	for _, pct := range []float64{0.0, 0.1, 0.25, 0.5, 0.9, 1.0} {
		c := New(Config{Pct: pct}, sub("rate"))
		orig := []byte{0x12, 0x34, 0x56, 0x78}
		const n = 200000
		corrupted := 0
		for i := 0; i < n; i++ {
			input := make([]byte, len(orig))
			copy(input, orig)
			out := c.Process(mkIn(uint64(i), input))
			if len(out) != 1 {
				t.Fatalf("pct %v: got %d outputs, want 1", pct, len(out))
			}
			if !bytes.Equal(out[0].Data, orig) {
				corrupted++
			}
		}
		rate := float64(corrupted) / float64(n)
		if diff := rate - pct; diff < -0.01 || diff > 0.01 {
			t.Errorf("pct %v: observed corruption rate %.4f, want ~%v", pct, rate, pct)
		}
	}
}

// Every bit position must be reachable (uniform bit selection).
func TestAllBitsReachable(t *testing.T) {
	c := New(Config{Pct: 1.0}, sub("bits"))
	orig := []byte{0x00, 0x00, 0x00, 0x00} // 32 bit positions
	seen := make(map[int]int)
	const n = 100000
	for i := 0; i < n; i++ {
		input := make([]byte, len(orig))
		copy(input, orig)
		out := c.Process(mkIn(uint64(i), input))
		got := out[0].Data
		for byteIdx := range got {
			if got[byteIdx] == 0 {
				continue
			}
			bit := byteIdx*8 + bits.TrailingZeros8(got[byteIdx])
			seen[bit]++
		}
	}
	totalBits := len(orig) * 8
	for b := 0; b < totalBits; b++ {
		if seen[b] == 0 {
			t.Errorf("bit %d never flipped over %d trials", b, n)
		}
	}
}

// Empty payload is forwarded unchanged even at Pct=1 (no bit to flip).
func TestEmptyPayload(t *testing.T) {
	c := New(Config{Pct: 1.0}, sub("empty"))
	out := c.Process(mkIn(7, []byte{}))
	if len(out) != 1 {
		t.Fatalf("got %d outputs, want 1", len(out))
	}
	if len(out[0].Data) != 0 {
		t.Fatalf("empty payload changed: %x", out[0].Data)
	}

	// nil payload too.
	out = c.Process(mkIn(8, nil))
	if len(out) != 1 || len(out[0].Data) != 0 {
		t.Fatalf("nil payload mishandled: %+v", out)
	}
}

// Pct=0 (zero value) never corrupts.
func TestZeroValueNeverCorrupts(t *testing.T) {
	c := New(Config{}, sub("zero"))
	orig := []byte{0xAB, 0xCD}
	for i := 0; i < 10000; i++ {
		input := []byte{0xAB, 0xCD}
		out := c.Process(mkIn(uint64(i), input))
		if !bytes.Equal(out[0].Data, orig) {
			t.Fatalf("iter %d: zero-value config corrupted packet", i)
		}
	}
}

// Pct is clamped to [0,1].
func TestClamp(t *testing.T) {
	orig := []byte{0x01}
	cHi := New(Config{Pct: 5.0}, sub("hi"))
	cLo := New(Config{Pct: -2.0}, sub("lo"))
	hiCorrupt := 0
	loCorrupt := 0
	for i := 0; i < 1000; i++ {
		in1 := []byte{0x01}
		in2 := []byte{0x01}
		if !bytes.Equal(cHi.Process(mkIn(uint64(i), in1))[0].Data, orig) {
			hiCorrupt++
		}
		if !bytes.Equal(cLo.Process(mkIn(uint64(i), in2))[0].Data, orig) {
			loCorrupt++
		}
	}
	if hiCorrupt != 1000 {
		t.Errorf("Pct>1 clamp: corrupted %d/1000, want 1000", hiCorrupt)
	}
	if loCorrupt != 0 {
		t.Errorf("Pct<0 clamp: corrupted %d/1000, want 0", loCorrupt)
	}
}

// Same seed + same input => byte-identical output (determinism).
func TestDeterminism(t *testing.T) {
	orig := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x11}
	run := func() [][]byte {
		c := New(Config{Pct: 0.5}, sub("det"))
		var outs [][]byte
		for i := 0; i < 2000; i++ {
			input := make([]byte, len(orig))
			copy(input, orig)
			res := c.Process(mkIn(uint64(i), input))
			cp := make([]byte, len(res[0].Data))
			copy(cp, res[0].Data)
			outs = append(outs, cp)
		}
		return outs
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("length mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("non-deterministic at %d: %x vs %x", i, a[i], b[i])
		}
	}
}
