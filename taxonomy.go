package argvio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// This file implements the taxonomy: the fixed, typed set of Record*
// functions vendors call, as opposed to a free-form "send any attribute
// bag" API. Field-per-tier boundaries here are the SDK-side enforcement
// of the consent model; server-side allowlist stripping is a backstop,
// not the primary mechanism (see docs/consent.md).
//
// Every event, span, metric and attribute name below is taken from the
// `public` server's allowlist (schema/allowlist/v1.yaml in
// getargvio/argvio), which rejects anything else. Change them only
// together with a server-side schema version — see CONTRIBUTING.md.

// Attribute keys used on emitted signals. Exported so vendors and tests
// can assert on them without relying on string literals staying in
// sync.
const (
	AttrCommandName   = "cli.command.name"
	AttrSuggestion    = "cli.command.suggestion" // TierBasic+, a known command name
	AttrExitCode      = "cli.command.exit_code"
	AttrDurationMs    = "cli.command.duration_ms"
	AttrFlagNames     = "cli.flag.names" // TierBasic+
	AttrSessionID     = "cli.session_id" // TierBasic+
	AttrInstallID     = "cli.install_id" // TierBasic+
	AttrHelpFlag      = "cli.help.flag"
	AttrInstallMethod = "cli.install.method"
	AttrFeature       = "cli.activation.feature" // TierBasic+
	AttrErrorType     = "cli.error.type"         // TierBasic+
	AttrErrorReason   = "cli.error.reason"       // TierBasic+
	AttrErrorMessage  = "cli.error.message"      // TierOptIn only
	AttrErrorStack    = "cli.error.stacktrace"   // TierOptIn only

	// Vendor-defined counters on the cli.counter metric, accepted at every
	// tier including TierAnonymous. See RecordCounter.
	AttrCounterName  = "cli.counter.name"
	AttrCounterLabel = "cli.counter.label"

	// Vendor-defined durations on the cli.measure metric, accepted at every
	// tier including TierAnonymous. See RecordDuration.
	AttrMeasureName  = "cli.measure.name"
	AttrMeasureLabel = "cli.measure.label"

	// Process-wide resource usage on the command span, omitted where the
	// platform can't measure it. See RecordCommand.
	AttrPeakRSSBytes = "cli.process.peak_rss_bytes" // TierBasic+, bytes
	AttrCPUTimeMs    = "cli.process.cpu_time_ms"    // TierBasic+, user + system

	// Build and startup cost on the session start event, omitted where it
	// can't be measured. See RecordSessionStart.
	AttrBinarySizeBytes = "cli.binary.size_bytes"   // TierBasic+, bytes
	AttrStartupMs       = "cli.startup.duration_ms" // TierBasic+, milliseconds

	// The environment the CLI runs in, on the session start event, omitted
	// where it can't be detected. See RecordSessionStart.
	AttrShell          = "cli.shell"           // TierBasic+, fixed vocabulary
	AttrTerminal       = "cli.terminal"        // TierBasic+, fixed vocabulary
	AttrStdoutIsTTY    = "cli.stdout.is_tty"   // TierBasic+, bool
	AttrRuntimeName    = "cli.runtime.name"    // TierBasic+, always "go"
	AttrRuntimeVersion = "cli.runtime.version" // TierBasic+, e.g. "1.22.1"

	// Facts only the vendor's CLI knows, on the session start event, omitted
	// unless the vendor configures them. See WithConfigCustomizedCount and
	// WithAuthState.
	AttrConfigCustomizedCount = "cli.config.customized_count" // TierBasic+, int
	AttrAuthState             = "cli.auth.state"              // TierBasic+, AuthState
)

// Log event names.
const (
	EventSessionStarted   = "cli.session.started"
	EventCommandInvoked   = "cli.command.invoked"
	EventCommandCompleted = "cli.command.completed"
	EventHelpViewed       = "cli.help.viewed"
	EventErrorRaised      = "cli.error.raised"
	EventFeatureActivated = "cli.feature.activated"
	EventCommandUnknown   = "cli.command.unknown"
)

