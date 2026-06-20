package scenario

// Examples returns a small starter library of named impairment profiles. These
// are illustrative defaults; the curated, provenanced profile library (G.1050,
// LTE/LEO/Wi-Fi traces) lands later (see PLAN.md P0.8 and the impairment-engine
// section). Pipeline order follows the canonical chain: loss -> corrupt ->
// reorder/dup -> ratelimit(queue) -> delay.
func Examples() map[string]Scenario {
	return map[string]Scenario{
		"clean": {
			Name: "clean",
			Seed: 1,
			Pipeline: []Stage{
				{Delay: &DelayParams{BaseMs: 1}},
			},
		},
		"lossy-burst": {
			Name: "lossy-burst",
			Seed: 1,
			Pipeline: []Stage{
				{GE: &GEParams{P: 0.02, R: 0.5}}, // ~3.8% loss, mean burst ~2 pkts
				{Delay: &DelayParams{BaseMs: 30, JitterMs: 5, Distribution: "uniform"}},
			},
		},
		"lte-congested": {
			Name: "lte-congested",
			Seed: 1,
			Pipeline: []Stage{
				{Loss: &LossParams{P: 0.01}},
				{Reorder: &ReorderParams{ReorderPct: 0.03, GapMs: 5, DupPct: 0.005}},
				{RateLimit: &RateLimitParams{RateMbps: 8, QueueBytes: 64000}},
				{Delay: &DelayParams{BaseMs: 40, SigmaMs: 12, Distribution: "normal", Correlation: 0.3}},
			},
		},
		"satellite-geo": {
			Name: "satellite-geo",
			Seed: 1,
			Pipeline: []Stage{
				{GE: &GEParams{P: 0.005, R: 0.4}},
				{Delay: &DelayParams{BaseMs: 600, JitterMs: 10, Distribution: "uniform"}},
			},
		},
	}
}
