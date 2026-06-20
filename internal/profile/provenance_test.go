package profile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/internal/droplist"
)

// ---------------------------------------------------------------------------
// Provenance lint — the CI gate (PLAN.md: "CI fails if any profile lacks
// cite/source/license").
// ---------------------------------------------------------------------------

// TestAllProfilesHaveProvenance fails if any registered built-in profile is
// missing Cite/Source/License or carries an out-of-vocabulary Source. This is
// the gate that keeps "no anonymous magic numbers" enforced over time.
func TestAllProfilesHaveProvenance(t *testing.T) {
	ps := Profiles()
	if len(ps) == 0 {
		t.Fatal("no profiles registered")
	}
	for name, p := range ps {
		if p.Name != name {
			t.Errorf("profile keyed %q has Name=%q", name, p.Name)
		}
		if err := p.ValidateProvenance(); err != nil {
			t.Errorf("profile %q fails provenance lint: %v", name, err)
		}
	}
}

// TestManifestTracesHaveProvenance applies the same gate to the shipped traces
// manifest: every entry must carry valid provenance, and every pinned file must
// match its recorded SHA-256.
func TestManifestTracesHaveProvenance(t *testing.T) {
	man := loadRepoManifest(t)
	if len(man.Entries) == 0 {
		t.Fatal("traces manifest has no entries")
	}
	if err := man.ValidateProvenance(); err != nil {
		t.Fatalf("manifest provenance lint: %v", err)
	}
	if err := man.Verify(); err != nil {
		t.Fatalf("manifest verify (sha256/license): %v", err)
	}
}

// loadRepoManifest finds traces/MANIFEST.json by walking up from the test's
// working directory (internal/profile) to the repo root.
func loadRepoManifest(t *testing.T) Manifest {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "traces", "MANIFEST.json")
		if _, err := os.Stat(p); err == nil {
			m, err := LoadManifestFile(p)
			if err != nil {
				t.Fatalf("load manifest %s: %v", p, err)
			}
			return m
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate traces/MANIFEST.json from test cwd")
	return Manifest{}
}

// ---------------------------------------------------------------------------
// G.1050 / TIA-921 importer.
// ---------------------------------------------------------------------------

// TestImportG1050Levels imports every level and asserts it validates, names
// canonically, and matches the built-in library entry.
func TestImportG1050Levels(t *testing.T) {
	for _, letter := range G1050Levels() {
		p, err := ImportG1050(letter)
		if err != nil {
			t.Fatalf("ImportG1050(%q): %v", letter, err)
		}
		if p.Name != "g1050-"+letter {
			t.Errorf("ImportG1050(%q): name %q", letter, p.Name)
		}
		if err := p.ValidateProvenance(); err != nil {
			t.Errorf("ImportG1050(%q): provenance: %v", letter, err)
		}
		// The importer and the built-in library must agree.
		got, ok := Get(p.Name)
		if !ok {
			t.Fatalf("built-in library missing %q", p.Name)
		}
		if got != p {
			t.Errorf("importer/library drift for %q", p.Name)
		}
	}
}

// TestImportG1050Aliases accepts bare letters, full names, and any case.
func TestImportG1050Aliases(t *testing.T) {
	for _, in := range []string{"C", "c", "g1050-C", "g1050-c", "G1050-C"} {
		p, err := ImportG1050(in)
		if err != nil {
			t.Fatalf("ImportG1050(%q): %v", in, err)
		}
		if p.Name != "g1050-C" {
			t.Errorf("ImportG1050(%q) => %q, want g1050-C", in, p.Name)
		}
	}
	if _, err := ImportG1050("Z"); err == nil {
		t.Error("expected error for unknown level Z")
	}
}

