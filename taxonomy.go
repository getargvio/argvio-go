package argvio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
)

// This file implements the taxonomy: the fixed, typed set of Record*
// functions vendors call, as opposed to a free-form "send any attribute
// bag" API. Field-per-tier boundaries here are the SDK-side enforcement
// of the consent model; server-side allowlist stripping is a backstop,
// not the primary mechanism (see docs/consent.md).
//
// TODO(schema): the exact attribute keys and per-tier field boundaries
// below mirror what was specified when this SDK was scaffolded. They
// must be checked against the authoritative server-side allowlist file
// before v0.1.0 ships, and updated here deliberately (never inferred at
// runtime) if the schema differs. See CONTRIBUTING.md.

// Attribute keys used on emitted signals. Exported so vendors and tests
// can assert on them without relying on string literals staying in
// sync.
const (
	AttrCommandPath   = "cli.command.path"
	AttrCommandFlags  = "cli.command.flags"
	AttrExitCode      = "cli.command.exit_code"
	AttrHelpUsed      = "cli.command.help_used"
	AttrSessionID     = "cli.session.id"
	AttrErrorCategory = "cli.error.category"
	AttrErrorMessage  = "cli.error.message" // TierOptIn only
	AttrErrorStack    = "cli.error.stack"   // TierOptIn only
)

// MetricCommandDuration is the name of the histogram instrument
// RecordLatency records to (unit: milliseconds), for vendors or tests
// that want to reference it without a string literal.
const MetricCommandDuration = "cli.command.duration"

// ErrorCategory is a coarse, vendor-assigned classification of an error
// — deliberately not the error's message or type name, both of which
// can carry arbitrary (and potentially sensitive) free-form text. This
// SDK does not attempt to infer a category from an error value itself
// (see the package's "no automatic PII scrubbing" design note); the
// vendor's own error handling already knows which of these applies.
type ErrorCategory string

const (
	ErrorCategoryUnknown    ErrorCategory = "unknown"
	ErrorCategoryUser       ErrorCategory = "user" // bad input, invalid flags/args
	ErrorCategoryValidation ErrorCategory = "validation"
	ErrorCategoryNetwork    ErrorCategory = "network"
	ErrorCategoryAuth       ErrorCategory = "auth"
	ErrorCategoryInternal   ErrorCategory = "internal"
	ErrorCategoryTimeout    ErrorCategory = "timeout"
	ErrorCategoryCanceled   ErrorCategory = "canceled"
)

