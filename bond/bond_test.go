package bond

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/pattern"
	"github.com/zsiec/impair/internal/sim"
	"github.com/zsiec/impair/result"
	"github.com/zsiec/impair/scenario"
)

const (
	bondTotal    = 2000
	bondInterval = 1_000_000 // 1 ms
)

func lossyBurst() scenario.Scenario { return scenario.Examples()["lossy-burst"] }
func clean() scenario.Scenario      { return scenario.Examples()["clean"] }

func mustBuild(t *testing.T, s scenario.Scenario) *engine.Engine {
	t.Helper()
	e, err := scenario.Build(s)
	if err != nil {
		t.Fatalf("build %s: %v", s.Name, err)
	}
	return e
}

func mustBuildLink(t *testing.T, s scenario.Scenario, link int) *engine.Engine {
	t.Helper()
	e, err := scenario.BuildLink(s, link)
	if err != nil {
		t.Fatalf("buildLink %s/%d: %v", s.Name, link, err)
	}
	return e
}

func sameSet(a, b map[uint64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// TestBondMaskingProvable is the SMPTE 2022-7 payoff, made provable and
// bit-deterministic: link A suffers a GE burst while the CLEAN redundant link B
// carries every packet, so the merge has ZERO gap and every one of A's drops is
// masked. This is "no gap when one link bursts," asserted from ground truth.
func TestBondMaskingProvable(t *testing.T) {
	a := mustBuild(t, lossyBurst())
	b := mustBuild(t, clean())
	ledgers := RunLinks([]*engine.Engine{a, b}, bondTotal, bondInterval)

	dropsA, dropsB := ledgers[0].Drops(), ledgers[1].Drops()
	if dropsA == 0 {
		t.Fatal("lossy link A should have dropped some packets under GE, dropped 0")
	}
	if dropsB != 0 {
		t.Fatalf("clean link B must drop nothing, dropped %d", dropsB)
	}

	mr := Merge(ledgers, bondTotal)
	if len(mr.Gaps) != 0 {
		t.Fatalf("clean link B masks all of A's burst -> expected 0 gaps, got %d: %v", len(mr.Gaps), mr.Gaps)
	}
	if mr.Masked != dropsA {
		t.Fatalf("all %d of link A's drops must be masked, masked=%d", dropsA, mr.Masked)
	}
	if mr.Delivered() != bondTotal {
		t.Fatalf("seamless merge must deliver all %d, got %d", bondTotal, mr.Delivered())
	}

	// The oracle must PASS every check (0 gap, redundancy exercised).
	in := Input{Lib: "test", Scenario: "burst-masked", Links: 2, Total: bondTotal,
		Delivered: mr.Delivered(), PerLinkDrops: []int{dropsA, dropsB}, Gaps: len(mr.Gaps)}
	for _, c := range Evaluate(in) {
		if c.Verdict != result.Pass {
			t.Fatalf("check %s = %s: %s", c.Name, c.Verdict, c.Detail)
		}
	}
}

// TestBondIndependence is the P2.1 substream guarantee: two links built from the
// SAME scenario via BuildLink must draw INDEPENDENT loss — if they dropped the
// identical seq set, the per-link substreams were aliased (the prefix bug), and
// bonding would mask nothing.
func TestBondIndependence(t *testing.T) {
	l0 := mustBuildLink(t, lossyBurst(), 0)
	l1 := mustBuildLink(t, lossyBurst(), 1)
	ledgers := RunLinks([]*engine.Engine{l0, l1}, bondTotal, bondInterval)
	if ledgers[0].Drops() == 0 || ledgers[1].Drops() == 0 {
		t.Fatalf("both GE links should drop: %d, %d", ledgers[0].Drops(), ledgers[1].Drops())
	}
	if sameSet(ledgers[0].Dropped, ledgers[1].Dropped) {
		t.Fatal("links dropped the identical seq set — per-link substreams are NOT independent (BuildLink prefix bug)")
	}
}

// TestBondDeterministic: RunLinks over independent links is byte-stable run to run.
func TestBondDeterministic(t *testing.T) {
	build := func() []LinkLedger {
		l0 := mustBuildLink(t, lossyBurst(), 0)
		l1 := mustBuildLink(t, lossyBurst(), 1)
		return RunLinks([]*engine.Engine{l0, l1}, bondTotal, bondInterval)
	}
	a, b := build(), build()
	for i := range a {
		if !sameSet(a[i].Dropped, b[i].Dropped) {
			t.Fatalf("link %d drop set not deterministic across runs", i)
		}
	}
}

// TestBondNegativeControl proves the oracle BITES: a receiver that fails to merge
// a packet a redundant link carried (Delivered below the survivable count, gaps
// known to be 0) must FAIL seamless-coverage. Two cases pin both ways a merge
// failure can present: (a) loss on a path the other masked, and — critically —
// (b) loss with ZERO recorded per-link drops, which an early "no loss -> PASS"
// short-circuit would silently let through.
func TestBondNegativeControl(t *testing.T) {
	seamless := func(in Input) result.Check {
		for _, c := range Evaluate(in) {
			if c.Name == "seamless-coverage" {
				return c
			}
		}
		t.Fatal("no seamless-coverage check")
		return result.Check{}
	}
	cases := []struct {
		name string
		in   Input
	}{
		{"merge dropped a masked packet", Input{Lib: "broken-merge", Scenario: "burst-masked", Links: 2,
			Total: bondTotal, Delivered: bondTotal - 5, PerLinkDrops: []int{40, 0}, Gaps: 0}},
		{"loss with no per-link drop recorded", Input{Lib: "broken-merge", Scenario: "clean", Links: 2,
			Total: bondTotal, Delivered: bondTotal - 5, PerLinkDrops: []int{0, 0}, Gaps: 0}},
	}
	for _, tc := range cases {
		if c := seamless(tc.in); c.Verdict != result.Fail {
			t.Fatalf("%s: seamless-coverage should FAIL (a carried packet was lost), got %s: %s", tc.name, c.Verdict, c.Detail)
		}
	}
}

// TestBondGolden is the multi-link pattern artifact (CI-gated): two INDEPENDENT
// links of the lossy-burst scenario, recorded per link. The committed golden
// pins both the determinism and the per-link independence (the two sections
// differ). Set UPDATE_GOLDEN=1 to regenerate after an intentional change.
func TestBondGolden(t *testing.T) {
	sc := lossyBurst()
	trace := sim.SyntheticTrace(bondTotal, bondInterval)
	var b strings.Builder
	for link := 0; link < 2; link++ {
		eng := mustBuildLink(t, sc, link)
		rec := pattern.NewRecorder(sc.Seed)
		sim.RunActions(eng, trace, rec.Add)
		fmt.Fprintf(&b, "# link %d\n", link)
		b.WriteString(rec.String())
	}
	got := b.String()

	path := filepath.Join("testdata", "golden_bond.pattern")
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
		t.Fatalf("read golden (run `UPDATE_GOLDEN=1 go test ./bond/`): %v", err)
	}
	if got != string(want) {
		t.Fatalf("bond pattern drifted from golden %s — determinism regression or intentional change (set UPDATE_GOLDEN=1)", path)
	}
}
