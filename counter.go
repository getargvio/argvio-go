package argvio

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// CounterOption configures RecordCounter.
type CounterOption func(*counterConfig)

type counterConfig struct {
	label string
}

// WithLabel splits a counter by a second dimension (for example the HTTP
// verb a rule was generated for). It is subject to the same rules as the
// counter's name; see RecordCounter.
func WithLabel(label string) CounterOption {
	return func(c *counterConfig) { c.label = label }
}

// RecordCounter adds n to a vendor-defined product counter, for things the
// built-in metrics don't cover ("security schemes seen, by type", "rules
// generated, by verb"). Counters are summed per name, label and command, and
// one data point per combination is sent when the client flushes, so calling
// this in a hot loop does not produce one record per call.
//
// # What is sent, and at which tier
//
// The cli.counter metric, with name (AttrCounterName), the optional label
// (AttrCounterLabel), the name of the command last passed to
// RecordCommandInvocation when there was one, and the CLI version, OS,
// architecture and CI flag every metric carries. It carries no session or
// install ID. Counters are accepted at EVERY tier, including TierAnonymous,
// so they are also reported from CI and when the user has granted no consent.
//
// # WARNING: names and labels are sent as-is
//
// Because counters are accepted at the anonymous tier, name and label must
// be fixed identifiers chosen in code ("openapi.scheme", "bearer"). Never
// pass user input, file paths, host names, URLs, tokens, or anything derived
// from them. This SDK does not scrub them: it only drops a name or label
// that doesn't match ^[a-z0-9][a-z0-9._-]*$ or is longer than 64 bytes. A
// counter with an invalid name is dropped; one with an invalid label is
// dropped too, rather than being sent without it, so a bad label can't
// silently merge into the unlabelled counter. An empty label is the same as
// no label.
//
// A non-positive n is ignored. Safe to call on a nil or disabled Client, and
// it never blocks or panics into the host CLI.
func (c *Client) RecordCounter(ctx context.Context, name string, n int64, opts ...CounterOption) {
	defer recoverGuard(nil)
	if n <= 0 || !validIdentifier(name) || !c.gate(TierAnonymous) || c.counter == nil {
		return
	}
	var cfg counterConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	attrs := make([]attribute.KeyValue, 0, 4)
	attrs = append(attrs, c.tierAttr(), attribute.String(AttrCounterName, name))
	if cfg.label != "" {
		if !validIdentifier(cfg.label) {
			return
		}
		attrs = append(attrs, attribute.String(AttrCounterLabel, cfg.label))
	}
	if p := c.currentCommand.Load(); p != nil {
		attrs = append(attrs, attribute.String(AttrCommandName, *p))
	}
	// The instrument sums in memory and exports one data point per attribute
	// set, so this is cheap and never touches the job queue.
	c.counter.Add(ctx, n, metric.WithAttributes(attrs...))
}
