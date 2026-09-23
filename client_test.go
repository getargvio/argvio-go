package argvio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

func newTestClient(t *testing.T, tier Tier, opts ...Option) (*Client, *fakeotlp.Server) {
	t.Helper()
	srv, err := fakeotlp.Start()
	if err != nil {
		t.Fatalf("starting fake OTLP server: %v", err)
	}
	t.Cleanup(srv.Close)

	allOpts := append([]Option{
		WithEndpoint(srv.Addr()),
		WithInsecure(),
		WithDefaultTier(tier),
		WithExportTimeout(2 * time.Second),
	}, opts...)

	c, err := New("test-api-key", "testcli", "1.2.3", allOpts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	})
	return c, srv
}

func flush(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func allLogEventNames(srv *fakeotlp.Server) []string {
	var names []string
	for _, rl := range srv.Logs() {
		for _, scope := range rl.GetScopeLogs() {
			for _, rec := range scope.GetLogRecords() {
				names = append(names, rec.GetEventName())
			}
		}
	}
	return names
}

func logAttr(srv *fakeotlp.Server, eventName, key string) (string, bool) {
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

func TestClientAnonymousTierEmitsNoCommandPath(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	c.RecordCommandInvocation(ctx, "mycli sub", []string{"verbose"})
	c.RecordExitCode(ctx, "mycli sub", 0)
	flush(t, c)

	if _, ok := logAttr(srv, "cli.command_invocation", AttrCommandPath); ok {
		t.Fatal("TierAnonymous must not emit cli.command_invocation events at all")
	}
	if path, ok := logAttr(srv, "cli.exit_code", AttrCommandPath); ok {
		t.Fatalf("TierAnonymous must not include command path on exit code, got %q", path)
	}

	names := allLogEventNames(srv)
	for _, n := range names {
		if n == "cli.command_invocation" {
			t.Fatal("TierAnonymous must not emit a command_invocation log event")
		}
	}
}

func TestClientBasicTierIncludesCommandPathNoErrors(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)
	ctx := context.Background()

	c.RecordCommandInvocation(ctx, "mycli sub", []string{"verbose"})
	c.RecordError(ctx, "mycli sub", ErrorCategoryInternal)
	flush(t, c)

	path, ok := logAttr(srv, "cli.command_invocation", AttrCommandPath)
	if !ok || path != "mycli sub" {
		t.Fatalf("TierBasic should include command path, got (%q, %v)", path, ok)
	}

	for _, n := range allLogEventNames(srv) {
		if n == "cli.error" {
			t.Fatal("TierBasic must not emit cli.error events (requires TierFull)")
		}
	}
	if got := c.Stats().BelowTierDropped; got == 0 {
		t.Fatal("expected RecordError at TierBasic to be counted as dropped-below-tier")
	}
}

func TestClientFullTierEmitsRedactedError(t *testing.T) {
	c, srv := newTestClient(t, TierFull)
	ctx := context.Background()

	c.RecordError(ctx, "mycli sub", ErrorCategoryNetwork)
	flush(t, c)

	cat, ok := logAttr(srv, "cli.error", AttrErrorCategory)
	if !ok || cat != string(ErrorCategoryNetwork) {
		t.Fatalf("TierFull should emit cli.error with category, got (%q, %v)", cat, ok)
	}
	if _, ok := logAttr(srv, "cli.error", AttrErrorMessage); ok {
		t.Fatal("TierFull must never include a raw error message")
	}
}

func TestClientOptInGateRejectsBelowOptInTier(t *testing.T) {
	c, _ := newTestClient(t, TierFull)
	scope, ok := c.OptIn()
	if ok {
		t.Fatal("OptIn() must return ok=false when resolved tier is not TierOptIn")
	}
	// Must be safe to call even though ok was false.
	scope.RecordErrorDetail(context.Background(), "mycli sub", errors.New("secret detail"))
}

func TestClientOptInTierEmitsRawErrorMessage(t *testing.T) {
	c, srv := newTestClient(t, TierOptIn)
	scope, ok := c.OptIn()
	if !ok {
		t.Fatal("OptIn() should return ok=true at TierOptIn")
	}
	scope.RecordErrorDetail(context.Background(), "mycli sub", errors.New("stack overflow at line 42"))
	flush(t, c)

	msg, ok := logAttr(srv, "cli.error_detail", AttrErrorMessage)
	if !ok || msg != "stack overflow at line 42" {
		t.Fatalf("expected raw error message at TierOptIn, got (%q, %v)", msg, ok)
	}
}

func TestClientNeverEscalatesAboveConfiguredCeiling(t *testing.T) {
	// Even though RecordError/OptIn *could* be called by misbehaving
	// vendor code, the resolved tier is fixed at construction time and
	// every call site re-checks it — a Basic-tier client can never emit
	// Full or OptIn fields no matter what's requested.
	c, srv := newTestClient(t, TierBasic)
	ctx := context.Background()

	c.RecordError(ctx, "mycli sub", ErrorCategoryInternal)
	if scope, ok := c.OptIn(); ok {
		t.Fatal("OptIn must not succeed below TierOptIn")
	} else {
		scope.RecordErrorDetailWithStack(ctx, "mycli sub", errors.New("boom"), []byte("stack"))
	}
	flush(t, c)

	for _, n := range allLogEventNames(srv) {
		if n == "cli.error" || n == "cli.error_detail" {
			t.Fatalf("unexpected %s event emitted below its required tier", n)
		}
	}
}

func TestClientDisabledIsZeroNetworkCalls(t *testing.T) {
	srv, err := fakeotlp.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	c, err := New("key", "testcli", "1.0.0",
		WithEndpoint(srv.Addr()), WithInsecure(), WithDisabled(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Disabled() {
		t.Fatal("expected Disabled() to be true")
	}

	ctx := context.Background()
	c.RecordSessionStart(ctx)
	c.RecordCommandInvocation(ctx, "mycli sub", []string{"x"})
	c.RecordExitCode(ctx, "mycli sub", 1)
	c.RecordLatency(ctx, "mycli sub", time.Millisecond)
	c.RecordHelpFlagUsage(ctx, "mycli sub")
	c.RecordError(ctx, "mycli sub", ErrorCategoryInternal)
	if scope, ok := c.OptIn(); ok {
		t.Fatal("OptIn should never succeed on a disabled client")
	} else {
		scope.RecordErrorDetail(ctx, "mycli sub", errors.New("x"))
	}
	_ = c.Flush(ctx)
	if err := c.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown on disabled client should be a no-op success, got %v", err)
	}

	if len(srv.Traces()) != 0 || len(srv.Metrics()) != 0 || len(srv.Logs()) != 0 {
		t.Fatal("disabled client must perform zero network calls")
	}
}

func resourceAttr(srv *fakeotlp.Server, key string) (string, bool) {
	for _, rm := range srv.Metrics() {
		for _, kv := range rm.GetResource().GetAttributes() {
			if kv.GetKey() == key {
				return kv.GetValue().GetStringValue(), true
			}
		}
	}
	return "", false
}

func TestNewWithoutEndpointDegradesToDisabled(t *testing.T) {
	t.Setenv(otelEndpointEnvVar, "")
	c, err := New("key", "testcli", "1.0.0")
	if !errors.Is(err, errNoEndpoint) {
		t.Fatalf("expected errNoEndpoint, got %v", err)
	}
	if c == nil || !c.Disabled() {
		t.Fatal("New without an endpoint must return a disabled, non-nil client")
	}
}

func TestNewFallsBackToOTELEndpointEnv(t *testing.T) {
	srv, err := fakeotlp.Start()
	if err != nil {
		t.Fatalf("starting fake OTLP server: %v", err)
	}
	t.Cleanup(srv.Close)
	// The URL form the OTel spec uses; "http" implies no TLS.
	t.Setenv(otelEndpointEnvVar, "http://"+srv.Addr())

	c, err := New("key", "testcli", "1.0.0", WithExportTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	c.RecordSessionStart(context.Background())
	flush(t, c)

	if len(srv.Metrics()) == 0 {
		t.Fatal("expected telemetry to reach the endpoint from OTEL_EXPORTER_OTLP_ENDPOINT")
	}
}

func TestWithDisableOTELEnvFallbackIgnoresEnv(t *testing.T) {
	t.Setenv(otelEndpointEnvVar, "http://127.0.0.1:1")
	c, err := New("key", "testcli", "1.0.0", WithDisableOTELEnvFallback())
	if !errors.Is(err, errNoEndpoint) {
		t.Fatalf("expected errNoEndpoint, got %v", err)
	}
	if !c.Disabled() {
		t.Fatal("expected a disabled client when the env fallback is turned off")
	}
}

func TestHasScheme(t *testing.T) {
	for endpoint, want := range map[string]bool{
		"localhost:4317":                  false,
		"127.0.0.1:4317":                  false,
		"collector.example.com":           false,
		"http://localhost:4317":           true,
		"https://collector.example.com":   true,
		"https://collector.example.com/":  true,
		"grpc://collector.example.com:43": false,
	} {
		if got := hasScheme(endpoint); got != want {
			t.Errorf("hasScheme(%q) = %v, want %v", endpoint, got, want)
		}
	}
}

func TestClientDoNotTrackForcesDisabled(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")
	c, err := New("key", "testcli", "1.0.0", WithEndpoint("127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Disabled() {
		t.Fatal("DO_NOT_TRACK=1 must force the client into disabled mode")
	}
}

func TestNilClientMethodsNeverPanic(t *testing.T) {
	var c *Client
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil *Client method panicked: %v", r)
		}
	}()

	c.RecordSessionStart(ctx)
	c.RecordSessionEnd(ctx)
	c.RecordCommandInvocation(ctx, "x", nil)
	c.RecordExitCode(ctx, "x", 1)
	c.RecordLatency(ctx, "x", time.Second)
	c.RecordHelpFlagUsage(ctx, "x")
	c.RecordError(ctx, "x", ErrorCategoryUnknown)
	_ = c.Tier()
	_ = c.Disabled()
	_ = c.Stats()
	_ = c.Flush(ctx)
	_ = c.Shutdown(ctx)
	if scope, ok := c.OptIn(); ok {
		t.Fatal("nil client OptIn must return ok=false")
	} else {
		scope.RecordErrorDetail(ctx, "x", errors.New("y"))
	}
}

func TestQueueOverflowDropsWithoutBlocking(t *testing.T) {
	c, _ := newTestClient(t, TierFull, WithQueueSize(1))
	ctx := context.Background()

	// Fire far more records than the queue can hold; none of these
	// calls may block regardless of how slow (or absent) the consumer
	// is.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			c.RecordExitCode(ctx, "mycli", i%2)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RecordExitCode calls blocked under queue pressure")
	}
}
