# argvio-go

A Go SDK for emitting CLI usage telemetry to Argvio's ingest server,
built around a tiered consent model that this package enforces on the
client side. Drops into a [Cobra](https://github.com/spf13/cobra) CLI
with one call; works standalone for any other Go CLI.

> **Status:** pre-v0.1.0. The taxonomy field list and exact wire values
> for `cli.analytics.tier` mirror what the server-side allowlist is
> expected to look like, but have not yet been checked against that
> file directly (see `TODO(schema)` comments in `tier.go` and
> `taxonomy.go`). Treat the public API shape as stable, the exact wire
> values as provisional until that's confirmed.

## Install

```bash
go get github.com/getargvio/argvio-go
```

## Quickstart: Cobra

```go
package main

import (
	"context"
	"os"
	"time"

	"github.com/getargvio/argvio-go/cobrasdk"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "mycli",
		Version: "1.4.0",
	}
	// ... add subcommands as usual ...

	client, err := cobrasdk.Instrument(rootCmd, os.Getenv("ARGVIO_API_KEY"))
	if err != nil {
		// client is still safe to use — it degraded to a no-op. Log if you want.
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.Shutdown(ctx)
	}()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
```

That one `cobrasdk.Instrument` call wires up automatic capture of
command path, flags used (names only), exit code, latency, help usage,
and classified errors across the whole command tree — see
[docs/cobra-integration.md](docs/cobra-integration.md) for exactly what
it hooks and how to compose it with your own `PersistentPreRunE`/
`HelpFunc` if you already have one.

## Quickstart: non-Cobra

```go
client, err := argvio.New(apiKey, "mycli", "1.4.0")
if err != nil {
	// still safe to use, degraded to no-op
}
defer client.Shutdown(context.Background())

client.RecordSessionStart(ctx)
defer client.RecordSessionEnd(ctx)

start := time.Now()
err = runCommand()
client.RecordLatency(ctx, "mycli run", time.Since(start))
code := 0
if err != nil {
	code = 1
}
client.RecordExitCode(ctx, "mycli run", code)
```

## Consent tiers

Every signal this SDK emits carries a `cli.analytics.tier` attribute
and is shaped by the tier the current session resolved to:

| Tier | Adds |
|---|---|
| `Anonymous` (default) | aggregate invocation/session/exit counts only, no identifiers |
| `Basic` | command path, flag names, exit code w/ path, latency w/ path |
| `Full` | session correlation ID, classified error signatures |
| `Opt-in+` | raw error messages and stack traces, via `Client.OptIn()` only |

The SDK enforces this client-side — it is structurally difficult to
call a lower-tier function with a higher-tier field (e.g. `RecordError`
has no parameter for a stack trace at all; that only exists on the
`OptInScope` returned by `Client.OptIn()`, which itself only activates
at `TierOptIn`). Server-side allowlist stripping is a backstop, not the
primary mechanism. See [docs/consent.md](docs/consent.md) for the full
model, how `ConsentProvider` resolves the tier, and how to write your
own.

## Disabling telemetry

```go
client, _ := argvio.New(apiKey, "mycli", "1.4.0", argvio.WithDisabled())
```

or set `ARGVIO_DISABLED=1`. Either produces a `Client` that performs
**zero network calls** and adds negligible overhead — proven by
`TestClientDisabledIsZeroNetworkCalls` and `BenchmarkDisabledClientCommand`
in this repo, not just claimed.

The `DO_NOT_TRACK` environment variable ([convention](https://do-not-track.dev))
is also honored automatically: if set to a truthy value, the client
disables itself the same way. Running in CI (common CI env vars
detected automatically) caps the default tier at `Anonymous` rather
than disabling outright — see `EnvConsentProvider`'s doc comment for
the reasoning.

## Performance

Telemetry is dispatched to a bounded background queue and never blocks
command execution; on overflow, records are dropped and counted
in-process (`Client.Stats().QueueOverflowDropped`), never applying
backpressure to the host CLI. Measured on the CI runner (see
`bench_test.go`):

```
BenchmarkUninstrumentedCommand   48 ns/op      0 B/op   0 allocs/op
BenchmarkInstrumentedCommand   1426 ns/op   2079 B/op  19 allocs/op   (3 Record* calls)
BenchmarkDisabledClientCommand   83 ns/op      0 B/op   0 allocs/op
```

That's roughly **475ns per `Record*` call** synchronously, well under
this SDK's target budget of 10µs/call — the actual network export
happens asynchronously on a background goroutine and isn't part of that
number.

## Reliability

No exported function panics under any input (nil client, network
failure, malformed consent config, unreachable endpoint) — see
`TestNilClientMethodsNeverPanic` and the fuzz-adjacent tests in
`consent_test.go`. Every network path respects `ctx` cancellation and
has an overridable timeout.

## Documentation

- [docs/consent.md](docs/consent.md) — the tier model in depth
- [docs/cobra-integration.md](docs/cobra-integration.md) — what
  `cobrasdk` captures automatically, what still needs a manual call,
  hook-chaining, and a worked multi-command example
- [CONTRIBUTING.md](CONTRIBUTING.md) — ground rules for changes here
  (this SDK is embedded in many vendors' CLIs; treat it accordingly)

## License

MIT — see [LICENSE](LICENSE).
