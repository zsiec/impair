package sim

import (
	"testing"

	"github.com/zsiec/impair/engine"
)

// nopCell forwards every packet unchanged — the minimal Cell, used to exercise
// the pipeline and the determinism harness before the real cells land.
type nopCell struct{}

func (nopCell) Name() string { return "nop" }
func (nopCell) Process(in engine.InFlight) []engine.InFlight {
	return []engine.InFlight{in}
}

// dropEvenCell drops packets with an even ingress seq — a trivial deterministic
// impairment to confirm drops are recorded and reproducible.
type dropEvenCell struct{}

func (dropEvenCell) Name() string { return "drop-even" }
func (dropEvenCell) Process(in engine.InFlight) []engine.InFlight {
	if in.Seq%2 == 0 {
		return nil
	}
	return []engine.InFlight{in}
}

func newEngine() *engine.Engine {
	return engine.New(
		[]engine.Cell{nopCell{}, dropEvenCell{}},
		[]engine.Cell{nopCell{}},
	)
}

// The pattern must be byte-identical across runs (the P0 determinism property).
func TestDeterministic(t *testing.T) {
	trace := SyntheticTrace(2000, 1_000_000) // 2000 pkts @ 1ms
	a := Run(newEngine(), trace)
	b := Run(newEngine(), trace)
	if a != b {
		t.Fatal("pattern not deterministic across runs")
	}
	if len(a) == 0 {
		t.Fatal("empty pattern")
	}
}

// Passthrough on S2C must forward everything; drop-even on C2S must drop half.
func TestStats(t *testing.T) {
	trace := SyntheticTrace(4000, 1_000_000)
	s := RunStats(newEngine(), trace)
	if s.Ingress != 4000 {
		t.Fatalf("ingress=%d want 4000", s.Ingress)
	}
	if s.Forwarded+s.Dropped != s.Ingress {
		t.Fatalf("forwarded(%d)+dropped(%d) != ingress(%d)", s.Forwarded, s.Dropped, s.Ingress)
	}
	if s.Dropped == 0 {
		t.Fatal("expected some drops from drop-even on c2s")
	}
}
