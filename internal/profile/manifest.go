package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// manifest.go is the traces provenance manifest: a JSON catalogue
// (traces/MANIFEST.json) that pins every shipped trace by SHA-256 and records
// its license and source. The loader parses it; the verifier recomputes each
// file's SHA-256 and fails on any mismatch (the CI gate from PLAN.md: "CI fails
// on checksum/license-field mismatch"). stdlib has no TOML, so JSON is used.

// ManifestEntry describes one trace file's provenance.
type ManifestEntry struct {
	// Name is the stable trace identifier (matches a TraceProfile.Name).
	Name string `json:"name"`
	// File is the path to the trace, relative to the manifest's directory.
	File string `json:"file"`
	// Format is the on-disk format, e.g. "mahimahi".
	Format string `json:"format"`
	// SHA256 is the lowercase hex SHA-256 of the file's bytes.
	SHA256 string `json:"sha256"`
	// Description is a one-line human summary.
	Description string `json:"description,omitempty"`

	// Cite/Source/License are the mandatory provenance triple.
	Cite    string `json:"cite"`
	Source  Source `json:"source"`
	License string `json:"license"`
	Notes   string `json:"notes,omitempty"`
}

// Manifest is the parsed traces/MANIFEST.json.
type Manifest struct {
	Version int             `json:"version"`
	Entries []ManifestEntry `json:"traces"`

	// dir is the directory the manifest was loaded from; File paths resolve
	// against it. Empty when parsed from a raw reader.
	dir string
}

// LoadManifest parses a Manifest from JSON. File paths in the result resolve
// relative to the current working directory (use LoadManifestFile to anchor
// them to the manifest's own directory).
func LoadManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("profile: decode manifest: %w", err)
	}
	return m, nil
}

// LoadManifestFile reads and parses the manifest at path, anchoring entry File
// paths to the manifest's directory so Verify can find them regardless of the
// process working directory.
func LoadManifestFile(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("profile: open manifest: %w", err)
	}
	defer f.Close()
	m, err := LoadManifest(f)
	if err != nil {
		return Manifest{}, err
	}
	m.dir = filepath.Dir(path)
	return m, nil
}

// resolve returns the on-disk path for an entry, anchored to the manifest dir.
func (m Manifest) resolve(e ManifestEntry) string {
	if m.dir == "" {
		return e.File
	}
	return filepath.Join(m.dir, e.File)
}

// Get returns the named entry and whether it exists.
func (m Manifest) Get(name string) (ManifestEntry, bool) {
	for _, e := range m.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return ManifestEntry{}, false
}

// Names returns the trace names sorted alphabetically.
func (m Manifest) Names() []string {
	out := make([]string, 0, len(m.Entries))
	for _, e := range m.Entries {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

// ValidateProvenance checks that every entry carries a name, file, valid Source,
// Cite and License, and a well-formed SHA-256 hex digest. It does not touch the
// filesystem (use Verify for that).
func (m Manifest) ValidateProvenance() error {
	seen := make(map[string]struct{}, len(m.Entries))
	for _, e := range m.Entries {
		if e.Name == "" {
			return fmt.Errorf("manifest: entry with empty name")
		}
		if _, dup := seen[e.Name]; dup {
			return fmt.Errorf("manifest: duplicate trace name %q", e.Name)
		}
		seen[e.Name] = struct{}{}
		if e.File == "" {
			return fmt.Errorf("manifest: trace %q: missing file", e.Name)
		}
		if e.Cite == "" {
			return fmt.Errorf("manifest: trace %q: missing cite", e.Name)
		}
		if e.Source == "" {
			return fmt.Errorf("manifest: trace %q: missing source", e.Name)
		}
		if !ValidSource(e.Source) {
			return fmt.Errorf("manifest: trace %q: invalid source %q", e.Name, e.Source)
		}
		if e.License == "" {
			return fmt.Errorf("manifest: trace %q: missing license", e.Name)
		}
		if err := validHexSHA256(e.SHA256); err != nil {
			return fmt.Errorf("manifest: trace %q: %w", e.Name, err)
		}
	}
	return nil
}

// Verify recomputes the SHA-256 of every entry's file and compares it to the
// recorded digest, returning the first mismatch (or read error). It also runs
// ValidateProvenance first. A nil error means every shipped trace is present
// and byte-identical to what the manifest pins.
func (m Manifest) Verify() error {
	if err := m.ValidateProvenance(); err != nil {
		return err
	}
	for _, e := range m.Entries {
		got, err := hashFile(m.resolve(e))
		if err != nil {
			return fmt.Errorf("manifest: trace %q: %w", e.Name, err)
		}
		if !equalFoldASCII(got, e.SHA256) {
			return fmt.Errorf("manifest: trace %q: sha256 mismatch: file %s, manifest %s",
				e.Name, got, e.SHA256)
		}
	}
	return nil
}

// TraceProfile imports the named manifest entry's trace file into a cited
// TraceProfile, carrying the manifest's provenance. It verifies the file's
// SHA-256 first, so a tampered trace fails to load.
func (m Manifest) TraceProfile(name string) (TraceProfile, error) {
	e, ok := m.Get(name)
	if !ok {
		return TraceProfile{}, fmt.Errorf("manifest: unknown trace %q", name)
	}
	path := m.resolve(e)
	got, err := hashFile(path)
	if err != nil {
		return TraceProfile{}, fmt.Errorf("manifest: trace %q: %w", name, err)
	}
	if !equalFoldASCII(got, e.SHA256) {
		return TraceProfile{}, fmt.Errorf("manifest: trace %q: sha256 mismatch: file %s, manifest %s", name, got, e.SHA256)
	}
	f, err := os.Open(path)
	if err != nil {
		return TraceProfile{}, fmt.Errorf("manifest: trace %q: %w", name, err)
	}
	defer f.Close()
	return ImportMahimahi(f, TraceMeta{
		Name:        e.Name,
		Description: e.Description,
		Cite:        e.Cite,
		Source:      e.Source,
		License:     e.License,
		Notes:       e.Notes,
	})
}

// HashBytes returns the lowercase hex SHA-256 of b (exposed so manifest authors
// and tests can compute the digest a new entry should pin).
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validHexSHA256(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("sha256 %q is not 64 hex chars", s)
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("sha256 %q is not valid hex: %w", s, err)
	}
	return nil
}
