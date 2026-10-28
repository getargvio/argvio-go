// Package cobrasdk instruments a github.com/spf13/cobra command tree to
// automatically emit telemetry through an *argvio.Client, without
// requiring the vendor to manually instrument every subcommand.
//
// # Why not just PersistentPreRunE/PersistentPostRunE
//
// Cobra's own execute() short-circuits before running any hooks at all
// when --help is requested, and — less obviously — it does not run
// PersistentPostRunE when RunE returns a non-nil error (it returns
// immediately from execute() instead). A naive PersistentPostRunE-only
// implementation would therefore silently miss every failing command
// and every help invocation, which are exactly the cases telemetry
// most needs to see. To capture path/flags/start-time reliably this
// package still uses PersistentPreRunE (chained with any hook the
// vendor already installed), but exit code, latency, and error capture
// are implemented by wrapping each command's RunE directly — the one
// place in Cobra's execution path that is guaranteed to run exactly
// when, and only when, the command's own logic actually ran — and help
// capture is implemented via Command.SetHelpFunc, the dedicated,
// chainable extension point Cobra provides for exactly this case.
//
// # Unknown commands
//
// A mistyped command never reaches RunE or PersistentPreRunE, so neither
// hook sees it. Cobra reports it in one of two ways, and each needs its own
// observer: on a root command with subcommands, Find fails and ExecuteC
// returns an error ("unknown command "x" for "mycli"") before any hook runs,
// which is observed by Execute or RecordExecuteError; on a non-root parent
// that has subcommands but no Run, cobra silently prints its help instead,
// which is observed from the help func InstrumentCobra installs. Neither
// changes what cobra does: the host CLI keeps its error, output and exit
// code. Installing an Args validator instead would not work (cobra skips
// Args on a command that is not runnable, and Args on the root replaces its
// built-in unknown-command check), so it is not used.
//
// What the user typed is only ever read in memory to find the nearest
// registered command, by edit distance; it is never sent, logged or stored,
// and only that command's own name is reported.
//
// See docs/cobra-integration.md in the module root for a worked
// example and the manual-wiring pattern for vendors who want to call
// the underlying pieces themselves instead of using Instrument or
// InstrumentCobra.
package cobrasdk

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	argvio "github.com/getargvio/argvio-go"
)

// Instrument is the single-call convenience helper for the common case:
// it builds an *argvio.Client from apiKey plus the name/version already
// declared on root (root.Name() and root.Version — see Cobra's own
// docs for how those are populated, typically from the Use and Version
// fields), then calls InstrumentCobra to wire up automatic capture.
//
//	client, err := cobrasdk.Instrument(rootCmd, apiKey,
//	    argvio.WithEndpoint("otel-collector.internal.example.com:4317"),
//	)
//	if err != nil {
//	    // client is still safe to use (degrades to no-op); log if you want
//	}
//	defer client.Shutdown(context.Background())
//
// Any argvio.Option can be passed through, e.g. to set the endpoint
// (required unless OTEL_EXPORTER_OTLP_ENDPOINT is set) or a
// ConsentProvider:
//
//	client, err := cobrasdk.Instrument(rootCmd, apiKey,
//	    argvio.WithEndpoint("otel-collector.internal.example.com:4317"),
//	    argvio.WithConsentProvider(myProvider),
//	)
//
// If you need a *argvio.Client built some other way (e.g. already
// constructed for reuse across multiple command trees, or you need
// options InstrumentCobra's HookOption doesn't expose), construct it
// with argvio.New directly and call InstrumentCobra yourself instead of
// Instrument.
func Instrument(root *cobra.Command, apiKey string, opts ...argvio.Option) (*argvio.Client, error) {
	if root == nil {
		client, err := argvio.New(apiKey, "", "", opts...)
		return client, err
	}
	client, err := argvio.New(apiKey, root.Name(), root.Version, opts...)
	InstrumentCobra(root, client)
	return client, err
}

// hookConfig holds InstrumentCobra's optional behavior. Kept
// unexported; configured via HookOption.
type hookConfig struct {
	// category and reason classify an error separately; both classifies it
	// in one call and takes precedence over the two when set. Each option
	// clears the fields it supersedes, so the last one given wins.
	category func(error) argvio.ErrorCategory
	reason   func(error) string
	both     func(error) (argvio.ErrorCategory, string)

	captureHelp    bool
	captureRunE    bool
	captureUnknown bool
}

