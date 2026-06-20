// Package scenario is the declarative wiring layer: it turns a serializable
// Scenario (an ordered pipeline of impairment stages + a seed) into a built
// engine.Engine, allocating each cell its own deterministic rng substream keyed
// by "<kind>/<dir>/<index>". Because rng.Root.Sub is additive, reordering or
// adding stages never perturbs unrelated stages' draws. This is the single
// source of truth a UI, a CLI, or a CUE/YAML file targets.
package scenario

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/cells/corrupt"
	"github.com/zsiec/impair/internal/cells/delay"
	"github.com/zsiec/impair/internal/cells/loss"
	"github.com/zsiec/impair/internal/cells/ratelimit"
	"github.com/zsiec/impair/internal/cells/reorder"
	"github.com/zsiec/impair/internal/droplist"
	"github.com/zsiec/impair/internal/rng"
)

// Scenario is a complete, serializable impairment configuration. Pipeline (if
// set) applies to both directions; otherwise C2S/S2C give per-direction
// pipelines. Each direction always gets its own cell instances and substreams.
type Scenario struct {
	Name     string  `json:"name,omitempty"`
	Seed     int64   `json:"seed"`
	Pipeline []Stage `json:"pipeline,omitempty"`
	C2S      []Stage `json:"c2s,omitempty"`
	S2C      []Stage `json:"s2c,omitempty"`
}

// Stage is one position in a pipeline. Exactly one field must be non-nil; that
// field selects the cell and carries its parameters (delays in milliseconds,
// rate in Mbps — friendlier than the internal ns/Bps units).
type Stage struct {
	Loss      *LossParams      `json:"loss,omitempty"`
	GE        *GEParams        `json:"ge,omitempty"`
	Delay     *DelayParams     `json:"delay,omitempty"`
	Reorder   *ReorderParams   `json:"reorder,omitempty"`
	Corrupt   *CorruptParams   `json:"corrupt,omitempty"`
	RateLimit *RateLimitParams `json:"ratelimit,omitempty"`
	DropList  *DropListParams  `json:"droplist,omitempty"`
}

// LossParams: independent Bernoulli loss.
type LossParams struct {
	P float64 `json:"p"`
}

// GEParams: Gilbert-Elliott 4-state burst loss. H defaults to 1 (lossless good)
// and K to 0 (total loss in bad) when left at zero — matching netem's gemodel.
type GEParams struct {
	P float64 `json:"p"`
	R float64 `json:"r"`
	H float64 `json:"h,omitempty"`
	K float64 `json:"k,omitempty"`
}

// DelayParams: fixed delay + jitter. Distribution is "", "uniform", "normal", or
// "pareto". Times are milliseconds.
type DelayParams struct {
	BaseMs       float64 `json:"baseMs"`
	JitterMs     float64 `json:"jitterMs,omitempty"`
	SigmaMs      float64 `json:"sigmaMs,omitempty"`
	Distribution string  `json:"distribution,omitempty"`
	Correlation  float64 `json:"correlation,omitempty"`
}

// ReorderParams: netem reorder + duplication. GapMs is the base delay reordered
// packets jump ahead of.
type ReorderParams struct {
	ReorderPct  float64 `json:"reorderPct"`
	GapMs       float64 `json:"gapMs,omitempty"`
	Correlation float64 `json:"correlation,omitempty"`
	DupPct      float64 `json:"dupPct,omitempty"`
}

// CorruptParams: single-bit corruption probability.
type CorruptParams struct {
	Pct float64 `json:"pct"`
}

// RateLimitParams: bandwidth shaping with a bounded drop-tail queue.
type RateLimitParams struct {
	RateMbps   float64 `json:"rateMbps"`
	QueueBytes int64   `json:"queueBytes,omitempty"`
}

// DropListParams: drop exactly the listed ingress sequence numbers (1-based).
type DropListParams struct {
	Seqs []uint64 `json:"seqs"`
}

const msToNs = 1_000_000

