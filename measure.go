package argvio

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// measureBucketsMs are the explicit histogram bucket boundaries of
// cli.measure, in milliseconds. The server expects the same boundaries from
// every SDK so that histograms from different languages can be merged.
var measureBucketsMs = []float64{1, 5, 10, 50, 100, 500, 1000, 5000, 10000, 60000, 300000}

// MeasureOption configures RecordDuration.
type MeasureOption func(*measureConfig)

type measureConfig struct {
	label string
}

// WithMeasureLabel splits a measure by a second dimension (for example the
// external tool a step ran). It is subject to the same rules as the measure's
// name; see RecordDuration.
func WithMeasureLabel(label string) MeasureOption {
	return func(c *measureConfig) { c.label = label }
}

// RecordDuration records how long something took, for timings the built-in
// metrics don't cover ("external tool run time", "scan time, by target
// type"). Durations are aggregated per name, label and command into a
// histogram, and one data point per combination is sent when the client
// flushes, so calling this in a hot loop does not produce one record per
// call.
//
// # What is sent, and at which tier
//
// The cli.measure histogram (milliseconds), with name (AttrMeasureName), the
// optional label (AttrMeasureLabel), the name of the command last passed to
// RecordCommandInvocation when there was one, and the CLI version, OS,
// architecture and CI flag every metric carries. It carries no session or
// install ID. Measures are accepted at EVERY tier, including TierAnonymous,
// so they are also reported from CI and when the user has granted no consent.
//
// # WARNING: names and labels are sent as-is
//
// Because measures are accepted at the anonymous tier, name and label must
// be fixed identifiers chosen in code ("john.run", "hmac-sha256"). Never
// pass user input, file paths, host names, URLs, tokens, or anything derived
// from them. This SDK does not scrub them: it only drops a name or label
// that doesn't match ^[a-z0-9][a-z0-9._-]*$ or is longer than 64 bytes. A
// measure with an invalid name is dropped; one with an invalid label is
// dropped too, rather than being sent without it, so a bad label can't
// silently merge into the unlabelled measure. An empty label is the same as
// no label.
//
// A non-positive d is ignored. Safe to call on a nil or disabled Client, and
// it never blocks or panics into the host CLI.
func (c *Client) RecordDuration(ctx context.Context, name string, d time.Duration, opts ...MeasureOption) {
	defer recoverGuard(nil)
	if d <= 0 || !validIdentifier(name) || !c.gate(TierAnonymous) || c.measure == nil {
		return
	}
	var cfg measureConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	attrs := make([]attribute.KeyValue, 0, 4)
	attrs = append(attrs, c.tierAttr(), attribute.String(AttrMeasureName, name))
	if cfg.label != "" {
		if !validIdentifier(cfg.label) {
			return
		}
		attrs = append(attrs, attribute.String(AttrMeasureLabel, cfg.label))
	}
	if p := c.currentCommand.Load(); p != nil {
		attrs = append(attrs, attribute.String(AttrCommandName, *p))
	}
	// The instrument aggregates in memory and exports one data point per
	// attribute set, so this is cheap and never touches the job queue.
	c.measure.Record(ctx, float64(d)/float64(time.Millisecond), metric.WithAttributes(attrs...))
}