// SpanCommandPrefix prefixes the name of the span RecordCommand emits
// for each command (e.g. "cli.command.deploy.push"). The server's
// command dashboards (frequency, latency, error rate, exit codes) are
// built from these spans.
const SpanCommandPrefix = "cli.command."

// Metric instrument names.
const (
	MetricCommandInvocations = "cli.command.invocations"
	MetricCommandDuration    = "cli.command.duration" // milliseconds
	MetricErrors             = "cli.errors"
	MetricCounter            = "cli.counter"
	MetricMeasure            = "cli.measure" // milliseconds
)

// maxCommandNameLength is the server's max_length for cli.command.name.
const maxCommandNameLength = 128

// maxFeatureLength is the server's max_length for cli.activation.feature.
const maxFeatureLength = 64

// maxIdentifierLength is the server's max_length for cli.error.reason,
// cli.counter.name, cli.counter.label, cli.measure.name and cli.measure.label.
const maxIdentifierLength = 64

// validIdentifier reports whether s matches the server's pattern for
// vendor-chosen identifiers, ^[a-z0-9][a-z0-9._-]*$, and is at most 64
// bytes. Values that don't are dropped rather than scrubbed, so free text
// never reaches the wire.
func validIdentifier(s string) bool {
	if s == "" || len(s) > maxIdentifierLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
		case i > 0 && (ch == '.' || ch == '_' || ch == '-'):
		default:
			return false
		}
	}
	return true
}

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

// commandName converts a command path as the CLI framework reports it
// ("mycli deploy push") into the dotted cli.command.name the server
// expects ("deploy.push"). The CLI's own name is dropped, since every
// record already carries it as service.name, except for the root command
// itself, which keeps it so it still has a name.
func (c *Client) commandName(commandPath string) string {
	parts := strings.Fields(commandPath)
	if len(parts) > 1 && parts[0] == c.cliName {
		parts = parts[1:]
	}
	name := strings.Join(parts, ".")
	if name == "" {
		name = c.cliName
	}
	if name == "" {
		name = "root"
	}
	if len(name) > maxCommandNameLength {
		name = name[:maxCommandNameLength]
	}
	return name
}

// truncateUTF8 cuts s to at most max bytes without splitting a multi-byte
// character, which the server could reject as invalid UTF-8.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
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
		c.emitEventNow(ctx, name, sev, attrs...)
	})
}

func (c *Client) emitEventNow(ctx context.Context, name string, sev otellog.Severity, attrs ...attribute.KeyValue) {
	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetEventName(name)
	rec.SetSeverity(sev)
	rec.AddAttributes(c.tierAttr())
	rec.AddAttributes(attrs...)
	c.logger.Emit(ctx, rec)
}

// --- Session ---------------------------------------------------------

