package argvio

import "go.opentelemetry.io/otel/attribute"

// This file holds the two session facts only the vendor's own CLI knows: how
// far the user's configuration deviates from the defaults, and whether the
// user is signed in to the vendor's service. Both are optional, supplied
// through Options, and sent on the session start event at TierBasic+.
//
// The types are the privacy control: the count is an int and the auth state
// one of a fixed set, so there is no way to pass a key name, a value, a user
// name or a token through them.

// maxConfigCustomizedCount is the upper bound a reported count is clamped to,
// so a miscounting vendor can't send an absurd value.
const maxConfigCustomizedCount = 1000

// AuthState is whether the CLI's user is signed in to the vendor's own
// service, as the vendor's CLI determines it. It is a fixed vocabulary
// matching the server's allowlist for cli.auth.state; any other value is
// dropped.
type AuthState string

const (
	// AuthStateAuthenticated means the user is signed in.
	AuthStateAuthenticated AuthState = "authenticated"
	// AuthStateAnonymous means the user is not signed in.
	AuthStateAnonymous AuthState = "anonymous"
	// AuthStateUnknown means the CLI can't tell.
	AuthStateUnknown AuthState = "unknown"
)

func (s AuthState) valid() bool {
	switch s {
	case AuthStateAuthenticated, AuthStateAnonymous, AuthStateUnknown:
		return true
	default:
		return false
	}
}

// WithConfigCustomizedCount reports how many of the CLI's configuration keys
// the user has set to something other than the default, as
// cli.config.customized_count on the session start event at TierBasic+. Only
// the count is ever sent: never key names or values, which this API has no
// way to accept. Counts below 0 are sent as 0 and above 1000 as 1000.
//
// Not setting this (or WithConfigCustomizedCountFunc) sends nothing. If the
// count isn't known when the Client is built, use
// WithConfigCustomizedCountFunc instead.
func WithConfigCustomizedCount(n int) Option {
	return func(c *config) { c.configCount = func() int { return n } }
}

// WithConfigCustomizedCountFunc is WithConfigCustomizedCount with the count
// computed lazily: fn runs once, when RecordSessionStart is called, and only
// at TierBasic+, so it never runs for a user who hasn't consented. It runs on
// the caller's goroutine, so keep it fast and don't block in it. If fn
// panics, no count is sent and the host CLI is unaffected. A nil fn sends
// nothing. The last of the two options passed wins.
func WithConfigCustomizedCountFunc(fn func() int) Option {
	return func(c *config) { c.configCount = fn }
}

// WithAuthState reports whether the user is signed in to the vendor's
// service, as cli.auth.state on the session start event at TierBasic+. It
// must be one of the AuthState constants; any other value is dropped and
// nothing is sent. Nothing about the account (name, ID, token) is accepted.
//
// Not setting this (or WithAuthStateFunc) sends nothing. If the state isn't
// known when the Client is built, use WithAuthStateFunc instead.
func WithAuthState(state AuthState) Option {
	return func(c *config) { c.authState = func() AuthState { return state } }
}

// WithAuthStateFunc is WithAuthState with the state resolved lazily: fn runs
// once, when RecordSessionStart is called, and only at TierBasic+, so a
// vendor can read its credentials late and a user who hasn't consented never
// has them read on telemetry's behalf. It runs on the caller's goroutine, so
// keep it fast and don't block in it. If fn panics, or returns anything but
// an AuthState constant, nothing is sent and the host CLI is unaffected. A
// nil fn sends nothing. The last of the two options passed wins.
func WithAuthStateFunc(fn func() AuthState) Option {
	return func(c *config) { c.authState = fn }
}

// clampConfigCount limits a reported count to [0, maxConfigCustomizedCount].
func clampConfigCount(n int) int {
	return min(max(n, 0), maxConfigCustomizedCount)
}

// vendorSessionAttrs evaluates the vendor-supplied callbacks and returns the
// attributes for the session start event. A callback that panics, or a value
// outside the vocabulary, leaves its attribute out. Call it only at
// TierBasic+.
func (c *Client) vendorSessionAttrs() []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if c.configCount != nil {
		if n, ok := callSafely(c.configCount); ok {
			attrs = append(attrs, attribute.Int(AttrConfigCustomizedCount, clampConfigCount(n)))
		}
	}
	if c.authState != nil {
		if s, ok := callSafely(c.authState); ok && s.valid() {
			attrs = append(attrs, attribute.String(AttrAuthState, string(s)))
		}
	}
	return attrs
}

// callSafely runs a vendor callback, reporting false if it panicked so that
// telemetry can never break the host CLI.
func callSafely[T any](fn func() T) (v T, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			var zero T
			v, ok = zero, false
		}
	}()
	return fn(), true
}
