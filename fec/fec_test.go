package fec

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/result"
	"github.com/zsiec/impair/scenario"
)

// TestRecover pins the ST 2022-1 erasure-decode math against hand-computed cases
// on a 5-column by 4-row matrix (col = i%5, row = i/5).
func TestRecover(t *testing.T) {
	cases := []struct {
		name     string
		m        Matrix
		lost     []int
		wantRec  []int
		wantResi []int
	}{
		{"single loss per column, 1-D -> all recovered",
			Matrix{L: 5, D: 4}, []int{0, 1, 2}, []int{0, 1, 2}, nil},
		{"two losses in one column, 1-D -> neither recovered",
			Matrix{L: 5, D: 4}, []int{0, 5}, nil, []int{0, 5}},
		{"two losses in one column, 2-D -> recovered by row+cascade",
			Matrix{L: 5, D: 4, TwoD: true}, []int{0, 5}, []int{0, 5}, nil},
		{"2x2 loss square, 2-D -> unrecoverable",
			Matrix{L: 5, D: 4, TwoD: true}, []int{0, 1, 5, 6}, nil, []int{0, 1, 5, 6}},
		{"burst of L (one row), 1-D -> column interleave recovers all",
			Matrix{L: 5, D: 4}, []int{0, 1, 2, 3, 4}, []int{0, 1, 2, 3, 4}, nil},
		{"burst of L+1, 1-D -> the doubled column is residual",
			Matrix{L: 5, D: 4}, []int{0, 1, 2, 3, 4, 5}, []int{1, 2, 3, 4}, []int{0, 5}},
		{"burst of L+1, 2-D -> cascade recovers all",
			Matrix{L: 5, D: 4, TwoD: true}, []int{0, 1, 2, 3, 4, 5}, []int{0, 1, 2, 3, 4, 5}, nil},
	}
	for _, tc := range cases {
		rec, resi := Recover(tc.m, tc.lost)
		if !eqInts(rec, tc.wantRec) {
			t.Errorf("%s: recovered = %v, want %v", tc.name, rec, tc.wantRec)
		}
		if !eqInts(resi, tc.wantResi) {
			t.Errorf("%s: residual = %v, want %v", tc.name, resi, tc.wantResi)
		}
		// Conservation: every loss is either recovered or residual, no double-count.
		if len(rec)+len(resi) != countInBlock(tc.m, tc.lost) {
			t.Errorf("%s: %d recovered + %d residual != %d losses", tc.name, len(rec), len(resi), countInBlock(tc.m, tc.lost))
		}
	}
}

// TestRecoverOrderIndependent: the recovered/residual sets are a pure function of
// (matrix, lost), independent of the order the losses are presented.
func TestRecoverOrderIndependent(t *testing.T) {
	m := Matrix{L: 5, D: 4, TwoD: true}
	a1, b1 := Recover(m, []int{0, 1, 2, 3, 4, 5})
	a2, b2 := Recover(m, []int{5, 3, 0, 4, 1, 2})
	if !eqInts(a1, a2) || !eqInts(b1, b2) {
		t.Fatalf("decode is order-dependent: %v/%v vs %v/%v", a1, b1, a2, b2)
	}
}

// TestOracleGoodRun: an ARQ-isolated FEC run that recovers exactly the recoverable
// set, with an honest self-reported count, passes every check.
func TestOracleGoodRun(t *testing.T) {
	in := Input{Lib: "test", Scenario: "fec-2d", Matrix: Matrix{L: 5, D: 4, TwoD: true},
		Total: 20, Lost: []int{0, 5}, Delivered: 20, ClaimedFEC: 2, ARQIsolated: true}
	for _, c := range Evaluate(in) {
		if c.Verdict != result.Pass {
			t.Fatalf("check %s = %s: %s", c.Name, c.Verdict, c.Detail)
		}
	}
}

// TestOracleNegativeControls proves the oracle BITES on the two unambiguous
// soundness violations: an over-reported FEC count, and (ARQ isolated) delivery
// above what the matrix can recover.
func TestOracleNegativeControls(t *testing.T) {
	sound := func(in Input) result.Check {
		for _, c := range Evaluate(in) {
			if c.Name == "fec-recovery-sound" {
				return c
			}
		}
		t.Fatal("no fec-recovery-sound check")
		return result.Check{}
	}
	// Over-claim: 0/5 are both recoverable (2-D) so recoverable=2; claiming 3 is impossible.
	overClaim := Input{Lib: "liar", Matrix: Matrix{L: 5, D: 4, TwoD: true},
		Total: 20, Lost: []int{0, 5}, Delivered: 20, ClaimedFEC: 3, ARQIsolated: true}
	if c := sound(overClaim); c.Verdict != result.Fail {
		t.Fatalf("over-claim should FAIL fec-recovery-sound, got %s: %s", c.Verdict, c.Detail)
	}
	// Impossible delivery: a 2x2 square is fully unrecoverable (residual 4), so a
	// FEC-only receiver can deliver at most 16/20; claiming 20 means ARQ leaked or
	// the decode is non-conformant.
	impossible := Input{Lib: "leaky", Matrix: Matrix{L: 5, D: 4, TwoD: true},
		Total: 20, Lost: []int{0, 1, 5, 6}, Delivered: 20, ClaimedFEC: -1, ARQIsolated: true}
	if c := sound(impossible); c.Verdict != result.Fail {
		t.Fatalf("impossible delivery should FAIL fec-recovery-sound, got %s: %s", c.Verdict, c.Detail)
	}
}