// classify returns the category and reason for err, calling the vendor's
// classifier once when it returns both. The category defaults to
// argvio.ErrorCategoryUnknown and the reason to none (empty).
func (c *hookConfig) classify(err error) (argvio.ErrorCategory, string) {
	if c.both != nil {
		return c.both(err)
	}
	category, reason := argvio.ErrorCategoryUnknown, ""
	if c.category != nil {
		category = c.category(err)
	}
	if c.reason != nil {
		reason = c.reason(err)
	}
	return category, reason
}

// HookOption configures InstrumentCobra.
type HookOption func(*hookConfig)

// WithErrorClassifier supplies a function that maps an error returned
// by a command's RunE to an argvio.ErrorCategory. Without this option,
// every captured error is recorded as argvio.ErrorCategoryUnknown —
// this package deliberately does not try to guess a category from an
// error's message or type (see the root module's PII-scrubbing
// non-goal); only your own error handling knows which of your errors
// are user-input mistakes vs. network failures vs. internal bugs.
func WithErrorClassifier(f func(error) argvio.ErrorCategory) HookOption {
	return func(c *hookConfig) {
		if f != nil {
			c.category, c.both = f, nil
		}
	}
}

// WithErrorReason supplies a function that maps an error to a
// vendor-assigned reason: a finer, fixed-vocabulary cause within its
// category (for example "no-token", "invalid-url"), sent as cli.error.reason
// at TierBasic and above. Return "" for an error with no reason.
//
// The reason must be a fixed identifier chosen in code, never the error's
// message or anything derived from user input: it is sent as-is, this
// package does not scrub it, and it is dropped unless it matches
// ^[a-z0-9][a-z0-9._-]*$ and is at most 64 bytes (see argvio.WithReason).
// To get the category and the reason from one call, use
// WithErrorClassification instead.
func WithErrorReason(f func(error) string) HookOption {
	return func(c *hookConfig) {
		if f != nil {
			c.reason, c.both = f, nil
		}
	}
}

// WithErrorClassification is WithErrorClassifier and WithErrorReason in one
// function: it returns both the category and the reason for an error, in a
// single call, so the two can come from the same switch. It replaces any
// classifier or reason function given before it, and is replaced by any
// given after it.
func WithErrorClassification(f func(error) (argvio.ErrorCategory, string)) HookOption {
	return func(c *hookConfig) {
		if f != nil {
			c.both, c.category, c.reason = f, nil, nil
		}
	}
}

// WithoutHelpCapture disables automatic RecordHelpFlagUsage capture via
// Command.SetHelpFunc, for vendors who already have their own help
// instrumentation and don't want InstrumentCobra touching HelpFunc.
func WithoutHelpCapture() HookOption {
	return func(c *hookConfig) { c.captureHelp = false }
}

// WithoutRunCapture disables the recursive RunE-wrapping InstrumentCobra
// otherwise installs across the whole command tree, leaving only the
// PersistentPreRunE-based command-invocation capture in place. Use this
// if you want to call RecordCommand/RecordError yourself
// (e.g. because you need the real OS exit code chosen in main(), which
// Cobra's command tree never sees).
func WithoutRunCapture() HookOption {
	return func(c *hookConfig) { c.captureRunE = false }
}

// WithoutUnknownCommandCapture disables the recording of unknown-command
// attempts that InstrumentCobra otherwise does from the help func, for
// parents that have subcommands but no Run of their own. It does not affect
// Execute or RecordExecuteError, which you call explicitly.
func WithoutUnknownCommandCapture() HookOption {
	return func(c *hookConfig) { c.captureUnknown = false }
}

