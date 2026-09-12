# Consent model

This document describes the four-tier consent model from this SDK's
perspective: what each tier unlocks, how the tier for a session gets
resolved, and how to plug in your own storage for the user's choice.

> **Schema status:** the tier→field mapping below mirrors what this SDK
> was scaffolded against. It has not yet been checked line-by-line
> against the server-side allowlist file (`public`'s source of truth).
> Search this repo for `TODO(schema)` before relying on exact wire
> values in a production integration; the shape of the API (which
> function requires which tier) is stable regardless.

## The four tiers

```go
const (
	TierAnonymous Tier = iota // default
	TierBasic
	TierFull
	TierOptIn
)
```

Tiers are strictly ordered — each is a superset of the tier below it.
`Tier.Allows(required)` is how the SDK (and you, if you want) checks
this: `TierFull.Allows(TierBasic) == true`, `TierBasic.Allows(TierFull)
== false`.

### TierAnonymous (default)

No persistent or cross-invocation identifiers, no command path, no flag
names. Every taxonomy function still emits *something* at this tier —
an aggregate counter increment — so vendors get install/usage volume
numbers even from users who've never made a choice.

| Function | What's emitted at Anonymous |
|---|---|
| `RecordSessionStart`/`End` | `cli.sessions` counter increment only |
| `RecordCommandInvocation` | `cli.invocations` counter increment only |
| `RecordExitCode` | exit code, no command path |
| `RecordLatency` | duration, no command path |
| `RecordHelpFlagUsage` | a bare "help was used" event, no command path |
| `RecordError` | **nothing** — dropped, counted in `Stats().BelowTierDropped` |

### TierBasic

Adds command path (`cmd.CommandPath()`, e.g. `"mycli sub subsub"`) and
flag *names* (never values — see below) to `RecordCommandInvocation`,
and adds the command path attribute to `RecordExitCode`,
`RecordLatency`, and `RecordHelpFlagUsage`.

### TierFull

Adds:
- A per-process session correlation ID (`AttrSessionID`) on
  `RecordSessionStart`/`RecordSessionEnd` — a random value generated
  once per `Client`, not a user or device identifier, and not
  persisted anywhere.
- `RecordError` starts actually emitting: category (`ErrorCategory`,
  e.g. `ErrorCategoryNetwork`) plus command path. Never a message or
  stack trace — see TierOptIn.

### TierOptIn

Raw error detail: message and, optionally, stack trace. This is
**only** reachable through `Client.OptIn()`:

```go
if scope, ok := client.OptIn(); ok {
    scope.RecordErrorDetail(ctx, cmd.CommandPath(), err)
    // or, to also attach a stack trace:
    scope.RecordErrorDetailWithStack(ctx, cmd.CommandPath(), err, debug.Stack())
}
```

`OptIn()` returns `ok == false` at every other tier, and the
`*OptInScope` it returns in that case is a safe, inert no-op — there is
no other function anywhere in this package that accepts a raw error
message or stack trace. This is deliberate: it should be structurally
impossible for a `RecordError` call site written against a lower tier
to accidentally leak detail, not just a runtime check.

## What's never captured, at any tier

**Flag values.** `RecordCommandInvocation` only ever accepts flag
*names* (`[]string`). There is no tier, no option, and no builder
anywhere in this SDK that accepts flag values. Flag values are
arbitrary free-form input a user typed — potentially secrets, paths, or
anything else — and this package doesn't try to be clever about
scrubbing that (see the root task's "no automatic PII scrubbing"
non-goal). If you need to understand *how* a flag was used, capture
that yourself via `RecordError`'s category or your own out-of-band
logging, scoped to your own judgment about what's safe.

## How the tier gets resolved

`argvio.New` resolves one tier for the whole `Client` (and therefore
the whole process — CLI invocations are short-lived, so consent isn't
expected to change mid-command):

1. If a `ConsentProvider` was supplied via `argvio.WithConsentProvider`,
   its `Resolve` is called once (with an internal timeout). If it
   succeeds, that tier is used.
2. Otherwise (no provider, or the provider errored/timed out), the tier
   from `argvio.WithDefaultTier` is used — `TierAnonymous` if that
   wasn't set either.

Once resolved, the tier is fixed for the `Client`'s lifetime. There is
no code path that re-escalates it later, including from inside
`Record*` calls — every call re-checks the resolved tier against what
it's trying to emit and drops (never upgrades) on mismatch.

## `ConsentProvider`

```go
type ConsentProvider interface {
	Resolve(ctx context.Context) (Tier, error)
}
```

Implementations must not panic (not enforced by the interface, but
`Client` construction recovers around every call, so a panicking
provider degrades to `TierAnonymous` rather than crashing the host CLI)
and should not block indefinitely.

### Composing providers

`ChainProviders(providers...)` tries each in order and uses the first
one that resolves without error — useful for "env override, then
stored file, then anonymous default":

```go
provider := argvio.ChainProviders(
	argvio.EnvConsentProvider{},                          // caps at Anonymous in CI
	argvio.NewFileConsentProvider("mycli"),                // the user's stored choice
	argvio.StaticProvider{Tier: argvio.TierAnonymous},     // fail-safe default
)
client, err := argvio.New(apiKey, "mycli", version,
	argvio.WithConsentProvider(provider),
)
```

### The reference file-based provider

`FileConsentProvider` stores the chosen tier as JSON
(`{"tier":"basic"}`) at a conventional per-CLI path:
`filepath.Join(os.UserConfigDir(), cliName, "argvio-consent.json")` —
which resolves to `$XDG_CONFIG_HOME` (or `~/.config`) on Linux,
`~/Library/Application Support` on macOS, and `%AppData%` on Windows,
via the standard library's `os.UserConfigDir`. Namespacing under your
CLI's own name avoids collisions with other CLIs that also embed this
SDK.

Use it to both read and persist a user's choice, e.g. from a first-run
prompt:

```go
fp := argvio.NewFileConsentProvider("mycli")
tier := promptUserForTier() // your own UX
_ = fp.Store(tier)
```

A missing, unreadable, or malformed file resolves as an error (never a
panic), so always place `FileConsentProvider` inside `ChainProviders`
with a fallback, or accept that `argvio.New` will fall back to
`WithDefaultTier` on its own.

### `DO_NOT_TRACK` and CI

These are handled outside `ConsentProvider` entirely, in `argvio.New`
itself:

- `DO_NOT_TRACK` (see [do-not-track.dev](https://do-not-track.dev)),
  when set to a truthy value, forces the client into full disabled
  mode — zero network calls — the same as `WithDisabled()`. This is a
  standing, explicit opt-out signal a user or environment sets
  deliberately, so it's honored as "no telemetry," not "least
  telemetry."
- CI environments (detected via common CI provider env vars — see
  `IsCI()`) are **not** disabled by default, but `EnvConsentProvider`
  caps the tier at `Anonymous` when placed in your provider chain.
  Reasoning: CI runs are unattended and don't represent any single
  human's consent choice, but aggregate install/build telemetry from CI
  still has real product value and carries no identifying fields at
  `TierAnonymous` by construction. If you want CI fully silent instead,
  check `argvio.IsCI()` yourself and pass `WithDisabled()`.

### Writing your own `ConsentProvider`

Anything that can produce a `Tier` from `context.Context` works —
reading a flag your CLI already parses, calling out to an OS keychain,
prompting interactively on first run, or looking at an enterprise
policy file. Keep `Resolve` fast and non-blocking-ish (it runs once,
with an internal timeout, during `argvio.New`) and let it return an
error rather than panic when it can't determine a tier.