// TestG1050Golden pins the imported ladder (as canonical JSON) so a change to
// any preset number is a visible, intentional diff. Regenerate with
// UPDATE_GOLDEN=1.
func TestG1050Golden(t *testing.T) {
	type named struct {
		Name    string  `json:"name"`
		Profile Profile `json:"profile"`
	}
	var list []named
	for _, letter := range G1050Levels() {
		p, err := ImportG1050(letter)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, named{Name: p.Name, Profile: p})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(list); err != nil {
		t.Fatal(err)
	}
	got := buf.String()

	path := filepath.Join("testdata", "golden_g1050.json")
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
		t.Fatalf("read golden (run `UPDATE_GOLDEN=1 go test ./internal/profile/`): %v", err)
	}
	if got != string(want) {
		t.Fatalf("g1050 ladder drifted from golden %s (set UPDATE_GOLDEN=1 if intentional)", path)
	}
}

// ---------------------------------------------------------------------------
// Mahimahi trace importer — round trip.
// ---------------------------------------------------------------------------

func sampleTraceMeta() TraceMeta {
	return TraceMeta{
		Name:        "sample",
		Description: "test trace",
		Cite:        "test fixture",
		Source:      SourceSynthetic,
		License:     "CC0-1.0",
	}
}

// TestImportMahimahiRoundTrip imports a Mahimahi trace, re-emits it, and parses
// the re-emitted form: the schedules must be equal (parse -> emit -> parse).
func TestImportMahimahiRoundTrip(t *testing.T) {
	raw := "# my mahimahi trace\n0\n0\n3\n3\n9\n12\n"
	tp, err := ImportMahimahi(strings.NewReader(raw), sampleTraceMeta())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	want := []int64{0, 0, 3, 3, 9, 12}
	if !equalInt64(tp.Schedule(), want) {
		t.Fatalf("parsed schedule = %v, want %v", tp.Schedule(), want)
	}

	emitted := tp.Emit()
	reparsed, err := droplist.Parse(emitted)
	if err != nil {
		t.Fatalf("reparse emitted: %v", err)
	}
	if !equalInt64(reparsed.Schedule, tp.Trace.Schedule) {
		t.Fatalf("round-trip changed schedule: %v -> %v", tp.Trace.Schedule, reparsed.Schedule)
	}
	if reparsed.MTU != tp.Trace.MTU {
		t.Fatalf("round-trip changed MTU: %d -> %d", tp.Trace.MTU, reparsed.MTU)
	}

	// Re-importing the emitted form (with the same meta) must yield an equal
	// TraceProfile — full provenance + schedule round trip.
	tp2, err := ImportMahimahi(strings.NewReader(emitted), sampleTraceMeta())
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if !equalInt64(tp2.Schedule(), tp.Schedule()) || tp2.Cite != tp.Cite || tp2.Source != tp.Source {
		t.Fatalf("reimported trace differs: %+v vs %+v", tp2, tp)
	}
}

// TestImportMahimahiRequiresProvenance rejects a trace with missing provenance.
func TestImportMahimahiRequiresProvenance(t *testing.T) {
	raw := "0\n1\n2\n"
	_, err := ImportMahimahi(strings.NewReader(raw), TraceMeta{Name: "x"}) // no cite/source/license
	if err == nil {
		t.Fatal("expected provenance error for trace without cite/source/license")
	}
}

// TestTraceProfileCellPaces builds the DeliveryTrace cell from an imported trace
// and checks it paces a packet to a delivery opportunity (data-driven, no rng).
func TestTraceProfileCellPaces(t *testing.T) {
	raw := "# mtu=1500 loop=0\n0\n10\n20\n"
	tp, err := ImportMahimahi(strings.NewReader(raw), sampleTraceMeta())
	if err != nil {
		t.Fatal(err)
	}
	cell := tp.Cell(7)
	if cell.Name() != "deliverytrace" {
		t.Fatalf("cell name = %q", cell.Name())
	}
	// First packet arrives at trace time 0 => uses opportunity 0 (t=0).
	out := cell.Process(engine.InFlight{Seq: 1, Data: make([]byte, 1500), RecvAt: 0, DeliverAt: 0})
	if len(out) != 1 {
		t.Fatalf("expected 1 forwarded, got %d", len(out))
	}
	// Second packet (1500B) needs the next opportunity (t=10ms => 10e6 ns).
	out = cell.Process(engine.InFlight{Seq: 2, Data: make([]byte, 1500), RecvAt: 0, DeliverAt: 0})
	if len(out) != 1 {
		t.Fatalf("expected 1 forwarded, got %d", len(out))
	}
	if out[0].DeliverAt != 10*1_000_000 {
		t.Fatalf("second packet DeliverAt = %d, want %d", out[0].DeliverAt, 10*1_000_000)
	}
}

