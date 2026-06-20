package rng

import "testing"

// Same seed + same name must reproduce the same sequence.
func TestReproducible(t *testing.T) {
	a := NewRoot(42).Sub("loss/c2s")
	b := NewRoot(42).Sub("loss/c2s")
	for i := 0; i < 1000; i++ {
		if x, y := a.Uint64(), b.Uint64(); x != y {
			t.Fatalf("draw %d diverged: %d != %d", i, x, y)
		}
	}
}

// Different names (or seeds) must give independent sequences.
func TestIndependent(t *testing.T) {
	a := NewRoot(42).Sub("loss/c2s")
	b := NewRoot(42).Sub("delay/c2s")
	same := 0
	for i := 0; i < 1000; i++ {
		if a.Uint64() == b.Uint64() {
			same++
		}
	}
	if same > 2 {
		t.Fatalf("substreams not independent: %d/1000 collisions", same)
	}
}

// Additivity (the load-bearing property): introducing a new substream must NOT
// change the sequence any existing substream produces.
func TestAdditive(t *testing.T) {
	// Baseline: only allocate "loss".
	r1 := NewRoot(7)
	loss1 := r1.Sub("loss/c2s")
	var base []uint64
	for i := 0; i < 500; i++ {
		base = append(base, loss1.Uint64())
	}

	// Now allocate other substreams first/around it; "loss" must be unchanged.
	r2 := NewRoot(7)
	_ = r2.Sub("delay/c2s")
	_ = r2.Sub("_future_source")
	loss2 := r2.Sub("loss/c2s")
	_ = r2.Sub("jitter/s2c")
	for i := 0; i < 500; i++ {
		if got := loss2.Uint64(); got != base[i] {
			t.Fatalf("additivity violated at draw %d: %d != %d", i, got, base[i])
		}
	}
}

// Float64 must stay in [0,1).
func TestFloatRange(t *testing.T) {
	s := NewRoot(1).Sub("x")
	for i := 0; i < 100000; i++ {
		if f := s.Float64(); f < 0 || f >= 1 {
			t.Fatalf("Float64 out of range: %v", f)
		}
	}
}
