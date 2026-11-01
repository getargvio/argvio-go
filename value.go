package argvio

import (
	"context"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// valueBuckets are the explicit histogram bucket boundaries of cli.value.
// The server accepts these and no others, so that histograms from every SDK
// add up. The first bucket, (-inf, 0], counts the runs that recorded
// nothing: that is what the server's hit rate is computed from.
var valueBuckets = []float64{0, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 5000, 10000, 100000}

// Unit is what a Value counts.
type Unit string

// Units a Value may have. Time goes to Client.Duration instead.
const (
	UnitCount   Unit = "1"
	UnitBytes   Unit = "By"
	UnitPercent Unit = "%"
)

// WithUnit sets what a Value counts; UnitCount by default. Counter and
// Duration handles ignore it.
func WithUnit(u Unit) MetricOption {
	return func(c *metricConfig) { c.unit = u }
}

// Value is a vendor-defined per-run quantity with named dimensions, made once
// by Client.Value. Safe for concurrent use; a nil *Value, or one from a nil or
// disabled Client, does nothing.
type Value struct {
	h    metricHandle
	unit Unit
}

// Value returns a handle on the vendor value name: a quantity recorded once
// per run, such as findings per scan, operations scanned or spec size,
// which a counter's running total loses. The server keeps its distribution:
// percentiles, mean, and the hit rate, the share of runs that recorded more
// than zero.
//
//	perScan := client.Value("scan.findings", argvio.Dims("severity"))
//	for _, sev := range severities {
//		perScan.Record(ctx, float64(report.Count(sev)), sev) // zeros too
//	}
//
// # Record zeros
//
// Record a value for every run, including 0. A run that records nothing
// isn't counted at all, so the hit rate and the percentiles would only
// describe the runs that found something.
//
// # What is sent, and at which tier
//
// The cli.value histogram, with the name (AttrValueName), its unit
// (AttrValueUnit), one cli.dim.<key> per dimension, the current command,
// and the CLI version, OS, architecture and CI flag every metric carries.
// Accepted at EVERY tier, including TierAnonymous, with the same warning as
// Client.Counter: names, keys and values must be fixed identifiers from
// code, never user input.
func (c *Client) Value(name string, opts ...MetricOption) *Value {
	var cfg metricConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	h := newMetricHandle(c, name, opts)
	switch cfg.unit {
	case "":
		cfg.unit = UnitCount
	case UnitCount, UnitBytes, UnitPercent:
	default:
		h.valid = false
	}
	return &Value{h: h, unit: cfg.unit}
}

// Record records v for one run, with one value per dimension key declared
// with Dims, in the same order. v must be finite and not negative; zero is
// valid and expected. A call with an invalid v, the wrong number of values
// or an invalid one is dropped. Never blocks or panics into the host CLI.
func (m *Value) Record(ctx context.Context, v float64, values ...string) {
	defer recoverGuard(nil)
	if m == nil || m.h.c == nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || !m.h.c.gate(TierAnonymous) || m.h.c.value == nil {
		return
	}
	attrs, ok := m.h.attrs(AttrValueName, values)
	if !ok {
		return
	}
	attrs = append(attrs, attribute.String(AttrValueUnit, string(m.unit)))
	m.h.c.value.Record(ctx, v, metric.WithAttributes(attrs...))
}