// ---------------------------------------------------------------------------
// Manifest loader / verifier.
// ---------------------------------------------------------------------------

// TestManifestVerifyDetectsTamper writes a trace + a correct manifest, verifies
// it, then corrupts the trace and asserts Verify fails on the sha256 mismatch.
func TestManifestVerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "t.up")
	body := "# mtu=1500\n0\n1\n2\n3\n"
	if err := os.WriteFile(tracePath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	manJSON := `{
  "version": 1,
  "traces": [
    {"name":"t","file":"t.up","format":"mahimahi","sha256":"` + HashBytes([]byte(body)) + `",
     "cite":"fixture","source":"synthetic","license":"CC0-1.0"}
  ]
}`
	manPath := filepath.Join(dir, "MANIFEST.json")
	if err := os.WriteFile(manPath, []byte(manJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	man, err := LoadManifestFile(manPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := man.Verify(); err != nil {
		t.Fatalf("verify clean manifest: %v", err)
	}

	// TraceProfile must load and carry provenance.
	tp, err := man.TraceProfile("t")
	if err != nil {
		t.Fatalf("TraceProfile: %v", err)
	}
	if err := tp.ValidateProvenance(); err != nil {
		t.Fatalf("loaded trace provenance: %v", err)
	}

	// Tamper.
	if err := os.WriteFile(tracePath, []byte(body+"99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := man.Verify(); err == nil {
		t.Fatal("expected Verify to fail after tampering with the trace")
	}
	if _, err := man.TraceProfile("t"); err == nil {
		t.Fatal("expected TraceProfile to fail loading a tampered trace")
	}
}

// TestManifestRejectsBadProvenance covers the lint paths on a parsed manifest.
func TestManifestRejectsBadProvenance(t *testing.T) {
	cases := map[string]string{
		"missing cite":    `{"version":1,"traces":[{"name":"t","file":"t.up","sha256":"` + strings.Repeat("a", 64) + `","source":"synthetic","license":"x"}]}`,
		"missing source":  `{"version":1,"traces":[{"name":"t","file":"t.up","sha256":"` + strings.Repeat("a", 64) + `","cite":"c","license":"x"}]}`,
		"bad source":      `{"version":1,"traces":[{"name":"t","file":"t.up","sha256":"` + strings.Repeat("a", 64) + `","cite":"c","source":"bogus","license":"x"}]}`,
		"missing license": `{"version":1,"traces":[{"name":"t","file":"t.up","sha256":"` + strings.Repeat("a", 64) + `","cite":"c","source":"synthetic"}]}`,
		"bad sha":         `{"version":1,"traces":[{"name":"t","file":"t.up","sha256":"xyz","cite":"c","source":"synthetic","license":"x"}]}`,
		"missing file":    `{"version":1,"traces":[{"name":"t","sha256":"` + strings.Repeat("a", 64) + `","cite":"c","source":"synthetic","license":"x"}]}`,
	}
	for name, js := range cases {
		man, err := LoadManifest(strings.NewReader(js))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := man.ValidateProvenance(); err == nil {
			t.Errorf("%s: expected ValidateProvenance to fail", name)
		}
	}
}

// TestManifestUnknownFieldRejected ensures the loader is strict.
func TestManifestUnknownFieldRejected(t *testing.T) {
	js := `{"version":1,"traces":[],"bogus":true}`
	if _, err := LoadManifest(strings.NewReader(js)); err == nil {
		t.Fatal("expected error on unknown manifest field")
	}
}

// TestHashBytes is a sanity check on the digest helper.
func TestHashBytes(t *testing.T) {
	// SHA-256 of the empty string.
	if got := HashBytes(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("HashBytes(nil) = %s", got)
	}
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
