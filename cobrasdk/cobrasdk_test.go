package cobrasdk

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	argvio "github.com/getargvio/argvio-go"
	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

func newTestClient(t *testing.T, tier argvio.Tier) (*argvio.Client, *fakeotlp.Server) {
	t.Helper()
	srv, err := fakeotlp.Start()
	if err != nil {
		t.Fatalf("starting fake OTLP server: %v", err)
	}
	t.Cleanup(srv.Close)

	c, err := argvio.New("test-key", "samplecli", "9.9.9",
		argvio.WithEndpoint(srv.Addr()),
		argvio.WithInsecure(),
		argvio.WithDefaultTier(tier),
		argvio.WithExportTimeout(2*time.Second),
		argvio.WithInstallIDPath(filepath.Join(t.TempDir(), "install-id")),
	)
	if err != nil {
		t.Fatalf("argvio.New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	})
	return c, srv
}

func flush(t *testing.T, c *argvio.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func eventAttr(srv *fakeotlp.Server, eventName, key string) (string, bool) {
	for _, rl := range srv.Logs() {
		for _, scope := range rl.GetScopeLogs() {
			for _, rec := range scope.GetLogRecords() {
				if rec.GetEventName() != eventName {
					continue
				}
				for _, kv := range rec.GetAttributes() {
					if kv.GetKey() == key {
						return kv.GetValue().GetStringValue(), true
					}
				}
			}
		}
	}
	return "", false
}

func hasEvent(srv *fakeotlp.Server, eventName string) bool {
	for _, rl := range srv.Logs() {
		for _, scope := range rl.GetScopeLogs() {
			for _, rec := range scope.GetLogRecords() {
				if rec.GetEventName() == eventName {
					return true
				}
			}
		}
	}
	return false
}

func newSampleCLI(run func(*cobra.Command, []string) error) *cobra.Command {
	root := &cobra.Command{Use: "samplecli", Version: "9.9.9"}
	sub := &cobra.Command{
		Use: "sub",
		RunE: func(cmd *cobra.Command, args []string) error {
			if run != nil {
				return run(cmd, args)
			}
			return nil
		},
	}
	sub.Flags().Bool("verbose", false, "verbose output")
	sub.Flags().String("name", "", "a name")
	root.AddCommand(sub)
	return root
}

func TestInstrumentCobraCapturesSuccessfulInvocation(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newSampleCLI(nil)
	InstrumentCobra(root, client)

	root.SetArgs([]string{"sub", "--verbose"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	flush(t, client)

	name, ok := eventAttr(srv, argvio.EventCommandInvoked, argvio.AttrCommandName)
	if !ok || name != "sub" {
		t.Fatalf("expected command name %q, got (%q, %v)", "sub", name, ok)
	}
	if !hasEvent(srv, argvio.EventSessionStarted) {
		t.Fatal("expected a session started event")
	}
	span := srv.Span(argvio.SpanCommandPrefix + "sub")
	if span == nil {
		t.Fatal("expected a cli.command.sub span")
	}
	if code := fakeotlp.Attr(span.GetAttributes(), argvio.AttrExitCode); code.GetIntValue() != 0 || code == nil {
		t.Fatalf("expected exit code 0 on span, got %v", code)
	}
	flags := fakeotlp.Attr(span.GetAttributes(), argvio.AttrFlagNames).GetArrayValue().GetValues()
	if len(flags) != 1 || flags[0].GetStringValue() != "verbose" {
		t.Fatalf("expected flag names [verbose] on span, got %v", flags)
	}
}

func TestInstrumentCobraCapturesFailingCommand(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierFull)
	wantErr := errors.New("sub failed")
	root := newSampleCLI(func(*cobra.Command, []string) error { return wantErr })
	InstrumentCobra(root, client, WithErrorClassifier(func(err error) argvio.ErrorCategory {
		return argvio.ErrorCategoryValidation
	}))

	root.SetArgs([]string{"sub"})
	root.SetOut(&bytes.Buffer{})
	root.SilenceErrors = true
	root.SilenceUsage = true
	if err := root.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want %v", err, wantErr)
	}
	flush(t, client)

	// This is exactly the case a naive PersistentPostRunE-only
	// implementation would miss: Cobra does not run PersistentPostRunE
	// when RunE errors, so exit code / error capture must come from the
	// RunE wrapper, not the persistent hook.
	span := srv.Span(argvio.SpanCommandPrefix + "sub")
	if span == nil {
		t.Fatal("expected a cli.command.sub span even though RunE returned an error")
	}
	if code := fakeotlp.Attr(span.GetAttributes(), argvio.AttrExitCode).GetIntValue(); code != 1 {
		t.Fatalf("expected exit code 1 on span, got %d", code)
	}
	errType, ok := eventAttr(srv, argvio.EventErrorRaised, argvio.AttrErrorType)
	if !ok || errType != string(argvio.ErrorCategoryValidation) {
		t.Fatalf("expected classified error type, got (%q, %v)", errType, ok)
	}
}

func TestInstrumentCobraCapturesHelp(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newSampleCLI(nil)
	InstrumentCobra(root, client)

	root.SetArgs([]string{"sub", "--help"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	flush(t, client)

	if !hasEvent(srv, argvio.EventHelpViewed) {
		t.Fatal("expected cli.help.viewed event for --help invocation")
	}
	if !hasEvent(srv, argvio.EventSessionStarted) {
		t.Fatal("expected a session started event for a --help invocation")
	}
	// --help must not also produce a command span: Cobra returns before
	// running PersistentPreRunE or RunE in this path.
	if srv.Span(argvio.SpanCommandPrefix+"sub") != nil {
		t.Fatal("did not expect a command span for a --help invocation")
	}
}

func TestInstrumentCobraChainsExistingHooks(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newSampleCLI(nil)

	var vendorPreRunCalled, vendorHelpCalled bool
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		vendorPreRunCalled = true
		return nil
	}
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		vendorHelpCalled = true
	})

	InstrumentCobra(root, client)

	root.SetArgs([]string{"sub"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !vendorPreRunCalled {
		t.Fatal("InstrumentCobra must not clobber the vendor's PersistentPreRunE")
	}

	root.SetArgs([]string{"sub", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !vendorHelpCalled {
		t.Fatal("InstrumentCobra must not clobber the vendor's HelpFunc")
	}
	flush(t, client)

	if _, ok := eventAttr(srv, argvio.EventCommandInvoked, argvio.AttrCommandName); !ok {
		t.Fatal("expected our own capture to still fire alongside the vendor's chained hook")
	}
}

func TestInstrumentNilSafety(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("InstrumentCobra/Instrument panicked: %v", r)
		}
	}()
	InstrumentCobra(nil, nil)

	root := newSampleCLI(nil)
	InstrumentCobra(root, nil)
	root.SetArgs([]string{"sub"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute with nil client: %v", err)
	}
}

func TestWithoutRunCaptureDisablesExitCodeCapture(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newSampleCLI(nil)
	InstrumentCobra(root, client, WithoutRunCapture())

	root.SetArgs([]string{"sub"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	flush(t, client)

	if srv.Span(argvio.SpanCommandPrefix+"sub") != nil {
		t.Fatal("WithoutRunCapture should disable automatic exit-code capture")
	}
	if _, ok := eventAttr(srv, argvio.EventCommandInvoked, argvio.AttrCommandName); !ok {
		t.Fatal("command-invocation capture should be unaffected by WithoutRunCapture")
	}
}

func TestRecordExitCapturesCommandsThatExitThemselves(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newSampleCLI(nil)
	// Stands in for a command that would call os.Exit(3) after RecordExit.
	root.Commands()[0].RunE = func(cmd *cobra.Command, _ []string) error {
		RecordExit(cmd, client, 3)
		return nil
	}
	InstrumentCobra(root, client, WithoutRunCapture())

	root.SetArgs([]string{"sub", "--verbose"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	flush(t, client)

	span := srv.Span(argvio.SpanCommandPrefix + "sub")
	if span == nil {
		t.Fatal("expected RecordExit to record a cli.command.sub span")
	}
	if code := fakeotlp.Attr(span.GetAttributes(), argvio.AttrExitCode).GetIntValue(); code != 3 {
		t.Fatalf("expected exit code 3 on span, got %d", code)
	}
	if fakeotlp.Attr(span.GetAttributes(), argvio.AttrDurationMs) == nil {
		t.Fatal("expected a duration measured from the pre-run start time")
	}
}

func TestRecordExitNilSafety(t *testing.T) {
	RecordExit(nil, nil, 1)
	root := newSampleCLI(nil)
	root.SetContext(context.Background())
	RecordExit(root, nil, 1)
}
