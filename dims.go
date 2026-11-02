package argvio

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MaxDims is how many dimensions a Counter or Duration handle may declare.
// The server rejects data points with more than its public.max_metric_dims
// (3 by default), so a handle can't declare more than that default.
const MaxDims = 3

// MetricOption configures a Counter or Duration handle.
type MetricOption func(*metricConfig)

type metricConfig struct {
	dims []string
	unit Unit
}

// Dims declares the dimension keys of a Counter or Duration handle, in the
// order its Add or Record calls pass their values: Dims("severity", "check")
// then Add(ctx, 1, "high", "bola"). Each key is sent as a cli.dim.<key>
// attribute, so it follows the same rules as a counter name: a fixed
// identifier matching ^[a-z0-9][a-z0-9._-]*$, at most 64 bytes. A handle
// with more than MaxDims keys, an invalid key or a repeated one drops every
// call.
func Dims(keys ...string) MetricOption {
	return func(c *metricConfig) { c.dims = append([]string(nil), keys...) }
}

// metricHandle is what Counter and Duration share: a name and dimension keys
// validated once, when the handle is made.
type metricHandle struct {
	c     *Client
	name  string
	keys  []attribute.Key
	valid bool
}

func newMetricHandle(c *Client, name string, opts []MetricOption) metricHandle {
	var cfg metricConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	h := metricHandle{c: c, name: name, valid: validIdentifier(name) && len(cfg.dims) <= MaxDims}
	seen := make(map[string]bool, len(cfg.dims))
	for _, k := range cfg.dims {
		if !validIdentifier(k) || seen[k] {
			h.valid = false
		}
		seen[k] = true
		h.keys = append(h.keys, attribute.Key(AttrDimPrefix+k))
	}
	return h
}

// attrs builds a call's attributes: the tier, the name under nameAttr, one
// cli.dim.<key> per declared key and the current command. ok is false when the
// call must be dropped: an invalid handle, a value count that doesn't match
// the keys, or an invalid value. A value is never sent as something else, so
// a bad one can't silently merge into another series.
func (h metricHandle) attrs(nameAttr string, values []string) (attrs []attribute.KeyValue, ok bool) {
	if !h.valid || len(values) != len(h.keys) {
		return nil, false
	}
	attrs = make([]attribute.KeyValue, 0, 3+len(h.keys))
	attrs = append(attrs, h.c.tierAttr(), attribute.String(nameAttr, h.name))
	for i, v := range values {
		if !validIdentifier(v) {
			return nil, false
		}
		attrs = append(attrs, h.keys[i].String(v))
	}
	if p := h.c.currentCommand.Load(); p != nil {
		attrs = append(attrs, attribute.String(AttrCommandName, *p))
	}
	attrs = append(attrs, h.c.metricInstallAttrs()...)
	return attrs, true
}

// Counter is a vendor-defined counter with named dimensions, made once by
// Client.Counter and used for every count. Safe for concurrent use; a nil
// *Counter, or one from a nil or disabled Client, does nothing.
type Counter struct {
	h metricHandle
}

// Counter returns a handle on the vendor counter name, for counts split by
// up to MaxDims named dimensions ("vulnerabilities found, by severity and
// check"), which RecordCounter's single label can't express:
//
//	vulns := client.Counter("vulns.found", argvio.Dims("severity", "check"))
//	vulns.Add(ctx, 1, "high", "bola")
//
// Counts are summed in memory per name, dimension values and command, and
// sent as the cli.counter metric when the client flushes, exactly like
// RecordCounter: at EVERY tier, including TierAnonymous, so from CI and from
// users who granted no consent too.
//
// # WARNING: names, keys and values are sent as-is
//
// They must be fixed identifiers chosen in code (a severity, a check id),
// never user input, file paths, host names, URLs, tokens, or anything derived
// from them. This SDK does not scrub them: it only drops a call whose name,
// key or value doesn't match ^[a-z0-9][a-z0-9._-]*$ or is longer than 64
// bytes.
func (c *Client) Counter(name string, opts ...MetricOption) *Counter {
	return &Counter{h: newMetricHandle(c, name, opts)}
}

// Add adds n to the counter, with one value per dimension key declared with
// Dims, in the same order. A call with a non-positive n, the wrong number of
// values or an invalid one is dropped. Never blocks or panics into the host
// CLI.
func (k *Counter) Add(ctx context.Context, n int64, values ...string) {
	defer recoverGuard(nil)
	if k == nil || k.h.c == nil || n <= 0 || !k.h.c.gate(TierAnonymous) || k.h.c.counter == nil {
		return
	}
	attrs, ok := k.h.attrs(AttrCounterName, values)
	if !ok {
		return
	}
	k.h.c.counter.Add(ctx, n, metric.WithAttributes(attrs...))
}

// Duration is a vendor-defined duration with named dimensions, made once by
// Client.Duration. Safe for concurrent use; a nil *Duration, or one from a nil
// or disabled Client, does nothing.
type Duration struct {
	h metricHandle
}

// Duration returns a handle on the vendor duration name, for timings split
// by up to MaxDims named dimensions:
//
//	scanTime := client.Duration("scan.run", argvio.Dims("target"))
//	scanTime.Record(ctx, time.Since(start), "openapi")
//
// Durations are aggregated in memory into the cli.measure histogram per name,
// dimension values and command, exactly like RecordDuration, and accepted at
// EVERY tier including TierAnonymous. The same warning as Client.Counter
// applies: names, keys and values must be fixed identifiers from code.
func (c *Client) Duration(name string, opts ...MetricOption) *Duration {
	return &Duration{h: newMetricHandle(c, name, opts)}
}

// Record records d, with one value per dimension key declared with Dims, in
// the same order. A call with a non-positive d, the wrong number of values or
// an invalid one is dropped. Never blocks or panics into the host CLI.
func (m *Duration) Record(ctx context.Context, d time.Duration, values ...string) {
	defer recoverGuard(nil)
	if m == nil || m.h.c == nil || d <= 0 || !m.h.c.gate(TierAnonymous) || m.h.c.measure == nil {
		return
	}
	attrs, ok := m.h.attrs(AttrMeasureName, values)
	if !ok {
		return
	}
	m.h.c.measure.Record(ctx, float64(d)/float64(time.Millisecond), metric.WithAttributes(attrs...))
}
