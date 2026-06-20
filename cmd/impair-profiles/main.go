// Command impair-profiles inspects the named, provenanced impairment-profile
// library and the traces manifest, printing citations for use in papers and
// reports.
//
// Usage:
//
//	impair-profiles list                 # list profiles + manifest traces
//	impair-profiles show <name>          # full parameters + provenance
//	impair-profiles cite <name>          # one-line citation (Cite / Source / License)
//
// A profile name is a built-in (e.g. "g1050-C"); a trace name is a manifest
// entry (e.g. "synthetic-lte-handover"). Pass -manifest to point at a non
// -default traces/MANIFEST.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/zsiec/impair/internal/profile"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "impair-profiles:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("impair-profiles", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "traces/MANIFEST.json", "path to the traces manifest (JSON)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON instead of text")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: impair-profiles [flags] <list|show|cite> [name]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return fmt.Errorf("missing command")
	}

	// The manifest is optional: load it if present, but built-in profiles work
	// without it.
	man, manErr := profile.LoadManifestFile(*manifestPath)

	switch rest[0] {
	case "list":
		return cmdList(stdout, man, manErr)
	case "show":
		if len(rest) < 2 {
			return fmt.Errorf("show: missing <name>")
		}
		return cmdShow(stdout, rest[1], man, manErr, *asJSON)
	case "cite":
		if len(rest) < 2 {
			return fmt.Errorf("cite: missing <name>")
		}
		return cmdCite(stdout, rest[1], man, manErr, *asJSON)
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func cmdList(w io.Writer, man profile.Manifest, manErr error) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tSOURCE\tDESCRIPTION")
	for _, name := range profile.Names() {
		p, _ := profile.Get(name)
		fmt.Fprintf(tw, "%s\tprofile\t%s\t%s\n", p.Name, p.Source, p.Description)
	}
	if manErr == nil {
		for _, name := range man.Names() {
			e, _ := man.Get(name)
			fmt.Fprintf(tw, "%s\ttrace\t%s\t%s\n", e.Name, e.Source, e.Description)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if manErr != nil {
		fmt.Fprintf(w, "\n(traces manifest unavailable: %v)\n", manErr)
	}
	return nil
}

func cmdShow(w io.Writer, name string, man profile.Manifest, manErr error, asJSON bool) error {
	if p, ok := profile.Get(name); ok {
		if asJSON {
			return writeJSON(w, p)
		}
		showProfile(w, p)
		return nil
	}
	if manErr == nil {
		if e, ok := man.Get(name); ok {
			if asJSON {
				return writeJSON(w, e)
			}
			showTrace(w, e)
			return nil
		}
	}
	return notFound(name, man, manErr)
}

func cmdCite(w io.Writer, name string, man profile.Manifest, manErr error, asJSON bool) error {
	if p, ok := profile.Get(name); ok {
		if asJSON {
			return writeJSON(w, p.Provenance())
		}
		writeCite(w, p.Name, p.Cite, string(p.Source), p.License)
		return nil
	}
	if manErr == nil {
		if e, ok := man.Get(name); ok {
			if asJSON {
				return writeJSON(w, profile.Provenance{Cite: e.Cite, Source: e.Source, License: e.License})
			}
			writeCite(w, e.Name, e.Cite, string(e.Source), e.License)
			return nil
		}
	}
	return notFound(name, man, manErr)
}

func showProfile(w io.Writer, p profile.Profile) {
	tw := tabwriter.NewWriter(w, 0, 2, 1, ' ', 0)
	fmt.Fprintf(tw, "name:\t%s\n", p.Name)
	fmt.Fprintf(tw, "kind:\tprofile\n")
	fmt.Fprintf(tw, "description:\t%s\n", p.Description)
	fmt.Fprintf(tw, "grade:\t%d\n", p.Grade)
	fmt.Fprintf(tw, "loss:\t%.4g%% (%s)\n", p.LossPct, lossModelName(p.LossModel))
	if p.LossModel == profile.LossGE {
		fmt.Fprintf(tw, "burstR:\t%.4g\n", p.BurstR)
	}
	fmt.Fprintf(tw, "baseDelay:\t%.4g ms\n", p.BaseDelayMs)
	if p.DelayDist == "normal" {
		fmt.Fprintf(tw, "jitter:\tsigma=%.4g ms (normal)\n", p.SigmaMs)
	} else if p.JitterMs > 0 {
		fmt.Fprintf(tw, "jitter:\t+/-%.4g ms (uniform)\n", p.JitterMs)
	}
	if p.DelayCorrelation > 0 {
		fmt.Fprintf(tw, "delayCorrelation:\t%.4g\n", p.DelayCorrelation)
	}
	if p.ReorderPct > 0 {
		fmt.Fprintf(tw, "reorder:\t%.4g%% gap=%.4g ms\n", p.ReorderPct, p.ReorderGapMs)
	}
	fmt.Fprintf(tw, "cite:\t%s\n", p.Cite)
	fmt.Fprintf(tw, "source:\t%s\n", p.Source)
	fmt.Fprintf(tw, "license:\t%s\n", p.License)
	if p.Notes != "" {
		fmt.Fprintf(tw, "notes:\t%s\n", p.Notes)
	}
	tw.Flush()
}

func showTrace(w io.Writer, e profile.ManifestEntry) {
	tw := tabwriter.NewWriter(w, 0, 2, 1, ' ', 0)
	fmt.Fprintf(tw, "name:\t%s\n", e.Name)
	fmt.Fprintf(tw, "kind:\ttrace\n")
	fmt.Fprintf(tw, "description:\t%s\n", e.Description)
	fmt.Fprintf(tw, "file:\t%s\n", e.File)
	fmt.Fprintf(tw, "format:\t%s\n", e.Format)
	fmt.Fprintf(tw, "sha256:\t%s\n", e.SHA256)
	fmt.Fprintf(tw, "cite:\t%s\n", e.Cite)
	fmt.Fprintf(tw, "source:\t%s\n", e.Source)
	fmt.Fprintf(tw, "license:\t%s\n", e.License)
	if e.Notes != "" {
		fmt.Fprintf(tw, "notes:\t%s\n", e.Notes)
	}
	tw.Flush()
}

func writeCite(w io.Writer, name, cite, source, license string) {
	fmt.Fprintf(w, "%s\n", name)
	fmt.Fprintf(w, "  cite:    %s\n", cite)
	fmt.Fprintf(w, "  source:  %s\n", source)
	fmt.Fprintf(w, "  license: %s\n", license)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func lossModelName(m profile.LossModel) string {
	switch m {
	case profile.LossGE:
		return "Gilbert-Elliott"
	case profile.LossBernoulli:
		return "Bernoulli"
	case "":
		return "Bernoulli"
	default:
		return string(m)
	}
}

func notFound(name string, man profile.Manifest, manErr error) error {
	var known []string
	known = append(known, profile.Names()...)
	if manErr == nil {
		known = append(known, man.Names()...)
	}
	sort.Strings(known)
	return fmt.Errorf("unknown profile or trace %q (known: %v)", name, known)
}