// InstrumentCobra wires automatic telemetry capture into root and its
// entire subcommand tree:
//
//   - Command path + flag names (see argvio.Client.RecordCommandInvocation)
//     are captured via a chained PersistentPreRunE on root.
//   - Exit code, latency, and errors (see RecordCommand and
//     RecordError) are captured by wrapping RunE (or Run) on every
//     command in the tree that has one, at the time InstrumentCobra is
//     called — subcommands added to the tree afterward are not wrapped.
//   - Help usage (see RecordHelpFlagUsage) is captured via
//     Command.SetHelpFunc.
//   - A mistyped subcommand of a parent that has no Run of its own (see
//     RecordUnknownCommand) is captured from the same help func, since cobra
//     shows that parent's help rather than failing. A mistyped command on the
//     root is only seen through Execute or RecordExecuteError.
//
// InstrumentCobra composes with hooks the vendor has already installed:
// if root already has a PersistentPreRunE, ours runs first (so
// command-invocation is always recorded even if the vendor's own
// pre-run later fails validation and returns an error), then the
// vendor's original hook runs and its result is returned unchanged. The
// same composition applies to an existing HelpFunc. It never discards a
// hook the vendor set.
//
// Safe to call with a nil client (degrades to installing no-op hooks)
// or a nil root (no-op).
func InstrumentCobra(root *cobra.Command, client *argvio.Client, opts ...HookOption) {
	if root == nil {
		return
	}
	cfg := &hookConfig{captureHelp: true, captureRunE: true, captureUnknown: true}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	classifiers.Store(root, cfg)

	installPreRun(root, client)
	if cfg.captureHelp || cfg.captureUnknown {
		installHelpCapture(root, client, cfg)
	}
	if cfg.captureRunE {
		wrapRunTree(root, client, cfg)
	}
}

type startTimeKey struct{}

func installPreRun(root *cobra.Command, client *argvio.Client) {
	existing := root.PersistentPreRunE
	existingNoE := root.PersistentPreRun

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		ctx := context.WithValue(cmd.Context(), startTimeKey{}, time.Now())
		cmd.SetContext(ctx)
		client.RecordSessionStart(ctx)
		client.RecordCommandInvocation(ctx, cmd.CommandPath(), changedFlagNames(cmd))

		if existing != nil {
			return existing(cmd, args)
		}
		if existingNoE != nil {
			existingNoE(cmd, args)
		}
		return nil
	}
	// Clear the non-error variant: Cobra only calls one of
	// PersistentPreRun/PersistentPreRunE (it prefers the E variant), and
	// we've already folded the non-E one into our replacement above.
	root.PersistentPreRun = nil
}

func installHelpCapture(root *cobra.Command, client *argvio.Client, cfg *hookConfig) {
	existing := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		client.RecordSessionStart(cmd.Context())
		// Cobra shows help in place of an error for a mistyped subcommand of
		// a parent with no Run; that is an unknown command, not a help view.
		if typed, ok := mistypedSubcommand(cmd); ok && cfg.captureUnknown {
			recordUnknown(cmd, client, typed)
		} else if cfg.captureHelp {
			client.RecordHelpFlagUsage(cmd.Context(), cmd.CommandPath())
		}
		if existing != nil {
			existing(cmd, args)
		}
	})
}

// mistypedSubcommand reports whether cmd's help is being shown because the
// user typed something that is not one of its subcommands, and returns what
// they typed. That is the case for a parent that has subcommands but is not
// runnable, was given a positional argument and was not asked for help: cobra
// falls back to its help without an error.
func mistypedSubcommand(cmd *cobra.Command) (typed string, ok bool) {
	if cmd.Runnable() || !cmd.HasSubCommands() {
		return "", false
	}
	help := cmd.Flags().Lookup("help")
	if help == nil || help.Changed || !cmd.Flags().Parsed() || cmd.Flags().NArg() == 0 {
		return "", false
	}
	return cmd.Flags().Arg(0), true
}

// unknownCommandPrefix starts the error cobra returns for an unknown
// subcommand: `unknown command "x" for "mycli"`.
const unknownCommandPrefix = "unknown command "

// Execute runs root like root.Execute and records an unknown-command attempt
// if that is how it failed (see RecordExecuteError). It returns exactly the
// error root.Execute would, so the host CLI's error output and exit code are
// unchanged.
//
//	if err := cobrasdk.Execute(rootCmd, client); err != nil {
//	    os.Exit(1)
//	}
//
// Safe to call with a nil client.
func Execute(root *cobra.Command, client *argvio.Client) error {
	if root == nil {
		return nil
	}
	cmd, err := root.ExecuteC()
	RecordExecuteError(cmd, client, err)
	return err
}

