package blackhole

import (
	"testing"

	"github.com/zsiec/impair/engine"
)

func pkt(at int64) engine.InFlight {
	return engine.InFlight{Seq: uint64(at), Dir: engine.C2S, RecvAt: at, DeliverAt: at}
}

func TestBlackholeWindow(t *testing.T) {
	c := New(Config{StartNs: 2000, EndNs: 3000}) // dark in [2000, 3000)
	cases := []struct {
		at   int64
		drop bool
	}{
		{1999, false}, // just before
		{2000, true},  // inclusive start
		{2500, true},  // inside
		{2999, true},  // last dark ns
		{3000, false}, // exclusive end
		{4000, false}, // after
	}
	for _, tc := range cases {
		out := c.Process(pkt(tc.at))
		dropped := len(out) == 0
		if dropped != tc.drop {
			t.Errorf("at=%d: dropped=%v, want %v", tc.at, dropped, tc.drop)
		}
		if !dropped && (len(out) != 1 || out[0].RecvAt != tc.at) {
			t.Errorf("at=%d: surviving packet altered: %+v", tc.at, out)
		}
	}
}

// Deterministic: same input -> same fate, no randomness, and an empty window
// (End <= Start) never drops.
func TestBlackholeDeterministicAndEmptyWindow(t *testing.T) {
	c := New(Config{StartNs: 1000, EndNs: 2000})
	for i := 0; i < 100; i++ {
		if got := len(c.Process(pkt(1500))) == 0; !got {
			t.Fatalf("iter %d: in-window packet should always drop", i)
		}
	}
	open := New(Config{StartNs: 5000, EndNs: 5000}) // empty window
	for _, at := range []int64{0, 5000, 10000} {
		if len(open.Process(pkt(at))) != 1 {
			t.Fatalf("empty window must never drop (at=%d)", at)
		}
	}
}

func TestBlackholeRequiresCleartextFalse(t *testing.T) {
	if New(Config{}).RequiresCleartext() {
		t.Fatal("blackhole is payload-agnostic; RequiresCleartext must be false")
	}
}
