package argvio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

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
		WithInstallIDPath(filepath.Join(t.TempDir(), "install-id")),
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

func TestClientAnonymousTierEmitsNoIdentifiers(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	c.RecordSessionStart(ctx)
	c.RecordCommandInvocation(ctx, "testcli sub", []string{"verbose"})
	c.RecordCommand(ctx, CommandResult{Path: "testcli sub", FlagNames: []string{"verbose"}, Duration: time.Millisecond})
	flush(t, c)

	if name, ok := logAttr(srv, EventCommandInvoked, AttrCommandName); !ok || name != "sub" {
		t.Fatalf("command name is allowed at TierAnonymous, got (%q, %v)", name, ok)
	}
	if srv.LogRecord(EventCommandCompleted) == nil {
		t.Fatal("expected a completed event at TierAnonymous")
	}
	for _, key := range []string{AttrSessionID, AttrInstallID} {
		if _, ok := logAttr(srv, EventCommandCompleted, key); ok {
			t.Fatalf("TierAnonymous must not include %s on the completed event", key)
		}
	}
	for _, key := range []string{AttrFlagNames, AttrSessionID} {
		if _, ok := logAttr(srv, EventCommandInvoked, key); ok {
			t.Fatalf("TierAnonymous must not include %s", key)
		}
	}
	for _, key := range []string{AttrSessionID, AttrInstallID} {
		if _, ok := logAttr(srv, EventSessionStarted, key); ok {
			t.Fatalf("TierAnonymous must not include %s on session start", key)
		}
	}
	span := srv.Span(SpanCommandPrefix + "sub")
	if span == nil {
		t.Fatal("expected a command span at TierAnonymous")
	}
	if fakeotlp.Attr(span.GetAttributes(), AttrFlagNames) != nil {
		t.Fatal("TierAnonymous must not include flag names on the command span")
	}
}

func TestClientBasicTierIncludesIdentifiersAndErrorCategory(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)
	ctx := context.Background()

	c.RecordSessionStart(ctx)
	c.RecordCommandInvocation(ctx, "mycli sub", []string{"verbose"})
	c.RecordCommand(ctx, CommandResult{Path: "mycli sub", Duration: 1500 * time.Millisecond, ExitCode: 3})
	c.RecordError(ctx, "mycli sub", ErrorCategoryInternal)
	flush(t, c)

	installID, ok := logAttr(srv, EventSessionStarted, AttrInstallID)
	if !ok || !validInstallID(installID) {
		t.Fatalf("TierBasic should include an install ID, got (%q, %v)", installID, ok)
	}
	sessionID, ok := logAttr(srv, EventSessionStarted, AttrSessionID)
	if !ok || sessionID == "" {
		t.Fatal("TierBasic should include a session ID on session start")
	}
	if got, _ := logAttr(srv, EventCommandInvoked, AttrSessionID); got != sessionID {
		t.Fatalf("expected the same session ID on invocation, got %q want %q", got, sessionID)
	}
	if rec := srv.LogRecord(EventCommandInvoked); fakeotlp.Attr(rec.GetAttributes(), AttrFlagNames) == nil {
		t.Fatal("TierBasic should include flag names")
	}

	if got, _ := logAttr(srv, EventCommandInvoked, AttrInstallID); got != installID {
		t.Fatalf("expected the install ID on invocation, got %q want %q", got, installID)
	}

	// The completed event and the error event both tie back to the session
	// and install, so the server can follow one install across commands.
	for _, event := range []string{EventCommandCompleted, EventErrorRaised} {
		if got, _ := logAttr(srv, event, AttrSessionID); got != sessionID {
			t.Fatalf("%s: session ID = %q, want %q", event, got, sessionID)
		}
		if got, _ := logAttr(srv, event, AttrInstallID); got != installID {
			t.Fatalf("%s: install ID = %q, want %q", event, got, installID)
		}
	}
	rec := srv.LogRecord(EventCommandCompleted)
	if got := fakeotlp.Attr(rec.GetAttributes(), AttrExitCode).GetIntValue(); got != 3 {
		t.Fatalf("completed exit code = %d, want 3", got)
	}
	if got := fakeotlp.Attr(rec.GetAttributes(), AttrDurationMs).GetIntValue(); got != 1500 {
		t.Fatalf("completed duration = %d, want 1500", got)
	}

	if errType, ok := logAttr(srv, EventErrorRaised, AttrErrorType); !ok || errType != string(ErrorCategoryInternal) {
		t.Fatalf("TierBasic should emit the error category, got (%q, %v)", errType, ok)
	}
	if _, ok := logAttr(srv, EventErrorRaised, AttrErrorMessage); ok {
		t.Fatal("TierBasic must never include a raw error message")
	}
}

func TestClientInstallIDIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-id")
	first := loadOrCreateInstallID(path)
	if !validInstallID(first) {
		t.Fatalf("expected a UUID, got %q", first)
	}
	if again := loadOrCreateInstallID(path); again != first {
		t.Fatalf("install ID changed across runs: %q then %q", first, again)
	}
	if err := os.WriteFile(path, []byte("not-a-uuid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if replaced := loadOrCreateInstallID(path); replaced == first || !validInstallID(replaced) {
		t.Fatalf("expected a corrupt install ID to be replaced, got %q", replaced)
	}
	if id := loadOrCreateInstallID(filepath.Join(path, "under-a-file")); id != "" {
		t.Fatalf("expected no install ID when it can't be persisted, got %q", id)
	}
}

