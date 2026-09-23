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
// See docs/cobra-integration.md in the module root for a worked
// example and the manual-wiring pattern for vendors who want to call
// the underlying pieces themselves instead of using Instrument or
// InstrumentCobra.
package cobrasdk

import (
	"context"
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
	classify    func(error) argvio.ErrorCategory
	captureHelp bool
	captureRunE bool
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
			c.classify = f
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
// if you want to call RecordExitCode/RecordLatency/RecordError yourself
// (e.g. because you need the real OS exit code chosen in main(), which
// Cobra's command tree never sees).
func WithoutRunCapture() HookOption {
	return func(c *hookConfig) { c.captureRunE = false }
}

// InstrumentCobra wires automatic telemetry capture into root and its
// entire subcommand tree:
//
//   - Command path + flag names (see argvio.Client.RecordCommandInvocation)
//     are captured via a chained PersistentPreRunE on root.
//   - Exit code, latency, and errors (see RecordExitCode, RecordLatency,
//     RecordError) are captured by wrapping RunE (or Run) on every
//     command in the tree that has one, at the time InstrumentCobra is
//     called — subcommands added to the tree afterward are not wrapped.
//   - Help usage (see RecordHelpFlagUsage) is captured via
//     Command.SetHelpFunc.
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
	cfg := &hookConfig{
		classify:    func(error) argvio.ErrorCategory { return argvio.ErrorCategoryUnknown },
		captureHelp: true,
		captureRunE: true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	installPreRun(root, client)
	if cfg.captureHelp {
		installHelpCapture(root, client)
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

func installHelpCapture(root *cobra.Command, client *argvio.Client) {
	existing := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		client.RecordHelpFlagUsage(cmd.Context(), cmd.CommandPath())
		if existing != nil {
			existing(cmd, args)
		}
	})
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
			recordOutcome(c, client, cfg, err)
			return err
		}
	case cmd.Run != nil:
		inner := cmd.Run
		cmd.RunE = func(c *cobra.Command, args []string) error {
			inner(c, args)
			recordOutcome(c, client, cfg, nil)
			return nil
		}
		cmd.Run = nil
	}
}

func recordOutcome(cmd *cobra.Command, client *argvio.Client, cfg *hookConfig, err error) {
	ctx := cmd.Context()
	path := cmd.CommandPath()

	if start, ok := ctx.Value(startTimeKey{}).(time.Time); ok {
		client.RecordLatency(ctx, path, time.Since(start))
	}

	code := 0
	if err != nil {
		code = 1
	}
	client.RecordExitCode(ctx, path, code)

	if err != nil {
		client.RecordError(ctx, path, cfg.classify(err))
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
