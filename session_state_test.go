package argvio

import (
	"context"
	"testing"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

// startSession records a session start and returns the count and auth state
// attributes of the resulting event, nil when absent.
func startSession(t *testing.T, tier Tier, opts ...Option) (count *int64, auth *string) {
	t.Helper()
	c, srv := newTestClient(t, tier, opts...)
	c.RecordSessionStart(context.Background())
	flush(t, c)

	rec := srv.LogRecord(EventSessionStarted)
	if rec == nil {
		t.Fatal("expected a session started event")
	}
	if v := fakeotlp.Attr(rec.GetAttributes(), AttrConfigCustomizedCount); v != nil {
		n := v.GetIntValue()
		count = &n
	}
	if v := fakeotlp.Attr(rec.GetAttributes(), AttrAuthState); v != nil {
		s := v.GetStringValue()
		auth = &s
	}
	return count, auth
}

func TestSessionStateWireNames(t *testing.T) {
	if AttrConfigCustomizedCount != "cli.config.customized_count" {
		t.Errorf("AttrConfigCustomizedCount = %q", AttrConfigCustomizedCount)
	}
	if AttrAuthState != "cli.auth.state" {
		t.Errorf("AttrAuthState = %q", AttrAuthState)
	}
	for state, want := range map[AuthState]string{
		AuthStateAuthenticated: "authenticated",
		AuthStateAnonymous:     "anonymous",
		AuthStateUnknown:       "unknown",
	} {
		if string(state) != want {
			t.Errorf("AuthState %q, want %q", state, want)
		}
	}
}

func TestSessionStateUnsetSendsNothing(t *testing.T) {
	for _, tier := range []Tier{TierAnonymous, TierBasic} {
		count, auth := startSession(t, tier)
		if count != nil || auth != nil {
			t.Errorf("%s: unset options sent count=%v auth=%v", tier, count, auth)
		}
	}
}

func TestSessionStateSentAtBasicAndAbove(t *testing.T) {
	for _, tier := range []Tier{TierBasic, TierFull, TierOptIn} {
		t.Run(tier.String(), func(t *testing.T) {
			count, auth := startSession(t, tier,
				WithConfigCustomizedCount(7), WithAuthState(AuthStateAuthenticated))
			if count == nil || *count != 7 {
				t.Errorf("count = %v, want 7", count)
			}
			if auth == nil || *auth != "authenticated" {
				t.Errorf("auth = %v, want authenticated", auth)
			}
		})
	}
}

func TestSessionStateGatedAtAnonymous(t *testing.T) {
	configCalls, authCalls := 0, 0
	count, auth := startSession(t, TierAnonymous,
		WithConfigCustomizedCount(7),
		WithAuthState(AuthStateAuthenticated),
		WithConfigCustomizedCountFunc(func() int { configCalls++; return 7 }),
		WithAuthStateFunc(func() AuthState { authCalls++; return AuthStateAuthenticated }),
	)
	if count != nil || auth != nil {
		t.Errorf("TierAnonymous sent count=%v auth=%v", count, auth)
	}
	if configCalls != 0 || authCalls != 0 {
		t.Errorf("callbacks ran below TierBasic (config %d, auth %d)", configCalls, authCalls)
	}
}

func TestSessionStateCallbacksRunOncePerSessionStart(t *testing.T) {
	configCalls, authCalls := 0, 0
	c, srv := newTestClient(t, TierBasic,
		WithConfigCustomizedCountFunc(func() int { configCalls++; return 3 }),
		WithAuthStateFunc(func() AuthState { authCalls++; return AuthStateAnonymous }),
	)
	if configCalls != 0 || authCalls != 0 {
		t.Fatalf("callbacks ran at construction (config %d, auth %d)", configCalls, authCalls)
	}
	c.RecordSessionStart(context.Background())
	c.RecordSessionStart(context.Background())
	flush(t, c)

	if configCalls != 1 || authCalls != 1 {
		t.Errorf("callbacks ran config %d, auth %d times, want 1 each", configCalls, authCalls)
	}
	rec := srv.LogRecord(EventSessionStarted)
	if v := fakeotlp.Attr(rec.GetAttributes(), AttrConfigCustomizedCount); v == nil || v.GetIntValue() != 3 {
		t.Errorf("count = %v, want 3", v)
	}
	if v := fakeotlp.Attr(rec.GetAttributes(), AttrAuthState); v == nil || v.GetStringValue() != "anonymous" {
		t.Errorf("auth = %v, want anonymous", v)
	}
}

func TestSessionStatePanickingCallbacksAreSwallowed(t *testing.T) {
	c, srv := newTestClient(t, TierBasic,
		WithConfigCustomizedCountFunc(func() int { panic("boom") }),
		WithAuthStateFunc(func() AuthState { panic(42) }),
	)
	c.RecordSessionStart(context.Background())
	flush(t, c)

	rec := srv.LogRecord(EventSessionStarted)
	if rec == nil {
		t.Fatal("a panicking callback must not suppress the session start event")
	}
	for _, key := range []string{AttrConfigCustomizedCount, AttrAuthState} {
		if fakeotlp.Attr(rec.GetAttributes(), key) != nil {
			t.Errorf("%s sent although its callback panicked", key)
		}
	}
	if got := c.Stats().RecoveredPanics; got != 0 {
		t.Errorf("RecoveredPanics = %d: callback panics must be handled before the outer guard", got)
	}
	// The rest of the event is intact.
	if fakeotlp.Attr(rec.GetAttributes(), AttrSessionID) == nil {
		t.Error("session ID missing from the event")
	}
}

func TestSessionStateOnePanickingCallbackKeepsTheOther(t *testing.T) {
	count, auth := startSession(t, TierBasic,
		WithConfigCustomizedCountFunc(func() int { panic("boom") }),
		WithAuthState(AuthStateUnknown),
	)
	if count != nil {
		t.Errorf("count = %d, want absent", *count)
	}
	if auth == nil || *auth != "unknown" {
		t.Errorf("auth = %v, want unknown", auth)
	}
}

func TestSessionStateConfigCountIsClamped(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int64
	}{
		{-5, 0},
		{0, 0},
		{1, 1},
		{maxConfigCustomizedCount, maxConfigCustomizedCount},
		{maxConfigCustomizedCount + 1, maxConfigCustomizedCount},
		{1 << 40, maxConfigCustomizedCount},
	} {
		count, _ := startSession(t, TierBasic, WithConfigCustomizedCount(tc.in))
		if count == nil || *count != tc.want {
			t.Errorf("count %d sent as %v, want %d", tc.in, count, tc.want)
		}
	}
}