func TestClientSessionStartEmitsOnce(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)
	ctx := context.Background()

	c.RecordSessionStart(ctx)
	c.RecordSessionStart(ctx)
	flush(t, c)

	count := 0
	for _, n := range allLogEventNames(srv) {
		if n == EventSessionStarted {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one session start event, got %d", count)
	}
}

func TestClientCommandSpan(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)
	start := time.Now().Add(-time.Second)

	c.RecordCommand(context.Background(), CommandResult{
		Path: "testcli deploy push", FlagNames: []string{"force"}, Start: start, Duration: 250 * time.Millisecond, ExitCode: 2,
	})
	flush(t, c)

	span := srv.Span(SpanCommandPrefix + "deploy.push")
	if span == nil {
		t.Fatal("expected a cli.command.deploy.push span")
	}
	attrs := span.GetAttributes()
	if got := fakeotlp.Attr(attrs, AttrCommandName).GetStringValue(); got != "deploy.push" {
		t.Errorf("command name = %q, want %q", got, "deploy.push")
	}
	if got := fakeotlp.Attr(attrs, AttrExitCode).GetIntValue(); got != 2 {
		t.Errorf("exit code = %d, want 2", got)
	}
	if got := fakeotlp.Attr(attrs, AttrDurationMs).GetIntValue(); got != 250 {
		t.Errorf("duration_ms = %d, want 250", got)
	}
	if got := time.Unix(0, int64(span.GetEndTimeUnixNano())).Sub(time.Unix(0, int64(span.GetStartTimeUnixNano()))); got != 250*time.Millisecond { //nolint:gosec // test timestamps fit in int64
		t.Errorf("span duration = %v, want 250ms", got)
	}
	if span.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Error("expected error status for a non-zero exit code")
	}
}

func TestClientCommandName(t *testing.T) {
	c := &Client{cliName: "mycli"}
	tests := map[string]string{
		"mycli deploy push": "deploy.push",
		"mycli":             "mycli",
		"":                  "mycli",
		"other sub":         "other.sub",
	}
	for path, want := range tests {
		if got := c.commandName(path); got != want {
			t.Errorf("commandName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestClientFullTierEmitsRedactedError(t *testing.T) {
	c, srv := newTestClient(t, TierFull)
	ctx := context.Background()

	c.RecordError(ctx, "mycli sub", ErrorCategoryNetwork)
	flush(t, c)

	errType, ok := logAttr(srv, EventErrorRaised, AttrErrorType)
	if !ok || errType != string(ErrorCategoryNetwork) {
		t.Fatalf("TierFull should emit cli.error.raised with the error type, got (%q, %v)", errType, ok)
	}
	if _, ok := logAttr(srv, EventErrorRaised, AttrErrorMessage); ok {
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

	msg, ok := logAttr(srv, EventErrorRaised, AttrErrorMessage)
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

	// The category event is allowed at TierBasic; raw detail is not.
	for _, key := range []string{AttrErrorMessage, AttrErrorStack} {
		if _, ok := logAttr(srv, EventErrorRaised, key); ok {
			t.Fatalf("%s emitted below TierOptIn", key)
		}
	}
}

func TestClientInstallMethodIsReportedAtEveryTier(t *testing.T) {
	for _, tier := range []Tier{TierAnonymous, TierBasic} {
		c, srv := newTestClient(t, tier, WithInstallMethod("brew"))
		c.RecordSessionStart(context.Background())
		flush(t, c)

		if got, ok := logAttr(srv, EventSessionStarted, AttrInstallMethod); !ok || got != "brew" {
			t.Fatalf("%s: install method = (%q, %v), want brew", tier, got, ok)
		}
	}

	c, srv := newTestClient(t, TierBasic)
	c.RecordSessionStart(context.Background())
	flush(t, c)
	if _, ok := logAttr(srv, EventSessionStarted, AttrInstallMethod); ok {
		t.Fatal("install method must be absent unless WithInstallMethod is set")
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
	c.RecordCommand(ctx, CommandResult{Path: "mycli sub", Duration: time.Millisecond, ExitCode: 1})
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
	v := resourceAttrValue(srv, key)
	return v.GetStringValue(), v != nil
}

// resourceAttrValue returns key from the resource of any exported signal.
func resourceAttrValue(srv *fakeotlp.Server, key string) *commonpb.AnyValue {
	var resources []*resourcepb.Resource
	for _, r := range srv.Metrics() {
		resources = append(resources, r.GetResource())
	}
	for _, r := range srv.Logs() {
		resources = append(resources, r.GetResource())
	}
	for _, r := range srv.Traces() {
		resources = append(resources, r.GetResource())
	}
	for _, res := range resources {
		if v := fakeotlp.Attr(res.GetAttributes(), key); v != nil {
			return v
		}
	}
	return nil
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
	c.RecordCommandInvocation(ctx, "x", nil)
	c.RecordCommand(ctx, CommandResult{Path: "x", ExitCode: 1})
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
			c.RecordCommand(ctx, CommandResult{Path: "mycli", ExitCode: i % 2})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RecordCommand calls blocked under queue pressure")
	}
}
