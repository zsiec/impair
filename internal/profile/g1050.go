package profile

import (
	"fmt"
	"sort"

	"github.com/zsiec/impair/scenario"
)

// g1050.go is the G.1050 / TIA-921 parametric importer: it maps the standard's
// graded "service level" ladder onto concrete, cited Profile presets, and turns
// a named level + seed into a runnable scenario.Scenario.
//
// PROVENANCE / DISCLAIMER. ITU-T Rec. G.1050 and TIA-921 define a five-segment
// IP path, a graded ladder of network service-level behaviours (lightly loaded
// .. heavily congested), and a large catalogue of link configurations; the
// realized impairments are packet delay, delay variation, loss and reordering.
// The specification text is paywalled, so the numbers below are NOT transcribed
// from the standard — they are engineering approximations that reproduce the
// STRUCTURE (a graded loss/delay/jitter/reorder ladder) and are sourced from the
// model parameters only (no copyrighted data redistributed). See package doc.
//
// References (structure only):
//   - https://www.itu.int/rec/T-REC-G.1050
//   - https://www.soft-switch.org/spandsp-doc/g1050_ip_network_model_page.html

// g1050Cite is the shared citation for the built-in G.1050/TIA-921 ladder.
const (
	g1050Cite    = "ITU-T G.1050 (2016) / TIA-921 IP network model — service-level structure (parametric approximation; not transcribed)"
	g1050License = "model-parameters only (no standard text or measured data redistributed); see package doc"
)

// g1050Level is one rung of the G.1050/TIA-921-inspired service-level ladder.
// Levels are identified by a single letter (A..E), A being pristine.
type g1050Level struct {
	letter      string
	grade       int
	description string
	source      Source

	lossModel    LossModel
	lossPct      float64
	burstR       float64
	baseDelayMs  float64
	jitterMs     float64
	delayDist    string
	sigmaMs      float64
	delayCorr    float64
	reorderPct   float64
	reorderGapMs float64
}

// g1050Ladder is the ordered preset table. A is pristine; severity increases
// down the ladder. This is the single source of truth that both Profiles() and
// ImportG1050 read from, so the built-in library and the importer can never
// drift apart. The first two rungs are presented as the well-provisioned LAN /
// broadband cases (Bernoulli loss); the congested rungs use Gilbert-Elliott
// burst loss, matching the standard's emphasis on clustered congestion loss.
var g1050Ladder = []g1050Level{
	{
		letter: "A", grade: 0, source: SourceG1050,
		description: "pristine backbone / managed LAN (excellent)",
		lossModel:   LossBernoulli, lossPct: 0,
		baseDelayMs: 1,
	},
	{
		letter: "B", grade: 1, source: SourceG1050,
		description: "well-provisioned broadband (good)",
		lossModel:   LossBernoulli, lossPct: 0.1,
		baseDelayMs: 20, jitterMs: 3, delayDist: "uniform",
	},
	{
		letter: "C", grade: 2, source: SourceTIA921,
		description: "mobile / LTE-ish access (fair)",
		lossModel:   LossGE, lossPct: 1.0, burstR: 0.5,
		baseDelayMs: 45, sigmaMs: 12, delayDist: "normal", delayCorr: 0.3,
		reorderPct: 1.0, reorderGapMs: 5,
	},
	{
		letter: "D", grade: 3, source: SourceTIA921,
		description: "heavily congested access (poor)",
		lossModel:   LossGE, lossPct: 3.0, burstR: 0.35,
		baseDelayMs: 90, sigmaMs: 30, delayDist: "normal", delayCorr: 0.4,
		reorderPct: 3.0, reorderGapMs: 10,
	},
	{
		letter: "E", grade: 4, source: SourceTIA921,
		description: "stressed / lossy edge (severe)",
		lossModel:   LossGE, lossPct: 8.0, burstR: 0.25,
		baseDelayMs: 150, sigmaMs: 60, delayDist: "normal", delayCorr: 0.5,
		reorderPct: 6.0, reorderGapMs: 20,
	},
}

// profile builds the cited Profile for this rung. The name is "g1050-<letter>".
func (l g1050Level) profile() Profile {
	return Profile{
		Name:             "g1050-" + l.letter,
		Description:      l.description,
		Grade:            l.grade,
		Cite:             fmt.Sprintf("%s — service level %s", g1050Cite, l.letter),
		Source:           l.source,
		License:          g1050License,
		Notes:            "G.1050/TIA-921-inspired parametric preset (structure only)",
		LossModel:        l.lossModel,
		LossPct:          l.lossPct,
		BurstR:           l.burstR,
		BaseDelayMs:      l.baseDelayMs,
		JitterMs:         l.jitterMs,
		DelayDist:        l.delayDist,
		SigmaMs:          l.sigmaMs,
		DelayCorrelation: l.delayCorr,
		ReorderPct:       l.reorderPct,
		ReorderGapMs:     l.reorderGapMs,
	}
}

// G1050Profiles returns the full built-in G.1050/TIA-921 ladder keyed by name.
// Every returned profile carries mandatory provenance.
func G1050Profiles() map[string]Profile {
	out := make(map[string]Profile, len(g1050Ladder))
	for _, l := range g1050Ladder {
		p := l.profile()
		out[p.Name] = p
	}
	return out
}

// G1050Levels returns the available service-level letters in ladder order
// (pristine -> severe), e.g. ["A","B","C","D","E"].
func G1050Levels() []string {
	out := make([]string, 0, len(g1050Ladder))
	for _, l := range g1050Ladder {
		out = append(out, l.letter)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ImportG1050 returns the cited Profile for a G.1050/TIA-921 service level. The
// level may be given as a bare letter ("C") or the full profile name
// ("g1050-C"); it is case-insensitive. The returned profile validates against
// the provenance lint. This is the parametric importer entry point: a named,
// cited preset that ImportG1050(level).Compile(seed) turns into a runnable,
// reproducible scenario.
func ImportG1050(level string) (Profile, error) {
	letter := normalizeLevel(level)
	for _, l := range g1050Ladder {
		if l.letter == letter {
			return l.profile(), nil
		}
	}
	return Profile{}, fmt.Errorf("profile: unknown G.1050 service level %q (have %v)", level, G1050Levels())
}

// ScenarioG1050 imports the named G.1050 level and compiles it with seed.
func ScenarioG1050(level string, seed int64) (scenario.Scenario, error) {
	p, err := ImportG1050(level)
	if err != nil {
		return scenario.Scenario{}, err
	}
	return p.Compile(seed), nil
}

// normalizeLevel turns "g1050-c", "C", "c" all into "C".
func normalizeLevel(level string) string {
	s := level
	// strip an optional "g1050-" prefix (case-insensitive).
	const pfx = "g1050-"
	if len(s) >= len(pfx) && equalFoldASCII(s[:len(pfx)], pfx) {
		s = s[len(pfx):]
	}
	return upperASCII(s)
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if toUpperByte(a[i]) != toUpperByte(b[i]) {
			return false
		}
	}
	return true
}

func upperASCII(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = toUpperByte(b[i])
	}
	return string(b)
}

func toUpperByte(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}