func TestSessionStateZeroCountIsSent(t *testing.T) {
	count, _ := startSession(t, TierBasic, WithConfigCustomizedCount(0))
	if count == nil || *count != 0 {
		t.Errorf("a count of 0 is a real measurement, got %v", count)
	}
}

func TestSessionStateInvalidAuthStateIsDropped(t *testing.T) {
	for _, s := range []AuthState{"", "Authenticated", "admin@example.com", "token-abc"} {
		_, auth := startSession(t, TierBasic, WithAuthState(s))
		if auth != nil {
			t.Errorf("auth state %q sent as %q", s, *auth)
		}
		_, auth = startSession(t, TierBasic, WithAuthStateFunc(func() AuthState { return s }))
		if auth != nil {
			t.Errorf("auth state %q from callback sent as %q", s, *auth)
		}
	}
}

func TestSessionStateNilCallbacksSendNothing(t *testing.T) {
	count, auth := startSession(t, TierBasic,
		WithConfigCustomizedCountFunc(nil), WithAuthStateFunc(nil))
	if count != nil || auth != nil {
		t.Errorf("nil callbacks sent count=%v auth=%v", count, auth)
	}
}

func TestSessionStateLastOptionWins(t *testing.T) {
	count, auth := startSession(t, TierBasic,
		WithConfigCustomizedCount(1), WithConfigCustomizedCountFunc(func() int { return 2 }),
		WithAuthStateFunc(func() AuthState { return AuthStateAnonymous }), WithAuthState(AuthStateAuthenticated),
	)
	if count == nil || *count != 2 {
		t.Errorf("count = %v, want 2", count)
	}
	if auth == nil || *auth != "authenticated" {
		t.Errorf("auth = %v, want authenticated", auth)
	}
}

func TestSessionStateOnDisabledClient(t *testing.T) {
	c, err := New("key", "testcli", "1.0.0", WithDisabled(),
		WithConfigCustomizedCountFunc(func() int { t.Error("callback ran on a disabled client"); return 1 }),
		WithAuthStateFunc(func() AuthState { t.Error("callback ran on a disabled client"); return AuthStateUnknown }),
	)
	if err != nil {
		t.Fatal(err)
	}
	c.RecordSessionStart(context.Background())
}