// RecordExecuteError records an unknown-command attempt if err, as returned by
// ExecuteC or Execute, is cobra's unknown-command error; cmd is the command
// ExecuteC returned alongside it. Use it instead of Execute when you call
// ExecuteC yourself. It does nothing for any other error, so it is safe to
// call on every one.
//
// Only a command with subcommands counts: the same message from an Args
// validator such as cobra.NoArgs on a leaf is a misused command, not an
// unknown one. The text of err is read once, in memory, to find the nearest
// registered command; it is not sent, logged or stored.
func RecordExecuteError(cmd *cobra.Command, client *argvio.Client, err error) {
	if cmd == nil || err == nil || !cmd.HasSubCommands() {
		return
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, unknownCommandPrefix) {
		return
	}
	// If the message ever stops looking like this the attempt is still
	// counted, just without a suggestion.
	var typed string
	if quoted, qerr := strconv.QuotedPrefix(msg[len(unknownCommandPrefix):]); qerr == nil {
		typed, _ = strconv.Unquote(quoted)
	}
	recordUnknown(cmd, client, typed)
}

// recordUnknown records an unknown-command attempt on parent, with the nearest
// of parent's subcommands to typed as the suggestion when one is close enough.
// typed is used here and nowhere else: it never leaves this function.
func recordUnknown(parent *cobra.Command, client *argvio.Client, typed string) {
	ctx := parent.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	var suggestion string
	if near := nearestCommand(parent, typed); near != nil {
		suggestion = near.CommandPath()
	}
	// Registered on every attempt rather than once up front, so commands
	// added after InstrumentCobra are known too. The attempt is rare.
	client.SetKnownCommands(commandPaths(parent.Root())...)
	client.RecordSessionStart(ctx)
	client.RecordUnknownCommand(ctx, suggestion)
}

// commandPaths returns the command path of every command under cmd.
func commandPaths(cmd *cobra.Command) []string {
	paths := []string{cmd.CommandPath()}
	for _, sub := range cmd.Commands() {
		paths = append(paths, commandPaths(sub)...)
	}
	return paths
}

// maxTypedLength bounds what is compared: no command name is longer than the
// server's limit, so anything longer is not a typo of one.
const maxTypedLength = 128

// defaultSuggestionDistance is cobra's own default for
// SuggestionsMinimumDistance.
const defaultSuggestionDistance = 2

