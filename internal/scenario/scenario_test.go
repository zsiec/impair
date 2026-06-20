package scenario

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/zsiec/impair/internal/sim"
)

// Every example scenario must build, run, and produce a non-trivial pattern.
func TestExamplesBuild(t *testing.T) {
	trace := sim.SyntheticTrace(3000, 1_000_000)
	for name, sc := range Examples() {
		eng, err := Build(sc)
		if err != nil {
			t.Fatalf("%s: build: %v", name, err)
		}
		if got := sim.Run(eng, trace); len(got) == 0 {
			t.Fatalf("%s: empty pattern", name)
		}
	}
}

// The headline P0 property: a built scenario produces a byte-identical pattern
// across runs and (via the committed golden) across machines/time.
func TestDeterministicAcrossRuns(t *testing.T) {
	trace := sim.SyntheticTrace(3000, 1_000_000)
	for name, sc := range Examples() {
		e1, _ := Build(sc)
		e2, _ := Build(sc)
		if sim.Run(e1, trace) != sim.Run(e2, trace) {
			t.Fatalf("%s: pattern not deterministic across builds", name)
		}
	}
}

// Golden gate (make schedule-golden): the realized schedule of the
// "lte-congested" profile must match a committed artifact byte-for-byte. Set
// UPDATE_GOLDEN=1 to regenerate after an intentional change.
func TestScheduleGolden(t *testing.T) {
	sc := Examples()["lte-congested"]
	eng, err := Build(sc)
	if err != nil {
		t.Fatal(err)
	}
	got := sim.Run(eng, sim.SyntheticTrace(2000, 1_000_000))

	path := filepath.Join("testdata", "golden_lte-congested.pattern")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run `UPDATE_GOLDEN=1 go test ./internal/scenario/`): %v", err)
	}
	if got != string(want) {
		t.Fatalf("pattern drifted from golden %s — determinism regression or intentional change (set UPDATE_GOLDEN=1)", path)
	}
}

// Scenario must round-trip through JSON unchanged (the UI/CLI persistence path).
func TestJSONRoundTrip(t *testing.T) {
	for name, sc := range Examples() {
		var buf bytes.Buffer
		if err := Save(&buf, sc); err != nil {
			t.Fatalf("%s: save: %v", name, err)
		}
		got, err := Load(&buf)
		if err != nil {
			t.Fatalf("%s: load: %v", name, err)
		}
		e1, _ := Build(sc)
		e2, err := Build(got)
		if err != nil {
			t.Fatalf("%s: rebuild: %v", name, err)
		}
		trace := sim.SyntheticTrace(1000, 1_000_000)
		if sim.Run(e1, trace) != sim.Run(e2, trace) {
			t.Fatalf("%s: JSON round-trip changed behavior", name)
		}
	}
}

// Over-/under-specified stages must be rejected.
func TestStageValidation(t *testing.T) {
	if _, err := Build(Scenario{Pipeline: []Stage{{}}}); err == nil {
		t.Fatal("empty stage should error")
	}
	if _, err := Build(Scenario{Pipeline: []Stage{{Loss: &LossParams{P: 0.1}, Corrupt: &CorruptParams{Pct: 0.1}}}}); err == nil {
		t.Fatal("over-specified stage should error")
	}
	if _, err := Build(Scenario{Pipeline: []Stage{{Loss: &LossParams{P: 0.1}}}, C2S: []Stage{{Loss: &LossParams{P: 0.1}}}}); err == nil {
		t.Fatal("Pipeline + C2S should error")
	}
}
