# Cobra integration

`cobrasdk` wires automatic telemetry capture into a
[Cobra](https://github.com/spf13/cobra) command tree. This document
covers what it captures automatically, what it deliberately doesn't,
the hook-chaining pattern it uses to compose with hooks you've already
installed, and a worked multi-command example.

## The one-liner

```go
client, err := cobrasdk.Instrument(rootCmd, apiKey)
```

This builds an `*argvio.Client` — using `rootCmd.Name()` and
`rootCmd.Version` for the CLI name/version, so you don't declare them a
second time — and calls `InstrumentCobra(rootCmd, client)` to wire up
capture. `argvio.Option`s pass through:

```go
client, err := cobrasdk.Instrument(rootCmd, apiKey,
	argvio.WithConsentProvider(myProvider),
	argvio.WithEndpoint("ingest.internal:4317"),
)
```

If you already construct your `*argvio.Client` some other way (shared
across multiple command trees, or you need an `argvio.Option`
`Instrument` doesn't expose a path for — there isn't one, it's just
`New` underneath), call the two pieces separately:

```go
client, err := argvio.New(apiKey, "mycli", version, opts...)
cobrasdk.InstrumentCobra(rootCmd, client)
```

## Why this isn't just `PersistentPreRunE`/`PersistentPostRunE`

This is worth understanding before you customize anything, because it
explains why `InstrumentCobra` touches more than just the persistent
hooks.

Cobra's internal `execute()` has two short-circuits that matter here:

1. **`--help` never reaches any hook.** The help check happens before
   `PersistentPreRunE`, `RunE`, or `PersistentPostRunE` — Cobra returns
   early. A `PersistentPreRunE`-only integration would never see help
   invocations at all.
2. **`PersistentPostRunE` does not run when `RunE` returns an error.**
   Cobra's `execute()` returns immediately on a `RunE` error, skipping
   `PostRunE` and `PersistentPostRunE` entirely. A
   `PersistentPostRunE`-only integration would silently miss every
   failing command — exactly the case telemetry most needs to see.

So `InstrumentCobra` uses three different extension points, each
chosen because it's the one place in Cobra's execution path guaranteed
to fire exactly when it should:

| Capture | Extension point | Fires when |
|---|---|---|
| Command path, flag names | `PersistentPreRunE` (chained) | Command resolved and flags parsed, before `RunE` |
| Exit code, latency, classified error | `RunE` wrapper, installed recursively across the whole tree | `RunE` (or `Run`) actually ran, regardless of its result |
| Help usage | `Command.SetHelpFunc` (chained) | `-h`/`--help` requested |

The `RunE` wrapping happens once, recursively, at
`InstrumentCobra`-call time — subcommands added to the tree
*afterward* are not wrapped. Call `InstrumentCobra` after your full
command tree is assembled (after all `AddCommand` calls), not before.

## What's captured automatically vs. what needs a manual call

Automatic:
- `RecordCommandInvocation` — command path + flag names, on every
  resolved, runnable command.
- `RecordExitCode` — `0` on success, `1` on any `RunE` error.
- `RecordLatency` — wall-clock time between `PersistentPreRunE` and the
  command's `RunE` returning.
- `RecordError` — fires when `RunE` returns a non-nil error, classified
  via `WithErrorClassifier` (default: `ErrorCategoryUnknown`).
- `RecordHelpFlagUsage` — on any help invocation.

Still manual:
- `RecordSessionStart`/`RecordSessionEnd` — a "session" here is your
  process invocation, which Cobra has no concept of. Call these
  yourself in `main()`, before/after `rootCmd.Execute()`.
- The **real OS exit code** your `main()` eventually chooses. Cobra's
  command tree never sees it — `RunE` returning an error just means
  "failed," not *which* exit code your `main()` will pass to
  `os.Exit()`. `InstrumentCobra` records `0`/`1` as a best-effort
  proxy. If your CLI uses meaningful custom exit codes, use
  `WithoutRunCapture()` and call `client.RecordExitCode` yourself after
  `Execute()` returns, with the real code.
- Raw error detail (`Client.OptIn()`) — never automatic at any tier, by
  design (see [consent.md](consent.md)). If you want opt-in-tier users
  to get detailed error capture, call it yourself, typically from
  `WithErrorClassifier`'s call site or right after `Execute()` returns.

## Hook chaining

`InstrumentCobra` never discards a hook you already installed:

- If `root.PersistentPreRunE` (or the non-`E` `PersistentPreRun`) is
  already set, `InstrumentCobra` installs a replacement that records
  the invocation *first*, then calls your original hook and returns its
  result. This ordering is deliberate: the invocation is recorded even
  if your own pre-run later fails validation and returns an error.
- If `root.HelpFunc()` already returns a non-default function (i.e. you
  called `SetHelpFunc` yourself), `InstrumentCobra` records help usage
  first, then calls your original function.

If you'd rather wire capture into your *own* existing
`PersistentPreRunE` manually instead of letting `InstrumentCobra`
install its own, call the lower-level `Client` methods directly:

```go
rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
	client.RecordCommandInvocation(cmd.Context(), cmd.CommandPath(), changedFlagNames(cmd))
	return myOwnPreRunLogic(cmd, args)
}
```

(`InstrumentCobra`'s flag-name extraction just walks
`cmd.Flags().Visit`, collecting `f.Name` for every flag with `Changed
== true` — nothing InstrumentCobra-specific about it.)

## Options

```go
cobrasdk.InstrumentCobra(rootCmd, client,
	cobrasdk.WithErrorClassifier(classify),
	cobrasdk.WithoutHelpCapture(),
	cobrasdk.WithoutRunCapture(),
)
```

- **`WithErrorClassifier(func(error) argvio.ErrorCategory)`** — maps a
  `RunE` error to a category. Without it, every error is recorded as
  `ErrorCategoryUnknown`. This SDK deliberately doesn't try to guess a
  category from an error's message or type (see the root module's
  PII-scrubbing non-goal) — only your own error handling knows which of
  your errors are user mistakes vs. network failures vs. internal bugs.
