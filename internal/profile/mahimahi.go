package profile

import (
	"fmt"
	"io"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/droplist"
	"github.com/zsiec/impair/internal/rng"
)

// mahimahi.go is the Mahimahi delivery-opportunity TRACE importer. A Mahimahi
// trace is a file of millisecond timestamps, each granting one MTU of egress
// capacity ("one packet may be delivered now"); it is the cross-tool-portable
// way to express a cellular link's time-varying capacity. We adopt the format
// verbatim and reuse the engine's tested parser/serializer (internal/droplist).
//
// Unlike a Profile (a parametric loss/delay/jitter ladder), a trace drives the
// DeliveryTrace cell — a token-bucket / delivery schedule — directly. A
// TraceProfile pairs that schedule with the same MANDATORY provenance every
// profile carries, so an imported trace is just as citable.

// TraceProfile is a named, cited Mahimahi delivery-opportunity trace. It is the
// trace analogue of Profile: it carries provenance and compiles to a
// scenario.Scenario whose single stage is a DeliveryTrace (token-bucket) cell.
type TraceProfile struct {
	// Name is the stable identifier (e.g. "mahimahi-verizon-lte").
	Name string `json:"name"`
	// Description is a one-line human summary.
	Description string `json:"description"`

	// Cite/Source/License are the mandatory provenance triple (see Profile).
	Cite    string `json:"cite"`
	Source  Source `json:"source"`
	License string `json:"license"`
	Notes   string `json:"notes,omitempty"`

	// Trace is the parsed delivery schedule + token-bucket parameters.
	Trace droplist.DeliveryTraceConfig `json:"trace"`
}

// ValidateProvenance mirrors Profile.ValidateProvenance for trace profiles so
// the provenance lint can treat both uniformly.
func (t TraceProfile) ValidateProvenance() error {
	if t.Cite == "" {
		return fmt.Errorf("trace %q: missing Cite", t.Name)
	}
	if t.Source == "" {
		return fmt.Errorf("trace %q: missing Source", t.Name)
	}
	if !ValidSource(t.Source) {
		return fmt.Errorf("trace %q: invalid Source %q", t.Name, t.Source)
	}
	if t.License == "" {
		return fmt.Errorf("trace %q: missing License", t.Name)
	}
	return nil
}

// Provenance returns the trace profile's citation triple.
func (t TraceProfile) Provenance() Provenance {
	return Provenance{Cite: t.Cite, Source: t.Source, License: t.License}
}

// Cell builds the DeliveryTrace (token-bucket / delivery-opportunity gate) cell
// for this trace, pacing egress to the trace's opportunities exactly as the
// engine's bandwidth cell expects. seed derives the cell's substream for API
// symmetry with the other cells; the DeliveryTrace cell is purely data-driven
// (no random draws), so the schedule fully determines its behavior. The scenario
// layer does not (yet) expose a DeliveryTrace stage, so a trace is wired into an
// engine via this builder rather than scenario.Build.
func (t TraceProfile) Cell(seed int64) engine.Cell {
	src := rng.NewRoot(seed).Sub("deliverytrace/" + t.Name)
	return droplist.NewDeliveryTrace(t.Trace, src)
}

// Schedule returns the parsed delivery-opportunity timestamps (ms), the
// token-bucket schedule the engine consumes.
func (t TraceProfile) Schedule() []int64 {
	out := make([]int64, len(t.Trace.Schedule))
	copy(out, t.Trace.Schedule)
	return out
}

// ImportMahimahi parses a Mahimahi delivery-opportunity trace from r and wraps
// it in a cited TraceProfile. The trace body is the standard one-timestamp-per
// -line format (comments with '#', optional "# key=value" config line);
// provenance (name/cite/source/license) is supplied by the caller because it is
// metadata ABOUT the file, not contained in the Mahimahi format itself.
//
// The returned TraceProfile validates against the provenance lint.
func ImportMahimahi(r io.Reader, meta TraceMeta) (TraceProfile, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return TraceProfile{}, fmt.Errorf("profile: read mahimahi trace: %w", err)
	}
	cfg, err := droplist.Parse(string(raw))
	if err != nil {
		return TraceProfile{}, fmt.Errorf("profile: parse mahimahi trace %q: %w", meta.Name, err)
	}
	tp := TraceProfile{
		Name:        meta.Name,
		Description: meta.Description,
		Cite:        meta.Cite,
		Source:      meta.Source,
		License:     meta.License,
		Notes:       meta.Notes,
		Trace:       cfg,
	}
	if err := tp.ValidateProvenance(); err != nil {
		return TraceProfile{}, err
	}
	return tp, nil
}

// TraceMeta is the provenance/metadata a caller attaches to an imported trace.
// The Mahimahi format carries no provenance of its own, so it must be supplied
// out-of-band (typically from traces/MANIFEST.json).
type TraceMeta struct {
	Name        string
	Description string
	Cite        string
	Source      Source
	License     string
	Notes       string
}

// Emit serializes the trace profile's schedule back to the Mahimahi on-disk
// format (the inverse of ImportMahimahi's body parse). Round-tripping a trace
// through ImportMahimahi -> Emit -> Parse yields an equal schedule, which the
// round-trip test pins.
func (t TraceProfile) Emit() string {
	return droplist.Serialize(t.Trace)
}
