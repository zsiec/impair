# Impair / Transit-WPT

A **media-transport-aware** network-impairment engine (**Impair**) and a `quic-interop-runner`-style cross-protocol conformance + QoE matrix (**Transit-WPT**) for low-latency transports: **SRT, RIST, and (later) MoQ**.

The thing nobody has built: deterministic, seedable, *protocol-aware* impairment driving standards-defined media workloads through real stacks, graded by an oracle that **never trusts the implementation's self-reported stats**.

## Documentation

| Doc | What |
|---|---|
| [`docs/DESIGN.md`](docs/DESIGN.md) | Integrated architecture: components, tech stack, oracle, SUT contract, requirements |
| [`docs/EMULATORS.md`](docs/EMULATORS.md) | Survey of ~35 network emulators → the impairment-engine architecture (Shadow/ns-3/Mahimahi/KauNet lessons) |
| [`docs/RESEARCH.md`](docs/RESEARCH.md) | Cited findings per research stream (prior art, conformance specs, QoE oracles, governance) |
| [`docs/ROADMAP.md`](docs/ROADMAP.md) | Phased plan P0→v1 |
| [`docs/OPEN-QUESTIONS.md`](docs/OPEN-QUESTIONS.md) | **Read first.** Adversarial completeness critique — the load-bearing holes |

---

## The honest framing (corrections the research forced)

The synthesized design oversold three things; the completeness critic caught them. The corrected version is *stronger* because it's real:

### 1. "Deterministic" means the impairment *schedule*, not bit-identical end-to-end results
You cannot get bit-identical end-to-end goodput/latency while the systems-under-test are real async processes on a real kernel UDP socket (their clocks, goroutine scheduling, and the OS stack are outside our control). Split the claim cleanly:
- **Bit-deterministic** — *only* the in-process **Sans-I/O** path (your own Go/Rust cores), where we own the clock and feed `(virtual-time, packets)` directly. This is the genuine superpower, and it's unique to your Sans-I/O design. It does **not** extend to libsrt/libRIST/moq-rs binaries.
- **Deterministic impairment + statistically-bounded results** — every other path. Same seed → identical *drop/delay decisions* (CI-diffable), but end-to-end metrics are reported with bootstrap confidence intervals, and conformance fails only on CI non-overlap.

### 2. The moat is the *oracle + reference*, not "selective dropping"
Protocol-aware *selective drop* only works on **cleartext / test-key** payloads. In production-realistic configs (SRT KM-encryption, RIST DTLS/PSK, MoQ-over-QUIC) the media plane is opaque, so selective drop degrades to header-level. The durable, defensible moat is the combination of:
- a **protocol-aware oracle** that decodes the wire and grades TSBPD-deadline-met ratio / FEC-recovery / ARQ efficiency from *ground truth*, not the impl's stats;
- a **deterministic Sans-I/O reference** for differential cross-checking;
- **methodology + standards alignment** (RFC 6349-style protocol-relative normalization; consume `draft-evens-moq-bench` profiles and supply its missing impairment layer).

  Protocol-aware *impairment* remains a real feature — in a **test-key mode** (harness holds the keys) and via **q-log side channels** — just not the headline for encrypted production traffic.

### 3. Scope reality: you have SRT+RIST (Go+Rust). You do **not** have a MoQ stack.
The design assumed reusable MoQ Sans-I/O parsers — they don't exist. So **MoQ is build/integrate-from-scratch** (a large QUIC + WebTransport + MoQT surface). Sequence accordingly: lead with **SRT** (your strongest, with the existing `txbench` seed), then **RIST**, and treat **MoQ as a later, partner-dependent or QUIC-library-leveraged phase** — not P0–P2.

### 4. Neutrality: your own stacks can't be the "golden" arbiter unprovenanced
Using `srtgo`/your Rust stacks to judge libsrt is circular unless they're independently conformance-validated. Frame the oracle as **wire-ground-truth + spec-derived assertions**; the Sans-I/O differential is a *cross-check that flags divergence for investigation*, never the verdict. (This protects the neutrality that is the whole governance moat.)

> Also note: `~/dev/impair/` is currently **docs only**. The "core IP" today is a ~156-line prototype `relay.go` in `txbench` with two known determinism bugs (wall-clock `time.AfterFunc`; unseeded PRNG). P0 is real engineering, not a 4-6 week "evolution."

---

## Architecture in one picture

