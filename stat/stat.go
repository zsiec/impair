// Package stat provides the small statistical primitives Transit-WPT needs to
// turn the noisy results of real (Tier-2) implementations into bounded verdicts:
// per the framing, real binaries on real sockets give distribution-reproducible
// results, so a metric is reported as a mean with a bootstrap confidence
// interval rather than a single number, and conformance is judged on the
// interval, not a point.
package stat

import (
	"math"
	"math/rand"
	"sort"
)

// CI is a mean with a two-sided bootstrap confidence interval.
type CI struct {
	Mean float64 `json:"mean"`
	Lo   float64 `json:"lo"`
	Hi   float64 `json:"hi"`
	N    int     `json:"n"`
}

// BootstrapMeanCI returns the sample mean and a (1-2*alpha) percentile bootstrap
// CI of the mean over `iters` resamples, using a seed so the result is
// reproducible. alpha is the one-sided tail (e.g. 0.025 for a 95% interval).
// With fewer than 2 samples the interval collapses to the mean.
func BootstrapMeanCI(samples []float64, alpha float64, iters int, seed int64) CI {
	n := len(samples)
	if n == 0 {
		return CI{}
	}
	m := mean(samples)
	if n < 2 || iters < 2 {
		return CI{Mean: m, Lo: m, Hi: m, N: n}
	}
	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, iters)
	for i := range means {
		var sum float64
		for j := 0; j < n; j++ {
			sum += samples[rng.Intn(n)]
		}
		means[i] = sum / float64(n)
	}
	sort.Float64s(means)
	return CI{Mean: m, Lo: percentile(means, alpha), Hi: percentile(means, 1-alpha), N: n}
}

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// percentile returns the q-quantile (0..1) of a sorted slice via nearest-rank.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(q * float64(len(sorted)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Overlap reports whether two CIs overlap (used as the conformance rule: a
// candidate whose interval does not overlap the reference band differs
// significantly).
func Overlap(a, b CI) bool {
	return a.Lo <= b.Hi && b.Lo <= a.Hi
}
