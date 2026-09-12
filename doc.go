// Package argvio is a client SDK for emitting CLI usage telemetry to
// Argvio's OTLP ingest server, built around a tiered consent model
// (Anonymous, Basic, Full, Opt-in+) that this package enforces on the
// client side rather than trusting server-side stripping alone.
//
// Vendors interact with it through a fixed taxonomy of Record*
// functions on Client (RecordCommandInvocation, RecordExitCode,
// RecordError, ...) instead of a free-form attribute bag, so it is
// structurally difficult to accidentally emit a field above what the
// resolved consent tier allows. See docs/consent.md in the module root
// for the full tier model and docs/cobra-integration.md for wiring this
// into a github.com/spf13/cobra CLI (the cobrasdk subpackage) with a
// single call.
//
// Every exported method on Client is safe to call on a nil receiver and
// never panics — a bug in this package should never crash a CLI that
// embeds it.
package argvio
