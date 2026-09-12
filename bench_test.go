package argvio

import (
	"context"
	"testing"
	"time"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

// simulatedCommandWork stands in for a CLI command's actual work.
// Chosen small (~1µs) so the benchmark measures this SDK's overhead as
// a fraction of a fast command, not a slow one where any overhead would
// be lost in the noise.
func simulatedCommandWork() {
	sum := 0
	for i := 0; i < 200; i++ {
		sum += i
	}
	_ = sum
}

// BenchmarkUninstrumentedCommand is the baseline: command work with no
// telemetry at all.
func BenchmarkUninstrumentedCommand(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		simulatedCommandWork()
	}
}

// BenchmarkInstrumentedCommand measures the same simulated command work
// with a full three-call taxonomy sequence (invocation, latency, exit
// code) against a live (in-memory) Client and exporter pipeline.
//
// Target budget: the per-call overhead this adds to a command's
// synchronous execution path (i.e. everything before Record* returns
// control to the caller — it does not include the asynchronous
// network export, which happens on the background worker goroutine
// after the caller has already moved on) should stay under 10
// microseconds per Record* call on typical hardware. See the
// BenchmarkInstrumentedCommand vs BenchmarkUninstrumentedCommand ns/op
// delta when running `go test -bench=Command -benchtime=200000x`.
func BenchmarkInstrumentedCommand(b *testing.B) {
	srv, err := fakeotlp.Start()
	if err != nil {
		b.Fatal(err)
	}
	defer srv.Close()

	c, err := New("bench-key", "benchcli", "1.0.0",
		WithEndpoint(srv.Addr()),
		WithInsecure(),
		WithDefaultTier(TierFull),
	)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	}()

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.RecordCommandInvocation(ctx, "benchcli run", nil)
		simulatedCommandWork()
		c.RecordLatency(ctx, "benchcli run", time.Microsecond)
		c.RecordExitCode(ctx, "benchcli run", 0)
	}
}

// BenchmarkDisabledClientCommand measures the near-zero overhead a
// vendor pays when telemetry is fully disabled (WithDisabled, or
// DO_NOT_TRACK) — every Record* call should be a handful of cheap
// checks (nil/disabled/shutdown) with no allocation and no dispatch.
func BenchmarkDisabledClientCommand(b *testing.B) {
	c, err := New("bench-key", "benchcli", "1.0.0", WithDisabled())
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.RecordCommandInvocation(ctx, "benchcli run", nil)
		simulatedCommandWork()
		c.RecordLatency(ctx, "benchcli run", time.Microsecond)
		c.RecordExitCode(ctx, "benchcli run", 0)
	}
}