// TestOracleUnderRecoveryWarns: a shortfall below the FEC ceiling is a WARN
// (could be a late/lost FEC packet), not a hard FAIL.
func TestOracleUnderRecoveryWarns(t *testing.T) {
	in := Input{Lib: "weak", Matrix: Matrix{L: 5, D: 4, TwoD: true},
		Total: 20, Lost: []int{0, 5}, Delivered: 19, ClaimedFEC: 1, ARQIsolated: true}
	var eff result.Check
	for _, c := range Evaluate(in) {
		if c.Name == "fec-recovery-effective" {
			eff = c
		}
	}
	if eff.Verdict != result.Warn {
		t.Fatalf("under-recovery should WARN, got %s: %s", eff.Verdict, eff.Detail)
	}
}

// TestFECFromDroplist ties the oracle to the REALIZED droplist: a droplist cell
// drops ingress packets, and the FEC model computes recoverability from exactly
// those losses (media index = ingress Seq - 1).
func TestFECFromDroplist(t *testing.T) {
	sc := scenario.Scenario{Name: "fec-droplist", Seed: 1,
		Pipeline: []scenario.Stage{{DropList: &scenario.DropListParams{Seqs: []uint64{1, 6, 7}}}}}
	eng, err := scenario.Build(sc)
	if err != nil {
		t.Fatal(err)
	}
	lost := realizedDrops(eng, 20) // ingress seqs {1,6,7} -> media indices {0,5,6}
	if !eqInts(lost, []int{0, 5, 6}) {
		t.Fatalf("realized droplist = %v, want [0 5 6]", lost)
	}
	// 1-D: col0={0,5} doubled (residual), col1={6} alone (recovered).
	rec, resi := Recover(Matrix{L: 5, D: 4}, lost)
	if !eqInts(rec, []int{6}) || !eqInts(resi, []int{0, 5}) {
		t.Fatalf("1-D recover = %v/%v, want [6]/[0 5]", rec, resi)
	}
	// 2-D: row+column cascade recovers all three.
	rec2, resi2 := Recover(Matrix{L: 5, D: 4, TwoD: true}, lost)
	if !eqInts(rec2, []int{0, 5, 6}) || len(resi2) != 0 {
		t.Fatalf("2-D recover = %v/%v, want [0 5 6]/[]", rec2, resi2)
	}
}

// realizedDrops drives n C2S packets through eng and returns the 0-based media
// indices (ingress Seq - 1) the engine dropped.
func realizedDrops(eng *engine.Engine, n int) []int {
	var lost []int
	for k := 0; k < n; k++ {
		for _, a := range eng.Handle(engine.Packet{Data: []byte{byte(k)}, Dir: engine.C2S}, int64(k)) {
			if a.Kind == engine.Drop {
				lost = append(lost, int(a.Seq)-1)
			}
		}
	}
	return lost
}

// TestFECGolden pins the recoverability table for canonical (matrix, droplist)
// cases — a regression in the decode math surfaces as a golden diff. Set
// UPDATE_GOLDEN=1 to regenerate after an intentional change.
func TestFECGolden(t *testing.T) {
	cases := []struct {
		m    Matrix
		lost []int
	}{
		{Matrix{L: 5, D: 4}, []int{0, 1, 2}},
		{Matrix{L: 5, D: 4}, []int{0, 5}},
		{Matrix{L: 5, D: 4, TwoD: true}, []int{0, 5}},
		{Matrix{L: 5, D: 4, TwoD: true}, []int{0, 1, 5, 6}},
		{Matrix{L: 5, D: 4}, []int{0, 1, 2, 3, 4}},
		{Matrix{L: 5, D: 4}, []int{0, 1, 2, 3, 4, 5}},
		{Matrix{L: 5, D: 4, TwoD: true}, []int{0, 1, 2, 3, 4, 5}},
		{Matrix{L: 10, D: 5}, []int{3, 7, 22}},
	}
	var b strings.Builder
	b.WriteString("fec-golden v1\n")
	for _, c := range cases {
		rec, resi := Recover(c.m, c.lost)
		fmt.Fprintf(&b, "L=%d D=%d %s lost=%v -> recovered=%v residual=%v\n",
			c.m.L, c.m.D, dim(c.m.TwoD), c.lost, rec, resi)
	}
	got := b.String()

	path := filepath.Join("testdata", "golden_fec.txt")
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
		t.Fatalf("read golden (run `UPDATE_GOLDEN=1 go test ./fec/`): %v", err)
	}
	if got != string(want) {
		t.Fatalf("FEC recoverability drifted from golden %s — decode regression or intentional change (set UPDATE_GOLDEN=1)\n--- got ---\n%s", path, got)
	}
}

func eqInts(a, b []int) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