func (c ErrorCategory) valid() bool {
	switch c {
	case ErrorCategoryUnknown, ErrorCategoryUser, ErrorCategoryValidation,
		ErrorCategoryNetwork, ErrorCategoryAuth, ErrorCategoryInternal,
		ErrorCategoryTimeout, ErrorCategoryCanceled:
		return true
	default:
		return false
	}
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// tierAttr returns the cli.analytics.tier attribute for the resolved
// session tier, present on every emitted record/span.
func (c *Client) tierAttr() attribute.KeyValue {
	return attribute.String(ResourceAttrTier, c.tier.String())
}

// gate reports whether required is satisfied by c's resolved tier. When
// not satisfied, it counts the drop and returns false; callers should
// return immediately without emitting anything.
func (c *Client) gate(required Tier) bool {
	if !c.enabled() {
		return false
	}
	if !c.tier.Allows(required) {
		c.counters.belowTierCeil.Add(1)
		return false
	}
	return true
}

func (c *Client) emitEvent(ctx context.Context, name string, sev otellog.Severity, attrs ...attribute.KeyValue) {
	c.dispatch(func() {
		var rec otellog.Record
		rec.SetTimestamp(time.Now())
		rec.SetEventName(name)
		rec.SetSeverity(sev)
		rec.AddAttributes(c.tierAttr())
		rec.AddAttributes(attrs...)
		c.logger.Emit(ctx, rec)
	})
}

// --- Session ---------------------------------------------------------

// ensureSessionID lazily generates a session correlation ID at
// TierFull+ (see TierFull's doc comment: session correlation is a
// Full-tier capability). It is a random, non-persistent identifier
// scoped to a single process invocation — not a user or device
// identifier.
func (c *Client) ensureSessionID() string {
	if !c.tier.Allows(TierFull) {
		return ""
	}
	if p := c.sessionID.Load(); p != nil {
		return *p
	}
	id := newSessionID()
	c.sessionID.CompareAndSwap(nil, &id)
	if p := c.sessionID.Load(); p != nil {
		return *p
	}
	return id
}

// RecordSessionStart records the start of a CLI process session. Call
// once, as early as possible (e.g. in your root command's
// PersistentPreRunE, or main() for non-Cobra CLIs). Safe to call on a
// nil or disabled Client.
//
// At TierAnonymous this only increments an aggregate session counter.
// At TierBasic+ it also emits a session_start event. At TierFull+ the
// event carries a per-process session correlation ID (see AttrSessionID).
func (c *Client) RecordSessionStart(ctx context.Context) {
	defer recoverGuard(nil)
	if !c.enabled() {
		return
	}
	c.dispatch(func() {
		c.sessions.Add(ctx, 1, metric.WithAttributes(c.tierAttr()))
	})
	if !c.tier.Allows(TierBasic) {
		return
	}
	attrs := []attribute.KeyValue{}
	if id := c.ensureSessionID(); id != "" {
		attrs = append(attrs, attribute.String(AttrSessionID, id))
	}
	c.emitEvent(ctx, "cli.session_start", otellog.SeverityInfo1, attrs...)
}

// RecordSessionEnd records the end of a CLI process session. Call once,
// symmetric with RecordSessionStart (e.g. root command's
// PersistentPostRunE, or a deferred call in main()). Safe to call on a
// nil or disabled Client.
func (c *Client) RecordSessionEnd(ctx context.Context) {
	defer recoverGuard(nil)
	if !c.enabled() || !c.tier.Allows(TierBasic) {
		return
	}
	attrs := []attribute.KeyValue{}
	if id := c.ensureSessionID(); id != "" {
		attrs = append(attrs, attribute.String(AttrSessionID, id))
	}
	c.emitEvent(ctx, "cli.session_end", otellog.SeverityInfo1, attrs...)
}

// --- Command invocation ------------------------------------------------

// RecordCommandInvocation records that a command began executing.
// commandPath should be the full command path (e.g. cmd.CommandPath()
// in Cobra: "mycli sub subsub"), not just the leaf command name.
// flagNames is the list of flag *names* the user set — never flag
// values, which this SDK never accepts anywhere in the taxonomy (flag
// values are arbitrary free-form input this package deliberately does
// not try to inspect or redact; see the package's PII-scrubbing
// non-goal).
//
// At TierAnonymous this only increments an aggregate invocation
// counter with no path/flag detail. At TierBasic+ the full command path
// and flag names are included.
func (c *Client) RecordCommandInvocation(ctx context.Context, commandPath string, flagNames []string) {
	defer recoverGuard(nil)
	if !c.enabled() {
		return
	}
	c.dispatch(func() {
		c.invocations.Add(ctx, 1, metric.WithAttributes(c.tierAttr()))
	})
	if !c.tier.Allows(TierBasic) {
		return
	}
	attrs := []attribute.KeyValue{attribute.String(AttrCommandPath, commandPath)}
	if len(flagNames) > 0 {
		attrs = append(attrs, attribute.StringSlice(AttrCommandFlags, flagNames))
	}
	c.emitEvent(ctx, "cli.command_invocation", otellog.SeverityInfo1, attrs...)
}

// RecordExitCode records the exit code a command finished with.
// commandPath identifies which command this applies to and should
// match the value passed to RecordCommandInvocation; pass "" if
// unknown (e.g. a non-Cobra CLI with a single implicit command).
//
// At TierAnonymous the exit code is recorded without a command path
// (aggregate success/failure counts only). At TierBasic+ the command
// path is included.
func (c *Client) RecordExitCode(ctx context.Context, commandPath string, code int) {
	defer recoverGuard(nil)
	if !c.gate(TierAnonymous) {
		return
	}
	attrs := []attribute.KeyValue{attribute.Int(AttrExitCode, code)}
	if c.tier.Allows(TierBasic) && commandPath != "" {
		attrs = append(attrs, attribute.String(AttrCommandPath, commandPath))
	}
	sev := otellog.SeverityInfo1
	if code != 0 {
		sev = otellog.SeverityWarn1
	}
	c.emitEvent(ctx, "cli.exit_code", sev, attrs...)
}

// RecordLatency records how long a command took to execute. commandPath
// identifies which command this applies to; pass "" for a whole-session
// duration or a single-command CLI. d is recorded in milliseconds on a
// histogram (cli.command.duration), suitable for percentile queries
// server-side.
//
// Available at every tier: a duration by itself, without a command
// path, carries no more information than "some command took Xms" (the
// command path attribute itself is still gated to TierBasic+).
func (c *Client) RecordLatency(ctx context.Context, commandPath string, d time.Duration) {
	defer recoverGuard(nil)
	if !c.gate(TierAnonymous) {
		return
	}
	attrs := []attribute.KeyValue{c.tierAttr()}
	if c.tier.Allows(TierBasic) && commandPath != "" {
		attrs = append(attrs, attribute.String(AttrCommandPath, commandPath))
	}
	c.dispatch(func() {
		c.latencyHist.Record(ctx, float64(d.Milliseconds()), metric.WithAttributes(attrs...))
	})
}

// RecordHelpFlagUsage records that a command's help output was
// requested (-h/--help or an explicit help command).
func (c *Client) RecordHelpFlagUsage(ctx context.Context, commandPath string) {
	defer recoverGuard(nil)
	if !c.gate(TierAnonymous) {
		return
	}
	attrs := []attribute.KeyValue{attribute.Bool(AttrHelpUsed, true)}
	if c.tier.Allows(TierBasic) && commandPath != "" {
		attrs = append(attrs, attribute.String(AttrCommandPath, commandPath))
	}
	c.emitEvent(ctx, "cli.help_flag_used", otellog.SeverityInfo1, attrs...)
}

// --- Errors --------------------------------------------------------

// RecordError records that a command failed with an error, classified
// by category rather than its raw message or stack trace. This is
// deliberately the only error-recording capability available below
// TierOptIn: it is structurally impossible to pass a stack trace or raw
// error message through this function's signature. Raw error detail is
// only reachable via Client.OptIn(), which itself only returns a usable
// recorder when the resolved tier is actually TierOptIn.
//
// Requires TierFull+; at lower tiers this is a no-op (counted in
// Stats().BelowTierDropped).
func (c *Client) RecordError(ctx context.Context, commandPath string, category ErrorCategory) {
	defer recoverGuard(nil)
	if !c.gate(TierFull) {
		return
	}
	if !category.valid() {
		category = ErrorCategoryUnknown
	}
	attrs := []attribute.KeyValue{
		attribute.String(AttrErrorCategory, string(category)),
	}
	if commandPath != "" {
		attrs = append(attrs, attribute.String(AttrCommandPath, commandPath))
	}
	c.dispatch(func() {
		c.errorEvents.Add(ctx, 1, metric.WithAttributes(c.tierAttr(), attribute.String(AttrErrorCategory, string(category))))
	})
	c.emitEvent(ctx, "cli.error", otellog.SeverityError1, attrs...)
}

// OptInScope exposes telemetry capabilities that require the user's
// explicit TierOptIn consent, structurally separated from Client so
// that raw error content (messages, stack traces) can never be passed
// through a lower-tier code path by mistake or misuse — there is no
// function anywhere in this package outside OptInScope that accepts
// that data.
type OptInScope struct {
	c *Client
}

// OptIn returns an OptInScope and true if, and only if, the Client's
// resolved consent tier is TierOptIn. Otherwise it returns a non-nil,
// safe-to-call-but-inert OptInScope and false — callers can use the
// zero-cost pattern:
//
//	if scope, ok := client.OptIn(); ok {
//	    scope.RecordErrorDetail(ctx, cmd.CommandPath(), err)
//	}
//
// without needing a separate nil check, though checking ok is required
// to know whether detailed capture is actually happening.
func (c *Client) OptIn() (*OptInScope, bool) {
	if c == nil {
		return &OptInScope{}, false
	}
	return &OptInScope{c: c}, c.enabled() && c.tier == TierOptIn
}

// RecordErrorDetail records an error's message and stack trace at full
// fidelity. Only emits anything if the scope's Client is actually
// resolved to TierOptIn (re-checked here defensively, not just at
// OptIn() call time) — calling this on a scope obtained when OptIn
// returned false is always a safe no-op.
func (s *OptInScope) RecordErrorDetail(ctx context.Context, commandPath string, err error) {
	defer recoverGuard(nil)
	if s == nil || s.c == nil || err == nil {
		return
	}
	c := s.c
	if !c.gate(TierOptIn) {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(AttrErrorMessage, err.Error()),
	}
	if commandPath != "" {
		attrs = append(attrs, attribute.String(AttrCommandPath, commandPath))
	}
	c.emitEvent(ctx, "cli.error_detail", otellog.SeverityError1, attrs...)
}

// RecordErrorDetailWithStack is identical to RecordErrorDetail but also
// attaches a raw stack trace. Kept as a separate, more explicitly named
// method (rather than an optional parameter) so call sites make an
// affirmative, visible choice to capture stack traces.
func (s *OptInScope) RecordErrorDetailWithStack(ctx context.Context, commandPath string, err error, stack []byte) {
	defer recoverGuard(nil)
	if s == nil || s.c == nil || err == nil {
		return
	}
	c := s.c
	if !c.gate(TierOptIn) {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(AttrErrorMessage, err.Error()),
	}
	if commandPath != "" {
		attrs = append(attrs, attribute.String(AttrCommandPath, commandPath))
	}
	if len(stack) > 0 {
		attrs = append(attrs, attribute.String(AttrErrorStack, string(stack)))
	}
	c.emitEvent(ctx, "cli.error_detail", otellog.SeverityError1, attrs...)
}
