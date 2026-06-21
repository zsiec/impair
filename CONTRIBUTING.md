# Contributing to impair

Thanks for your interest in contributing to impair!

## Bug Reports

Please open a [GitHub issue](https://github.com/zsiec/impair/issues) with:

- Go version (`go version`)
- Operating system and architecture
- Minimal reproduction steps
- Expected vs. actual behavior

For determinism bugs (the same seed producing different decisions across runs),
include the scenario JSON or the `scenario.Examples()` key and the run count.

## Pull Requests

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-change`)
3. Make your changes
4. Run the gauntlet below — all of it
5. Commit with a clear message
6. Open a pull request

```bash
gofmt -l .                       # must print nothing
go vet ./...
go test -race -count=1 ./...
make schedule-golden             # the determinism gate
```

Keep changes focused — one feature or fix per PR.

## Ground rules

- **The core stays stdlib-only.** `impair` has zero external module dependencies
  and intends to keep it that way; do not add a `require` to go.mod without a
  very good reason and a discussion first.
- **Determinism is binding.** A `Cell` must be a deterministic function of its
  state and its seeded `rng.Source` substream — no real time (`time.Now`), no
  global `math/rand`, no map-iteration nondeterminism. The `schedule-golden`
  gate enforces that the same seed reproduces a byte-identical impairment
  pattern; if you change cell behavior intentionally, regenerate the goldens
  with `make update-golden` and call it out in the PR.
- **The encrypted-flow guard.** Any new payload-selective cell (one that
  inspects packet *contents*) must return `true` from `RequiresCleartext()` so
  `scenario.Build` refuses to wire it onto an encrypted flow. Timing/size/loss
  cells return `false`.
- **Internal packages** under `internal/` are not part of the public API.
- **Tests.** Add tests for new behavior; new cells and oracle checks should ship
  with goldens or negative controls.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