```
                    ┌───────────────── Transit-WPT (matrix + governance) ─────────────────┐
                    │  static results.json ─► static HTML matrix (QIR/wpt.fyi model)        │
                    │  register-by-PR manifests: implementations_{srt,rist,moq}.json        │
                    └──────────────────────────────▲──────────────────────────────────────┘
                                                    │ results + artifacts (pcap/qlog/QoE trace)
   ┌──────────── Orchestrator (Go, from txbench) ─┴─────────────────────────────────────┐
   │  resolve scenario (CUE) → schedule SUT pair/triple → pick tier → run reps → oracle    │
   └───┬───────────────────────────────────┬──────────────────────────────────────────────┘
       │ Tier-1 (white-box, BIT-DETERMINISTIC)│ Tier-2 (black-box, dist-reproducible)
       ▼                                       ▼
  ┌─ Sans-I/O cores (your Go/Rust) ─┐   ┌─ Foreign binaries: libsrt, libRIST, moq-rs ─┐
  │  virtual clock + replayed pattern│   │  Docker, QIR env-var/exit-127 contract       │
  └───────────────┬──────────────────┘   │  real socket through netem / Impair relay    │
                  │                       └───────────────┬──────────────────────────────┘
                  ▼                                       ▼
        ┌──── Impair engine (cell pipeline) ──────────────────────────────┐
        │ classify→loss(GE/4-state)→corrupt→dup→reorder→AQM/queue→delay    │
        │ + protocol-aware cells (TSBPD-deadline, NACK-window, FEC-block)  │
        │ master seed → per-stage keyed substreams · KauNet droplist replay│
        └──── Oracle: sender truth ⨯ wire ledger ⨯ receiver delivery ──────┘
```

## What a *fully complete* v1 actually requires

- **Impair engine**: virtual-clock event core; netem-vocab loss models (Bernoulli/Gilbert/GE/4-state) with GE burst loss first-class; AQM/queue + buffer; reorder/dup/corrupt; per-stage seeded substreams; KauNet-style droplist replay; multi-link + timed fault injection; multi-Gbps-clean datapath.
- **Profiles**: seedable realistic profiles (G.1050/TIA-921 via spandsp) + curated named profiles (LTE-congested, GEO/LEO, Wi-Fi-roam) with **license-cleared, checksum-pinned traces** (note: MAWI/CRAWDAD are retired — sourcing is an open task).
- **Oracle**: three vantage points (sender self-describing payload, wire ledger, receiver) + spec-derived per-protocol assertions; a *per-delivery-unit* model that actually maps to SRT streams **and** RIST RTP/SSRC packets **and** MoQ objects/subgroups (the "one payload/one oracle" premise needs per-protocol specialization).
- **Wire observer**: Sans-I/O decoders for SRT control, RIST RTCP/NACK + 2022-1/-7 FEC, MoQ objects (test-key mode for encrypted).
- **SUT contract**: Tier-1 in-process `transport` interface (promote `txbench`); Tier-2 QIR-style Docker entrypoint (ROLE/SCENARIO/exit-127).
- **Scenario schema**: one CUE source → Go structs + result JSON-Schema + matrix; declarative, diffable, seed+repeat baked in.
- **Results + matrix**: static-site model; versioned upload schema up front; clickable pcap/qlog/QoE artifacts; cross-protocol Measurement views.
- **Methodology spec**: a published "RFC-6349-for-low-latency-media" with a mandated results schema; align vocabulary with `draft-evens-moq-bench`.
- **Governance/GTM**: register-by-PR + sandboxed/cost-bounded CI for untrusted vendor images; recruit 2-3 friendly first-mover vendors *before* public launch; IETF/VSF/OpenMOQ engagement to be blessed not bypassed; license + neutral-governance posture.
- **Determinism calibration**: bound Tier-1 against ns-3/hardware on a size-1 link to earn the "reproducible" claim (open: platform + acceptance metric).

## Recommended starting move

**Build the SRT-only Tier-1 slice end-to-end first** (P0+P1): harden the relay into a virtual-clock, seeded, GE-burst-loss engine; add the SRT wire observer; ship a 3-stack static matrix with PASS/FAIL/WARN + clickable artifacts. That single vertical proves the whole thesis (deterministic + protocol-aware + oracle-graded), is genuinely achievable solo, reuses `txbench`, and de-risks everything downstream — *before* taking on RIST, MoQ, foreign-binary CI, or governance.
