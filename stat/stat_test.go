package stat

import (
	"math"
	"testing"
)

func TestBootstrapMeanCI(t *testing.T) {
	// Tight cluster around 100 -> narrow CI bracketing the mean.
	s := []float64{100, 100, 99, 101, 100, 100, 98, 102, 100, 100}
	ci := BootstrapMeanCI(s, 0.025, 2000, 1)
	if math.Abs(ci.Mean-100) > 0.5 {
		t.Fatalf("mean=%v want ~100", ci.Mean)
	}
	if !(ci.Lo <= ci.Mean && ci.Mean <= ci.Hi) {
		t.Fatalf("mean %v not within [%v,%v]", ci.Mean, ci.Lo, ci.Hi)
	}
	if ci.Hi-ci.Lo > 5 {
		t.Fatalf("CI too wide for tight data: [%v,%v]", ci.Lo, ci.Hi)
	}
	if ci.N != len(s) {
		t.Fatalf("N=%d", ci.N)
	}
}

func TestReproducible(t *testing.T) {
	s := []float64{1, 2, 3, 4, 5, 6, 7}
	a := BootstrapMeanCI(s, 0.025, 1000, 42)
	b := BootstrapMeanCI(s, 0.025, 1000, 42)
	if a != b {
		t.Fatalf("not reproducible: %+v vs %+v", a, b)
	}
}

func TestDegenerate(t *testing.T) {
	if ci := BootstrapMeanCI(nil, 0.025, 100, 1); ci.N != 0 {
		t.Fatal("empty should give zero CI")
	}
	ci := BootstrapMeanCI([]float64{42}, 0.025, 100, 1)
	if ci.Mean != 42 || ci.Lo != 42 || ci.Hi != 42 {
		t.Fatalf("single sample collapses to point, got %+v", ci)
	}
}

func TestOverlap(t *testing.T) {
	if !Overlap(CI{Lo: 90, Hi: 100}, CI{Lo: 95, Hi: 105}) {
		t.Fatal("should overlap")
	}
	if Overlap(CI{Lo: 90, Hi: 95}, CI{Lo: 96, Hi: 100}) {
		t.Fatal("should not overlap")
	}
}
