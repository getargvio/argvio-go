package argvio

import (
	"context"
	"strings"
	"testing"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

func unknownRecord(t *testing.T, srv *fakeotlp.Server) *logspb.LogRecord {
	t.Helper()
	rec := srv.LogRecord(EventCommandUnknown)
	if rec == nil {
		t.Fatalf("no %s event exported; got %v", EventCommandUnknown, allLogEventNames(srv))
	}
	return rec
}

func attrKeys(rec *logspb.LogRecord) map[string]bool {
	keys := map[string]bool{}
	for _, kv := range rec.GetAttributes() {
		keys[kv.GetKey()] = true
	}
	return keys
}

func TestRecordUnknownCommandSendsSuggestionAtBasicTier(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)
	c.SetKnownCommands("testcli deploy push", "testcli deploy")

	c.RecordUnknownCommand(context.Background(), "testcli deploy push")
	flush(t, c)

	rec := unknownRecord(t, srv)
	if got := fakeotlp.Attr(rec.GetAttributes(), AttrSuggestion).GetStringValue(); got != "deploy.push" {
		t.Fatalf("suggestion = %q, want deploy.push", got)
	}
	if got := fakeotlp.Attr(rec.GetAttributes(), ResourceAttrTier).GetStringValue(); got != "basic" {
		t.Fatalf("tier = %q, want basic", got)
	}
}

func TestRecordUnknownCommandAcceptsDottedSuggestion(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)
	c.SetKnownCommands("testcli deploy push")

	c.RecordUnknownCommand(context.Background(), "deploy.push")
	flush(t, c)

	if got, _ := logAttr(srv, EventCommandUnknown, AttrSuggestion); got != "deploy.push" {
		t.Fatalf("suggestion = %q, want deploy.push", got)
	}
}

func TestRecordUnknownCommandAnonymousTierIsABareAttempt(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	c.SetKnownCommands("testcli deploy")

	c.RecordUnknownCommand(context.Background(), "deploy")
	flush(t, c)

	rec := unknownRecord(t, srv)
	if _, ok := logAttr(srv, EventCommandUnknown, AttrSuggestion); ok {
		t.Fatal("suggestion sent at anonymous tier")
	}
	if got := fakeotlp.Attr(rec.GetAttributes(), ResourceAttrTier).GetStringValue(); got != "anonymous" {
		t.Fatalf("tier = %q, want anonymous", got)
	}
}

func TestRecordUnknownCommandOnlyEverCarriesTheAllowlistedAttributes(t *testing.T) {
	for _, tier := range []Tier{TierAnonymous, TierBasic, TierFull, TierOptIn} {
		c, srv := newTestClient(t, tier)
		c.SetKnownCommands("testcli deploy")
		c.RecordSessionStart(context.Background())
		c.RecordCommandInvocation(context.Background(), "testcli deploy", []string{"force"})

		c.RecordUnknownCommand(context.Background(), "deploy")
		flush(t, c)

		rec := unknownRecord(t, srv)
		for key := range attrKeys(rec) {
			switch key {
			case AttrSuggestion, ResourceAttrTier:
			default:
				t.Errorf("tier %v: unexpected attribute %q on %s", tier, key, EventCommandUnknown)
			}
		}
		if body := rec.GetBody().GetStringValue(); body != "" {
			t.Errorf("tier %v: body = %q, want empty", tier, body)
		}
	}
}

func TestRecordUnknownCommandDropsUnregisteredSuggestions(t *testing.T) {
	typed := []string{
		"",
		"deplooy",
		"/home/alice/.ssh/id_rsa",
		"ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"deploy push --token=hunter2",
		"deplo", // a fragment of a known command is not a known command
	}
	for _, s := range typed {
		c, srv := newTestClient(t, TierBasic)
		c.SetKnownCommands("testcli deploy")

		c.RecordUnknownCommand(context.Background(), s)
		flush(t, c)

		unknownRecord(t, srv)
		if got, ok := logAttr(srv, EventCommandUnknown, AttrSuggestion); ok {
			t.Errorf("suggestion %q was sent as %q", s, got)
		}
	}
}

func TestRecordUnknownCommandWithoutRegisteredCommandsSendsNoSuggestion(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)

	c.RecordUnknownCommand(context.Background(), "deploy")
	flush(t, c)

	unknownRecord(t, srv)
	if _, ok := logAttr(srv, EventCommandUnknown, AttrSuggestion); ok {
		t.Fatal("suggestion sent although no command was registered")
	}
}

func TestRecordUnknownCommandVeryLongSuggestionIsDropped(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)
	c.SetKnownCommands("testcli deploy")

	c.RecordUnknownCommand(context.Background(), "deploy"+strings.Repeat("x", 1<<20))
	flush(t, c)

	unknownRecord(t, srv)
	if _, ok := logAttr(srv, EventCommandUnknown, AttrSuggestion); ok {
		t.Fatal("over-long suggestion was sent")
	}
}

func TestSetKnownCommandsReplacesThePreviousSet(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)
	c.SetKnownCommands("testcli old")
	c.SetKnownCommands("testcli new")

	c.RecordUnknownCommand(context.Background(), "old")
	flush(t, c)

	unknownRecord(t, srv)
	if _, ok := logAttr(srv, EventCommandUnknown, AttrSuggestion); ok {
		t.Fatal("suggestion from a replaced set was sent")
	}
}

func TestRecordUnknownCommandIsNotAnInvocation(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)

	c.RecordUnknownCommand(context.Background(), "")
	flush(t, c)

	for _, name := range allLogEventNames(srv) {
		if name != EventCommandUnknown {
			t.Errorf("unexpected event %q", name)
		}
	}
	for _, rm := range srv.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				t.Errorf("an unknown command produced metric %q", m.GetName())
			}
		}
	}
	if len(srv.Traces()) != 0 {
		t.Error("an unknown command produced a span")
	}
}

func TestRecordUnknownCommandDisabledAndNilAreNoOps(t *testing.T) {
	var nilClient *Client
	nilClient.SetKnownCommands("x")
	nilClient.RecordUnknownCommand(context.Background(), "x")

	c, srv := newTestClient(t, TierBasic, WithDisabled())
	c.SetKnownCommands("testcli deploy")
	c.RecordUnknownCommand(context.Background(), "deploy")
	flush(t, c)

	if names := allLogEventNames(srv); len(names) != 0 {
		t.Fatalf("disabled client emitted %v", names)
	}
}
