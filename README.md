# impair

A deterministic, protocol-aware network-impairment engine for low-latency media transports — **SRT, RIST, and (header-level) MoQ**.

[![Go Reference](https://pkg.go.dev/badge/github.com/zsiec/impair.svg)](https://pkg.go.dev/github.com/zsiec/impair)
[![CI](https://github.com/zsiec/impair/actions/workflows/ci.yml/badge.svg)](https://github.com/zsiec/impair/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/zsiec/impair)](https://goreportcard.com/report/github.com/zsiec/impair)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> **Status:** pre-1.0. The SRT and RIST datapaths are implemented and tested; the public API may still change before a tagged release. MoQ support is header-detection only.

Most network emulators (`netem`, Toxiproxy, Comcast) impair bytes blindly and roll fresh dice every run. `impair` is built for grading media transports instead: a **seeded, Sans-I/O core** decides the fate of every packet as a pure function of `(seed, config, arrival time)`, so the same scenario reproduces the same impairment schedule byte-for-byte — and a real-socket **relay** applies those decisions to live SRT/RIST traffic. That makes loss/jitter/reorder a controlled, repeatable variable instead of noise.

## Features

- **Deterministic, seedable impairment** — same seed + same packets => identical forward/drop/delay decisions, every run. Each cell draws from its own keyed RNG substream, so adding a stage never reshuffles another.
- **A pipeline of composable cells** — loss (Bernoulli / Gilbert-Elliott / 4-state), corruption, duplication, reorder, rate limiting / AQM queueing, fixed + jittered delay, and KauNet-style droplist replay.
- **Sans-I/O core** — the `engine` owns no sockets and no clock; you feed it `(virtual-time, packet)` and it returns the actions. Pure, testable, and trivially golden-tested.
- **Real-socket relays** — `relay` (single-port, SRT-style) and `ristrelay` (dual-port RTP/RTCP for RIST Simple Profile) apply the engine's decisions to live UDP traffic, with a live-swappable engine for interactive tuning.
- **Protocol-aware wire observers + oracles** — decode SRT/RIST off the wire (control vs data, retransmits, NAKs, ACK progression) and grade conformance from **ground truth**, never the implementation's self-reported stats.
- **Recovery models** — SMPTE ST 2022-1 FEC and ST 2022-7 seamless-redundancy oracles for "should this have been recoverable?" checks.
- **An encrypted-flow guard** — payload-selective cells are refused at build time on flows whose media plane is opaque (SRT-KM, RIST-DTLS, QUIC/MoQ), so impairment never silently no-ops on ciphertext.
- **Zero dependencies** — the entire module is stdlib-only.

## Install

```bash
go get github.com/zsiec/impair
```

Requires Go 1.24 or later.

## Quick Start

### In-process (no sockets, fully deterministic)

Build an engine from a named scenario and run packets through it directly:

```go
package main

import (
	"fmt"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/scenario"
)

func main() {
	// A Scenario is just a seed + an ordered impairment pipeline.
	eng, err := scenario.Build(scenario.Examples()["lossy-burst"])
	if err != nil {
		panic(err)
	}

	// Hand the engine one packet at a time with a virtual arrival time (ns).
	// Same seed + same packets => identical decisions on every run.
	var forwarded, dropped int
	for i := 0; i < 500; i++ {
		actions := eng.Handle(engine.Packet{Data: []byte("frame"), Dir: engine.C2S}, int64(i)*1_000_000)
		for _, a := range actions {
			if a.Kind == engine.Drop {
				dropped++ // a.Reason names the cell that dropped it
			} else {
				forwarded++ // forward a.Data on a.Dir at a.DeliverAt
			}
		}
	}
	fmt.Printf("forwarded %d, dropped %d\n", forwarded, dropped)
}
```

### Live (impair a real SRT/RIST flow)

Put a relay in front of an upstream receiver; point your sender at the relay's address and it forwards traffic through the impairment engine:

```go
eng, _ := scenario.Build(scenario.Examples()["lte-congested"])

r, err := relay.New(eng, "127.0.0.1:5000", nil) // upstream = the real receiver
if err != nil {
	log.Fatal(err)
}
defer r.Close()

log.Printf("point your SRT sender at %s (forwards to 127.0.0.1:5000)", r.Addr())
// ... run traffic ...

st := r.Stats() // relay-side ground truth
log.Printf("forwarded=%d dropped=%d tail-dropped=%d", st.Forwarded, st.Dropped, st.TailDropped)
```

`r.SetEngine(...)` swaps the impairment live without dropping the flow — the basis for interactive "drag a fader and watch the picture break" tooling. For RIST (even-port RTP / odd-port RTCP), use `ristrelay.New` the same way.

## Concepts

`impair` is two layers, deliberately separated:

1. **The engine (Sans-I/O).** `engine.Engine` runs one ordered `Cell` pipeline per direction. `Handle(packet, recvAt)` returns a deterministic slice of `Action`s (Forward/Drop, with delivery time and drop reason). It touches no sockets and reads no real clock — all timing is virtual — so it is exhaustively golden-testable and bit-reproducible.

2. **The drivers (real I/O).** `relay` and `ristrelay` are thin UDP proxies that call the engine for each datagram and carry out its decisions on a min-heap scheduler. They are where virtual time meets the wall clock.

A **`Scenario`** (`scenario` package) is the serializable front door: a seed plus a pipeline of stages (or separate client→server / server→client pipelines). `scenario.Build` compiles it into an `engine.Engine`, allocating each cell its own deterministic RNG substream keyed by position — this is what lets you reorder or insert stages without perturbing the others' draws. Scenarios round-trip to JSON (`scenario.Load` / `scenario.Save`).

## Determinism: what is actually guaranteed

The deterministic guarantee is about the **impairment schedule**, and it is strongest in-process:

- **Bit-deterministic** on the in-process Sans-I/O path (`engine.Handle`), where `impair` owns the clock and feeds packets directly. Same `(seed, config, input)` => the same `Action` stream, byte-for-byte. This is the property the `schedule-golden` gate enforces.
- **Deterministic decisions, statistically-bounded outcomes** once a real socket and a real transport stack are in the loop. The engine still makes the same drop/delay *decisions* for a given seed (CI-diffable), but end-to-end metrics — goodput, latency — depend on the system-under-test's own clocks, scheduling, and kernel UDP path, which are outside the harness. Those are meant to be reported with confidence intervals across repeats, not asserted bit-exact.

Keeping that line honest is the point: the engine is a reproducible *cause*, not a promise of reproducible end-to-end *numbers* against asynchronous real binaries.

## Packages

| Package | What it does |
|---|---|
| [`engine`](engine) | Sans-I/O deterministic core — runs a packet through per-direction cell pipelines, returns Forward/Drop actions. No I/O, no real clock. |
| [`scenario`](scenario) | Declarative, serializable config → built engine, with per-cell keyed RNG substreams. The single config target for UI/CLI/JSON. |
| [`relay`](relay) | Real-socket UDP relay (SRT-style, single-port) that applies engine decisions; live-swappable engine; optional one-way-delay ledger. |
| [`ristrelay`](ristrelay) | Dual-port relay for RIST Simple Profile (even RTP / odd RTCP) — impairs media, passes RTCP cleanly. |
| [`wire`](wire) / [`ristwire`](ristwire) | Sans-I/O SRT / RIST wire decoders + passive observers (retransmits, NAKs, ACK progression, seq gaps). |
| [`quicwire`](quicwire) | Best-effort QUIC/MoQ header sniff to auto-label encrypted flows. |
| [`oracle`](oracle) / [`ristoracle`](ristoracle) | Grade wire observations into pass/warn/fail checks from spec-derived invariants. |
| [`fec`](fec) / [`bond`](bond) | SMPTE ST 2022-1 FEC and ST 2022-7 seamless-redundancy recoverability models + oracles. |
| [`result`](result) | Shared verdict vocabulary (Verdict / Check / Result / Matrix) — a dependency-free contract. |
| [`report`](report) | Render a `result.Matrix` to JSON and a self-contained static HTML conformance grid. |
| [`stat`](stat) | Bootstrap confidence intervals + overlap tests for distribution-reproducible (Tier-2) results. |
| [`cmd/impair-profiles`](cmd/impair-profiles) | Inspect the provenanced profile/trace library and print citations. |

Concrete cell implementations live under `internal/cells/`; RNG substreams, droplist/pattern formats, and the G.1050/Mahimahi profile importers under `internal/`. Internal packages are not part of the public API.

## Scenarios

`scenario.Examples()` ships a few ready-made profiles — `clean`, `lossy-burst`, `lte-congested`, `satellite-geo` — useful as starting points or test fixtures. Build your own as a literal or load from JSON:

```go
sc := scenario.Scenario{
	Name: "my-link",
	Seed: 0xC0FFEE,
	Pipeline: []scenario.Stage{
		{Kind: "loss", Params: map[string]float64{"p": 0.02}},
		{Kind: "delay", Params: map[string]float64{"baseMs": 30, "jitterMs": 8}},
	},
}
eng, err := scenario.Build(sc)
```

## Development

```bash
make build            # go build ./...
make test             # go test ./...
make race             # go test -race ./...
make vet              # go vet ./...
make schedule-golden  # determinism gate: same seed must reproduce byte-identical patterns
make update-golden    # regenerate committed goldens after an intentional behavior change
```

The module is stdlib-only (no `require` block, no `go.sum`). CI runs vet, a gofmt check, the race tests, and the determinism gate.

## Status & scope

- **SRT** — wire observer, oracle, single-port relay: implemented and tested.
- **RIST** — Simple Profile RTP/RTCP observer, oracle, dual-port relay; FEC (2022-1) and bonding (2022-7) recovery models: implemented and tested.
- **MoQ** — header detection only (used to auto-label encrypted flows); no MoQ-object-level oracle yet.

See [`docs/`](docs) for the design notes and roadmap behind these choices.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The short version: keep the core stdlib-only and deterministic, and run `gofmt -l .`, `go vet ./...`, `go test -race ./...`, and `make schedule-golden` before opening a PR.

## License

[MIT](LICENSE)