// Build constructs the Engine for s. It returns an error if a stage is empty,
// over-specified, or carries an unknown distribution.
func Build(s Scenario) (*engine.Engine, error) {
	root := rng.NewRoot(s.Seed)
	c2sStages, s2cStages := s.C2S, s.S2C
	if len(s.Pipeline) > 0 {
		if len(s.C2S) > 0 || len(s.S2C) > 0 {
			return nil, fmt.Errorf("scenario %q: set either Pipeline or C2S/S2C, not both", s.Name)
		}
		c2sStages, s2cStages = s.Pipeline, s.Pipeline
	}
	c2s, err := buildPipeline(root, "c2s", c2sStages)
	if err != nil {
		return nil, fmt.Errorf("c2s: %w", err)
	}
	s2c, err := buildPipeline(root, "s2c", s2cStages)
	if err != nil {
		return nil, fmt.Errorf("s2c: %w", err)
	}
	return engine.New(c2s, s2c), nil
}

func buildPipeline(root *rng.Root, dir string, stages []Stage) ([]engine.Cell, error) {
	cells := make([]engine.Cell, 0, len(stages))
	for i, st := range stages {
		c, err := buildCell(root, dir, i, st)
		if err != nil {
			return nil, fmt.Errorf("stage %d: %w", i, err)
		}
		cells = append(cells, c)
	}
	return cells, nil
}

func buildCell(root *rng.Root, dir string, idx int, st Stage) (engine.Cell, error) {
	sub := func(kind string) *rng.Source {
		return root.Sub(fmt.Sprintf("%s/%s/%d", kind, dir, idx))
	}
	set := 0
	var cell engine.Cell
	var err error
	if st.Loss != nil {
		set++
		cell = loss.New(loss.Config{P: st.Loss.P}, sub("loss"))
	}
	if st.GE != nil {
		set++
		cell = loss.NewGE(loss.GEConfig{P: st.GE.P, R: st.GE.R, H: st.GE.H, K: st.GE.K}, sub("ge"))
	}
	if st.Delay != nil {
		set++
		var dist delay.Distribution
		switch st.Delay.Distribution {
		case "", "none":
			dist = delay.None
		case "uniform":
			dist = delay.Uniform
		case "normal":
			dist = delay.Normal
		case "pareto":
			dist = delay.Pareto
		default:
			return nil, fmt.Errorf("delay: unknown distribution %q", st.Delay.Distribution)
		}
		cell = delay.New(delay.Config{
			Base:         int64(st.Delay.BaseMs * msToNs),
			Jitter:       int64(st.Delay.JitterMs * msToNs),
			Sigma:        int64(st.Delay.SigmaMs * msToNs),
			Distribution: dist,
			Correlation:  st.Delay.Correlation,
		}, sub("delay"))
	}
	if st.Reorder != nil {
		set++
		cell = reorder.New(reorder.Config{
			ReorderPct:  st.Reorder.ReorderPct,
			Gap:         int64(st.Reorder.GapMs * msToNs),
			Correlation: st.Reorder.Correlation,
			DupPct:      st.Reorder.DupPct,
		}, sub("reorder"))
	}
	if st.Corrupt != nil {
		set++
		cell = corrupt.New(corrupt.Config{Pct: st.Corrupt.Pct}, sub("corrupt"))
	}
	if st.RateLimit != nil {
		set++
		cell = ratelimit.New(ratelimit.Config{
			RateBps:    int64(st.RateLimit.RateMbps * 1_000_000 / 8),
			QueueBytes: st.RateLimit.QueueBytes,
		}, sub("ratelimit"))
	}
	if st.DropList != nil {
		set++
		cell = droplist.NewDropList(droplist.DropListConfig{Seqs: st.DropList.Seqs}, sub("droplist"))
	}
	if set == 0 {
		return nil, fmt.Errorf("empty stage (no impairment set)")
	}
	if set > 1 {
		return nil, fmt.Errorf("over-specified stage (%d impairments; exactly one allowed)", set)
	}
	return cell, err
}

// Load parses a Scenario from JSON.
func Load(r io.Reader) (Scenario, error) {
	var s Scenario
	err := json.NewDecoder(r).Decode(&s)
	return s, err
}

// Save writes a Scenario as indented JSON.
func Save(w io.Writer, s Scenario) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