// nearestCommand returns the subcommand of parent whose name is within edit
// distance of typed (cobra's SuggestionsMinimumDistance, 2 by default), the
// closest one if several are, or nil. Only edit distance counts, case
// ignored, plus the SuggestFor names the vendor declared: a command whose
// name merely starts with, or contains, the typed text is not a suggestion.
// Honours DisableSuggestions.
func nearestCommand(parent *cobra.Command, typed string) *cobra.Command {
	if parent.DisableSuggestions || typed == "" || len(typed) > maxTypedLength {
		return nil
	}
	limit := parent.SuggestionsMinimumDistance
	if limit <= 0 {
		limit = defaultSuggestionDistance
	}
	typed = strings.ToLower(typed)
	var best *cobra.Command
	bestDist := limit + 1
	for _, sub := range parent.Commands() {
		if !sub.IsAvailableCommand() {
			continue
		}
		dist := editDistance(typed, strings.ToLower(sub.Name()))
		for _, alt := range sub.SuggestFor {
			if strings.EqualFold(typed, alt) {
				dist = 0
			}
		}
		if dist < bestDist {
			best, bestDist = sub, dist
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b, in runes.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func wrapRunTree(cmd *cobra.Command, client *argvio.Client, cfg *hookConfig) {
	wrapRunE(cmd, client, cfg)
	for _, sub := range cmd.Commands() {
		wrapRunTree(sub, client, cfg)
	}
}

func wrapRunE(cmd *cobra.Command, client *argvio.Client, cfg *hookConfig) {
	switch {
	case cmd.RunE != nil:
		inner := cmd.RunE
		cmd.RunE = func(c *cobra.Command, args []string) error {
			err := inner(c, args)
			code := 0
			if err != nil {
				code = 1
			}
			recordOutcome(c, client, cfg, err, code)
			return err
		}
	case cmd.Run != nil:
		inner := cmd.Run
		cmd.RunE = func(c *cobra.Command, args []string) error {
			inner(c, args)
			recordOutcome(c, client, cfg, nil, 0)
			return nil
		}
		cmd.Run = nil
	}
}

// RecordExit records cmd as having finished with the given exit code, for
// commands that end the process themselves (os.Exit, log.Fatal) from inside
// their Run/RunE: that bypasses the wrapper InstrumentCobra installs, so
// nothing would be recorded otherwise. Call it right before exiting, then
// flush the client (Shutdown) since deferred calls won't run either:
//
//	if failed {
//	    cobrasdk.RecordExit(cmd, client, 1)
//	    _ = client.Shutdown(ctx)
//	    os.Exit(1)
//	}
//
// A non-zero code is also recorded as an error of unknown category. Safe to
// call with a nil client.
func RecordExit(cmd *cobra.Command, client *argvio.Client, code int) {
	if cmd == nil {
		return
	}
	var err error
	if code != 0 {
		err = errExitCode
	}
	recordOutcome(cmd, client, &hookConfig{}, err, code)
}

// RecordExitError is RecordExit for a command that ends the process itself
// after a failure: err is classified into the argvio.ErrorCategory recorded
// on cli.error.raised, instead of always being recorded as unknown.
//
//	if err != nil {
//	    cobrasdk.RecordExitError(cmd, client, err, 1)
//	    _ = client.Shutdown(ctx)
//	    os.Exit(1)
//	}
//
// The category and reason come from the WithErrorClassifier,
// WithErrorReason and WithErrorClassification options passed in opts, over
// those passed to InstrumentCobra for cmd's command tree, over
// argvio.ErrorCategoryUnknown and no reason. Where opts and the tree both
// set one, opts wins; a WithErrorClassification in opts replaces both of the
// tree's. A non-nil err or a non-zero code is recorded as an error; when err
// is nil and code is non-zero, the classifier is given a placeholder error.
// Other options in opts are ignored. Safe to call with a nil client.
func RecordExitError(cmd *cobra.Command, client *argvio.Client, err error, code int, opts ...HookOption) {
	if cmd == nil {
		return
	}
	if err == nil && code != 0 {
		err = errExitCode
	}
	cfg := classifierFor(cmd)
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	recordOutcome(cmd, client, cfg, err, code)
}

// classifiers maps the root of each command tree passed to InstrumentCobra
// to its hook config, so RecordExitError can reuse its classifiers.
var classifiers sync.Map // *cobra.Command -> *hookConfig

// classifierFor returns a copy of the classifiers InstrumentCobra was given
// for cmd's command tree, which the caller may override option by option.
func classifierFor(cmd *cobra.Command) *hookConfig {
	cfg := &hookConfig{}
	if f, ok := classifiers.Load(cmd.Root()); ok {
		*cfg = *f.(*hookConfig)
	}
	return cfg
}

// errExitCode stands in for the error a command exiting non-zero through
// RecordExit didn't return.
var errExitCode = errors.New("cobrasdk: command exited with a non-zero code")

func recordOutcome(cmd *cobra.Command, client *argvio.Client, cfg *hookConfig, err error, exitCode int) {
	ctx := cmd.Context()
	path := cmd.CommandPath()
	result := argvio.CommandResult{Path: path, FlagNames: changedFlagNames(cmd), ExitCode: exitCode}
	if start, ok := ctx.Value(startTimeKey{}).(time.Time); ok {
		result.Start, result.Duration = start, time.Since(start)
	}
	client.RecordCommand(ctx, result)
	if err != nil {
		category, reason := cfg.classify(err)
		client.RecordError(ctx, path, category, argvio.WithReason(reason))
	}
}

// changedFlagNames returns the names of flags the user actually set on
// cmd (local and inherited), never their values.
func changedFlagNames(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		names = append(names, f.Name)
	})
	return names
}
