// Package rng provides the deterministic, additive PRNG substream allocator that
// is the root of Impair's reproducibility. One root seed deterministically
// derives an independent SplitMix64 stream per named impairment source, so two
// runs with the same seed make identical decisions, and adding a new source
// never perturbs the draws of existing ones (the property the whole engine
// leans on; see PLAN.md P0.2).
package rng

// Source is a deterministic SplitMix64 PRNG for one impairment substream.
type Source struct{ state uint64 }

// Uint64 returns the next 64-bit value.
func (s *Source) Uint64() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Float64 returns a uniform value in [0,1) with 53 bits of precision.
func (s *Source) Float64() float64 {
	return float64(s.Uint64()>>11) / (1 << 53)
}

// Intn returns a uniform integer in [0,n) (n>0). Uses rejection-free reduction;
// the tiny modulo bias is irrelevant for impairment decisions.
func (s *Source) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(s.Uint64() % uint64(n))
}

// Root deterministically derives named substreams from a single seed.
type Root struct{ seed uint64 }

// NewRoot returns a Root for the given master seed.
func NewRoot(seed int64) *Root { return &Root{seed: uint64(seed)} }

// Seed returns the master seed.
func (r *Root) Seed() int64 { return int64(r.seed) }

// Sub returns the substream identified by name. The result depends only on
// (seed, name) and never on the order in which Sub is called, so introducing a
// new substream leaves every existing substream's sequence byte-identical.
func (r *Root) Sub(name string) *Source {
	h := fnv64a(name) ^ r.seed
	// One SplitMix64 mixing round so adjacent names diverge immediately.
	h += 0x9E3779B97F4A7C15
	h = (h ^ (h >> 30)) * 0xBF58476D1CE4E5B9
	h = (h ^ (h >> 27)) * 0x94D049BB133111EB
	return &Source{state: h ^ (h >> 31)}
}

func fnv64a(s string) uint64 {
	const offset = 1469598103934665603
	const prime = 1099511628211
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}