// ensureSessionID lazily generates a session correlation ID at
// TierBasic+. It is a random, non-persistent identifier scoped to a
// single process invocation — not a user or device identifier.
func (c *Client) ensureSessionID() string {
	if !c.tier.Allows(TierBasic) {
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

// identityAttrs returns the session and install ID attributes for a record,
// empty below TierBasic. It may read or create the install ID file, so call
// it from inside dispatch rather than on the caller's goroutine.
func (c *Client) identityAttrs(sessionID string) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if sessionID != "" {
		attrs = append(attrs, attribute.String(AttrSessionID, sessionID))
	}
	if c.tier.Allows(TierBasic) {
		c.installOnce.Do(func() { c.installID = loadOrCreateInstallID(c.installIDPath) })
		if c.installID != "" {
			attrs = append(attrs, attribute.String(AttrInstallID, c.installID))
		}
	}
	return attrs
}

// RecordSessionStart records the start of a CLI process session, which
// is what the server counts sessions, active installs and retention
// from. Call it as early as possible (cobrasdk does it for you); only the
// first call per Client emits anything, so calling it from several entry
// points is fine. Safe to call on a nil or disabled Client.
//
// At TierAnonymous the event carries no identifiers. At TierBasic+ it
// also carries the per-process session ID and the install ID (see
// AttrInstallID), a random UUID persisted in the CLI's config directory
// the first time it is needed — never derived from the user or machine.
//
// At TierBasic+ it also carries the executable's size in bytes
// (AttrBinarySizeBytes) and the startup overhead in milliseconds
// (AttrStartupMs), the time from this package loading until this call, so
// call it right before the command runs (cobrasdk does). Go has no
// portable process start time, so the overhead excludes the runtime's
// initialisation and the init of packages loaded before this one. The size
// is read with one stat, off the caller's goroutine. A value that can't be
// measured is omitted rather than sent as zero; a startup of 0 ms is a real
// measurement and is sent.
//
// Also at TierBasic+ it carries the environment: the shell (AttrShell),
// the terminal emulator (AttrTerminal), whether stdout is a terminal or
// piped (AttrStdoutIsTTY) and the Go runtime (AttrRuntimeName, always "go",
// and AttrRuntimeVersion, with build suffixes stripped). Shell and
// terminal are each one of a small fixed vocabulary (see the Shell* and
// Terminal* constants), "other" for anything unrecognised and, for the
// terminal, "unknown" when nothing identifies it; no path or raw
// environment value is ever sent. Detection reads environment variables
// and stats stdout, off the caller's goroutine, and an attribute that
// can't be detected is omitted.
//
// Also at TierBasic+ it carries two facts the vendor reports itself: how many
// configuration keys differ from their defaults (AttrConfigCustomizedCount,
// from WithConfigCustomizedCount, a bare count clamped to 0-1000) and whether
// the user is signed in (AttrAuthState, from WithAuthState, one of the
// AuthState constants). Either is omitted unless configured. The *Func forms
// are evaluated here, on the caller's goroutine, only at TierBasic+; a
// callback that panics just leaves its attribute out.
func (c *Client) RecordSessionStart(ctx context.Context) {
	defer recoverGuard(nil)
	if !c.enabled() || !c.sessionOn.CompareAndSwap(false, true) {
		return
	}
	sessionID := c.ensureSessionID()
	basic := c.tier.Allows(TierBasic)
	var startupMs int64
	var vendorAttrs []attribute.KeyValue
	if basic {
		// Read here, not in the job, so queueing behind other work doesn't
		// count as startup.
		startupMs = readStartupMs()
		// Also here: the vendor's callbacks may read state (config, credentials)
		// that is only safe to touch from the caller's goroutine.
		vendorAttrs = c.vendorSessionAttrs()
	}
	c.dispatch(func() {
		attrs := c.identityAttrs(sessionID)
		if c.installMethod != "" {
			attrs = append(attrs, attribute.String(AttrInstallMethod, c.installMethod))
		}
		if basic {
			if size, ok := readBinarySize(); ok && size > 0 {
				attrs = append(attrs, attribute.Int64(AttrBinarySizeBytes, size))
			}
			if startupMs >= 0 {
				attrs = append(attrs, attribute.Int64(AttrStartupMs, startupMs))
			}
			attrs = append(attrs, readSessionEnvironment()...)
			attrs = append(attrs, vendorAttrs...)
		}
		c.emitEventNow(ctx, EventSessionStarted, otellog.SeverityInfo1, attrs...)
	})
}

// --- Commands --------------------------------------------------------

// RecordCommandInvocation records that a command began executing.
// commandPath should be the full command path (e.g. cmd.CommandPath()
// in Cobra: "mycli sub subsub"); it is reported as the dotted command
// name ("sub.subsub"). flagNames is the list of flag *names* the user set
// — never flag values, which this SDK never accepts anywhere in the
// taxonomy (flag values are arbitrary free-form input this package
// deliberately does not try to inspect or redact; see the package's
// PII-scrubbing non-goal).
//
// The command name is recorded at every tier; flag names and the session
// ID at TierBasic+.
func (c *Client) RecordCommandInvocation(ctx context.Context, commandPath string, flagNames []string) {
	defer recoverGuard(nil)
	if !c.enabled() {
		return
	}
	name := c.commandName(commandPath)
	c.currentCommand.Store(&name)
	attrs := []attribute.KeyValue{attribute.String(AttrCommandName, name)}
	if c.tier.Allows(TierBasic) && len(flagNames) > 0 {
		attrs = append(attrs, attribute.StringSlice(AttrFlagNames, flagNames))
	}
	sessionID := c.ensureSessionID()
	c.dispatch(func() {
		c.emitEventNow(ctx, EventCommandInvoked, otellog.SeverityInfo1, append(attrs, c.identityAttrs(sessionID)...)...)
	})
}

// CommandResult describes one completed command, for RecordCommand.
type CommandResult struct {
	// Path is the full command path, as for RecordCommandInvocation.
	Path string
	// FlagNames are the names (never values) of the flags the user set.
	// Only sent at TierBasic+.
	FlagNames []string
	// Start is when the command began. Zero means Duration before now.
	Start time.Time
	// Duration is how long the command ran.
	Duration time.Duration
	// ExitCode is the command's exit code; non-zero counts as an error.
	ExitCode int
}

// RecordCommand records a completed command: a cli.command.<name> span
// covering its execution, plus the cli.command.invocations and
// cli.command.duration metrics. This is what the server's command
// frequency, latency, error-rate and exit-code dashboards are built
// from, so call it once for every command that ran (cobrasdk does).
//
// Available at every tier; flag names and process resource usage are only
// attached at TierBasic+. The span then carries the process's peak
// resident memory in bytes (AttrPeakRSSBytes) and its CPU time, user plus
// system, in milliseconds (AttrCPUTimeMs), read once when RecordCommand
// is called. They cover the whole process, not just this command, so in a
// long-lived or multi-command process they are cumulative; a CLI that
// runs one command per process gets exactly the command's usage. A
// measure the platform can't provide (Windows has neither) or that the OS
// fails to report is omitted rather than sent as zero.
func (c *Client) RecordCommand(ctx context.Context, r CommandResult) {
	defer recoverGuard(nil)
	if !c.enabled() {
		return
	}
	if r.Duration < 0 {
		r.Duration = 0
	}
	start := r.Start
	if start.IsZero() {
		start = time.Now().Add(-r.Duration)
	}
	name := c.commandName(r.Path)
	attrs := []attribute.KeyValue{
		c.tierAttr(),
		attribute.String(AttrCommandName, name),
		attribute.Int(AttrExitCode, r.ExitCode),
	}
	spanAttrs := make([]attribute.KeyValue, 0, len(attrs)+4)
	spanAttrs = append(spanAttrs, attrs...)
	spanAttrs = append(spanAttrs, attribute.Int64(AttrDurationMs, r.Duration.Milliseconds()))
	if c.tier.Allows(TierBasic) {
		if len(r.FlagNames) > 0 {
			spanAttrs = append(spanAttrs, attribute.StringSlice(AttrFlagNames, r.FlagNames))
		}
		peakRSS, cpuTime := readProcessUsage()
		if peakRSS > 0 {
			spanAttrs = append(spanAttrs, attribute.Int64(AttrPeakRSSBytes, peakRSS))
		}
		if cpuTime > 0 {
			spanAttrs = append(spanAttrs, attribute.Int64(AttrCPUTimeMs, cpuTime))
		}
	}
	sessionID := c.ensureSessionID()
	c.dispatch(func() {
		_, span := c.tracer.Start(ctx, SpanCommandPrefix+name,
			trace.WithTimestamp(start),
			trace.WithAttributes(spanAttrs...),
		)
		if r.ExitCode != 0 {
			span.SetStatus(codes.Error, "")
		}
		span.End(trace.WithTimestamp(start.Add(r.Duration)))

		c.invocations.Add(ctx, 1, metric.WithAttributes(attrs...))
		c.latencyHist.Record(ctx, float64(r.Duration.Microseconds())/1000, metric.WithAttributes(attrs...))

		// The completed event ties the outcome to the session and install,
		// which the span can't carry; the server's activation and friction
		// views are built from it.
		c.emitEventNow(ctx, EventCommandCompleted, otellog.SeverityInfo1, append([]attribute.KeyValue{
			attribute.String(AttrCommandName, name),
			attribute.Int(AttrExitCode, r.ExitCode),
			attribute.Int64(AttrDurationMs, r.Duration.Milliseconds()),
		}, c.identityAttrs(sessionID)...)...)
	})
}

// --- Features --------------------------------------------------------

// RecordFeatureActivated records that the user used one of the CLI's
// features, so vendors can see which features an install has actually
// adopted. The event carries the feature name, the name of the command
// last passed to RecordCommandInvocation (when there was one), and the
// session and install IDs.
//
// feature must be a fixed identifier chosen by the vendor in code (for
// example "deploy.dry-run"), never user input: it is sent as-is, and this
// SDK does not scrub it. It is cut to 64 bytes, the server's limit, and
// an empty feature is ignored.
//
// Each distinct feature is reported at most once per Client (that is,
// once per process), to keep volume down; the server works out the first
// time an install used a feature on its own, so repeating the call adds
// nothing. The event is TierBasic+, since it is only useful tied to an
// install; below that nothing is emitted and the feature is not
// remembered. Safe to call on a nil or disabled Client.
func (c *Client) RecordFeatureActivated(ctx context.Context, feature string) {
	defer recoverGuard(nil)
	if !c.gate(TierBasic) {
		return
	}
	feature = truncateUTF8(feature, maxFeatureLength)
	if feature == "" {
		return
	}
	if _, seen := c.activated.LoadOrStore(feature, struct{}{}); seen {
		return
	}
	attrs := []attribute.KeyValue{attribute.String(AttrFeature, feature)}
	if p := c.currentCommand.Load(); p != nil {
		attrs = append(attrs, attribute.String(AttrCommandName, *p))
	}
	sessionID := c.ensureSessionID()
	c.dispatch(func() {
		c.emitEventNow(ctx, EventFeatureActivated, otellog.SeverityInfo1, append(attrs, c.identityAttrs(sessionID)...)...)
	})
}

// SetKnownCommands registers the CLI's own command set: the command paths
// (as for RecordCommandInvocation, e.g. "mycli deploy push" or "deploy.push")
// that RecordUnknownCommand may report as a suggestion. It replaces any set
// given before. cobrasdk calls it for you when it records an unknown command;
// call it yourself only when you record them by hand. Safe to call on a nil
// or disabled Client.
func (c *Client) SetKnownCommands(commandPaths ...string) {
	defer recoverGuard(nil)
	if !c.enabled() {
		return
	}
	known := make(map[string]struct{}, len(commandPaths))
	for _, p := range commandPaths {
		if strings.TrimSpace(p) != "" {
			known[c.commandName(p)] = struct{}{}
		}
	}
	c.knownCommands.Store(&known)
}

// knownCommand returns the registered command name that path refers to, or
// "" if there is none. What it returns is the registered string itself, never
// the caller's, so nothing the caller passed in can reach the wire.
func (c *Client) knownCommand(path string) string {
	known := c.knownCommands.Load()
	if known == nil || path == "" || len(path) > 4*maxCommandNameLength {
		return ""
	}
	name := c.commandName(path)
	if _, ok := (*known)[name]; ok {
		return name
	}
	return ""
}

// RecordUnknownCommand records one attempt to run a command that does not
// exist, so vendors can see how often users mistype commands and which ones
// they were reaching for. It emits cli.command.unknown, which carries no
// command name, flags, session or install ID and an empty body.
//
// The typed text is deliberately not a parameter: a mistyped command is
// arbitrary user input and may be a secret, a path or a sentence, so this SDK
// accepts none of it, nor anything derived from it (a prefix, a hash, a
// length). suggestion is the nearest command the CLI itself knows, chosen by
// the CLI from its own command table; pass "" when no command is close enough.
// It is sent only if it names a command registered with SetKnownCommands and
// at TierBasic+; anything else is dropped and the attempt is still recorded
// without it.
//
// At TierAnonymous the event is a bare count of attempts. Safe to call on a
// nil or disabled Client.
func (c *Client) RecordUnknownCommand(ctx context.Context, suggestion string) {
	defer recoverGuard(nil)
	if !c.gate(TierAnonymous) {
		return
	}
	var attrs []attribute.KeyValue
	if c.tier.Allows(TierBasic) {
		if name := c.knownCommand(suggestion); name != "" {
			attrs = append(attrs, attribute.String(AttrSuggestion, name))
		}
	}
	c.emitEvent(ctx, EventCommandUnknown, otellog.SeverityInfo1, attrs...)
}

// RecordHelpFlagUsage records that a command's help output was
// requested (-h/--help or an explicit help command).
func (c *Client) RecordHelpFlagUsage(ctx context.Context, commandPath string) {
	defer recoverGuard(nil)
	if !c.gate(TierAnonymous) {
		return
	}
	c.emitEvent(ctx, EventHelpViewed, otellog.SeverityInfo1,
		attribute.String(AttrCommandName, c.commandName(commandPath)),
		attribute.String(AttrHelpFlag, "help"),
	)
}

// --- Errors --------------------------------------------------------

// ErrorOption configures RecordError.
type ErrorOption func(*errorConfig)

type errorConfig struct {
	reason string
}

// WithReason attaches a vendor-assigned reason to the error: a finer,
// fixed-vocabulary cause within its category (for example "no-token" or
// "invalid-url"), sent as cli.error.reason at TierBasic+.
//
// reason must be a fixed identifier chosen by the vendor in code, never the
// error's message or anything derived from user input. It is sent as-is and
// this SDK does not scrub it, but it must match ^[a-z0-9][a-z0-9._-]*$ and
// be at most 64 bytes: a reason that doesn't is dropped, and the error is
// still recorded without one.
func WithReason(reason string) ErrorOption {
	return func(c *errorConfig) { c.reason = reason }
}

// RecordError records that a command failed with an error, classified
// by category rather than its raw message or stack trace. This is
// deliberately the only error-recording capability available below
// TierOptIn: it is structurally impossible to pass a stack trace or raw
// error message through this function's signature. Raw error detail is
// only reachable via Client.OptIn(), which itself only returns a usable
// recorder when the resolved tier is actually TierOptIn.
//
// At every tier this counts the error on the cli.errors metric by
// command. At TierBasic+ the category is attached as the error type and a
// cli.error.raised event is emitted too, tied to the session and install.
// WithReason adds a vendor-assigned reason to both, at TierBasic+ only.
func (c *Client) RecordError(ctx context.Context, commandPath string, category ErrorCategory, opts ...ErrorOption) {
	defer recoverGuard(nil)
	if !c.enabled() {
		return
	}
	if !category.valid() {
		category = ErrorCategoryUnknown
	}
	var cfg errorConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	attrs := []attribute.KeyValue{attribute.String(AttrCommandName, c.commandName(commandPath))}
	basic := c.tier.Allows(TierBasic)
	if basic {
		attrs = append(attrs, attribute.String(AttrErrorType, string(category)))
		if validIdentifier(cfg.reason) {
			attrs = append(attrs, attribute.String(AttrErrorReason, cfg.reason))
		}
	}
	metricAttrs := append([]attribute.KeyValue{c.tierAttr()}, attrs...)
	sessionID := c.ensureSessionID()
	c.dispatch(func() {
		c.errorEvents.Add(ctx, 1, metric.WithAttributes(metricAttrs...))
		if basic {
			c.emitEventNow(ctx, EventErrorRaised, otellog.SeverityError1, append(attrs, c.identityAttrs(sessionID)...)...)
		}
	})
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
		attribute.String(AttrCommandName, c.commandName(commandPath)),
		attribute.String(AttrErrorMessage, err.Error()),
	}
	c.emitEvent(ctx, EventErrorRaised, otellog.SeverityError1, attrs...)
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
		attribute.String(AttrCommandName, c.commandName(commandPath)),
		attribute.String(AttrErrorMessage, err.Error()),
	}
	if len(stack) > 0 {
		attrs = append(attrs, attribute.String(AttrErrorStack, string(stack)))
	}
	c.emitEvent(ctx, EventErrorRaised, otellog.SeverityError1, attrs...)
}