- **`WithoutHelpCapture()`** — skip the `SetHelpFunc` wiring, if you
  have your own help instrumentation you don't want touched.
- **`WithoutRunCapture()`** — skip the recursive `RunE` wrapping, if you
  want to call `RecordExitCode`/`RecordLatency`/`RecordError` yourself
  (e.g. to record the real OS exit code — see above).

## A known limitation: persistent hooks on subcommands

Cobra only runs the *first* `PersistentPreRunE` it finds walking from
the executing command up to root (unless
`cobra.EnableTraverseRunHooks` is set). If a subcommand defines its own
`PersistentPreRunE`, it shadows the one `InstrumentCobra` installed on
root, and `RecordCommandInvocation` won't fire for that subcommand's
invocations. This is standard Cobra behavior, not specific to this SDK
— the general fix is `cobra.EnableTraverseRunHooks = true`, which makes
Cobra run every persistent hook from root to leaf instead of only the
first one it finds.

## Worked example

```go
package main

import (
	"context"
	"errors"
	"os"
	"time"

	argvio "github.com/getargvio/argvio-go"
	"github.com/getargvio/argvio-go/cobrasdk"
	"github.com/spf13/cobra"
)

// errDeployValidation is your own sentinel error from runDeploy's
// validation step — this SDK never inspects error content itself.
var errDeployValidation = errors.New("invalid deploy configuration")

func classifyDeployError(err error) argvio.ErrorCategory {
	if errors.Is(err, errDeployValidation) {
		return argvio.ErrorCategoryValidation
	}
	return argvio.ErrorCategoryUnknown
}

func main() {
	rootCmd := &cobra.Command{Use: "mycli", Version: "2.0.0"}

	deployCmd := &cobra.Command{
		Use: "deploy",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd)
		},
	}
	deployCmd.Flags().Bool("dry-run", false, "simulate without applying")
	rootCmd.AddCommand(deployCmd)

	statusCmd := &cobra.Command{
		Use: "status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd)
		},
	}
	rootCmd.AddCommand(statusCmd)

	// Assemble the whole tree BEFORE instrumenting. Instrument's opts are
	// argvio.Option (endpoint, consent provider, ...); cobrasdk.HookOption
	// (like WithErrorClassifier below) needs the two-step form instead:
	client, err := argvio.New(os.Getenv("MYCLI_ARGVIO_KEY"), rootCmd.Name(), rootCmd.Version)
	cobrasdk.InstrumentCobra(rootCmd, client, cobrasdk.WithErrorClassifier(classifyDeployError))
	_ = err // client is safe to use even if err != nil

	ctx := context.Background()
	client.RecordSessionStart(ctx)
	defer func() {
		client.RecordSessionEnd(ctx)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.Shutdown(shutdownCtx)
	}()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
```

`deployCmd`'s `--dry-run` flag, if set, is captured by name only
(`RecordCommandInvocation`'s `AttrCommandFlags` will include
`"dry-run"`) — never its value, which for `--dry-run` happens to be a
bool anyway, but the same rule applies uniformly to every flag type.
