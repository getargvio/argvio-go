# Contributing to argvio-go

Thanks for considering a contribution. This SDK is embedded in many
downstream CLI tools, so changes here have a wide blast radius — please
read this before opening a PR.

## Ground rules

- **No panics.** Every exported function must recover internally and
  degrade to a no-op rather than crash the host CLI. PRs that add a new
  exported function must include a test that proves it doesn't panic on
  nil/zero-value/malformed input.
- **No new dependencies without justification.** Every transitive
  dependency is a supply-chain and binary-size cost imposed on every
  vendor. If your change needs a new module, say why in the PR
  description and why the standard library or an existing dependency
  (OTel, Cobra) doesn't already cover it.
- **The tier→field mapping is not yours to redefine.** The
  `cli.analytics.tier` allowlist mirrors a schema owned by the `public`
  server repo. If you need to add or change a taxonomy field, get the
  server-side schema change merged first, then update this repo to
  match — don't invent new fields or tiers here.
- **Non-blocking is a hard requirement.** Anything on the hot path of a
  `Record*` call must be O(cheap) and must not perform network I/O
  synchronously.

## Development

```bash
go build ./...
go vet ./...
go test ./...
go test -bench=. -benchmem ./...
```

We use `golangci-lint` in CI; run it locally before opening a PR:

```bash
golangci-lint run ./...
```

## Supported Go versions

CI builds against the two most recent Go minor releases. Don't use
language features newer than the older of the two.

## Supported Cobra versions

CI tests against the latest two minor releases of `github.com/spf13/cobra`.
Avoid depending on Cobra internals or very recent Cobra-only APIs.

## Commit / PR style

- Keep PRs focused on one change.
- Explain *why*, not just *what*, in the PR description — especially for
  anything touching consent, tiering, or the batch processor.
- Add/update Godoc on any exported symbol you touch — this package's
  doc comments are a public API surface other companies depend on.

## Releases

Versions are semantic-versioned git tags (`vX.Y.Z`), not a version
constant in code. Maintainers cut releases; please don't tag releases
in a PR.
