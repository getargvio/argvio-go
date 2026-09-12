package argvio

import (
	"fmt"
	"os"
	"sync/atomic"
)

// argvioDebug enables printing recovered-panic diagnostics to stderr.
// Off by default: a vendor's CLI should never see this SDK's internals
// unless they've opted into debugging it.
var argvioDebug = os.Getenv("ARGVIO_DEBUG") == "1"

// droppedCounters tracks in-process (never network) counts of records
// dropped for various reasons. Exposed via Client.Stats for vendors who
// want to surface it in their own diagnostics/verbose output.
type droppedCounters struct {
	queueOverflow  atomic.Int64
	belowTierCeil  atomic.Int64
	recoveredPanic atomic.Int64
}

// Stats is a snapshot of in-process telemetry-about-telemetry: never
// sent over the network, provided so a vendor can optionally surface it
// (e.g. behind a --verbose flag) for their own debugging.
type Stats struct {
	// QueueOverflowDropped counts records dropped because a batch queue
	// was full rather than blocking the host CLI.
	QueueOverflowDropped int64
	// BelowTierDropped counts records/fields dropped because the
	// resolved consent tier didn't permit them.
	BelowTierDropped int64
	// RecoveredPanics counts internal panics recovered from, which
	// should always be zero; a nonzero value indicates a bug in this
	// SDK, not in the host CLI.
	RecoveredPanics int64
}

func (d *droppedCounters) snapshot() Stats {
	if d == nil {
		return Stats{}
	}
	return Stats{
		QueueOverflowDropped: d.queueOverflow.Load(),
		BelowTierDropped:     d.belowTierCeil.Load(),
		RecoveredPanics:      d.recoveredPanic.Load(),
	}
}

// recoverGuard is deferred at the top of every exported method to
// guarantee no panic ever escapes into the host CLI. If counters is
// non-nil, the recovery is tallied there.
func recoverGuard(counters *droppedCounters) {
	if r := recover(); r != nil {
		if counters != nil {
			counters.recoveredPanic.Add(1)
		}
		if argvioDebug {
			msg := "(non-error panic value)"
			if err, ok := r.(error); ok {
				msg = err.Error()
			}
			fmt.Fprintf(os.Stderr, "argvio: recovered internal panic: %s\n", msg)
		}
	}
}
