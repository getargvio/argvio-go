package argvio

import "sync/atomic"

// defaultClient is the Client package-level handles (NewCounter, NewDuration,
// NewValue) record on. It is nil until New or SetDefault sets it.
var defaultClient atomic.Pointer[Client]

// SetDefault makes c the Client that package-level handles record on,
// replacing any earlier default. New does this for the Client it returns
// unless WithoutDefault is passed, so SetDefault is mostly for tests. A nil c
// clears the default, which makes every package-level handle a no-op. Safe
// for concurrent use.
func SetDefault(c *Client) {
	defaultClient.Store(c)
}

// Default returns the Client package-level handles record on, or nil when
// there is none.
func Default() *Client {
	return defaultClient.Load()
}

// WithoutDefault stops New from registering the Client it returns as the
// default one. Use it in libraries, and when a program has several Clients,
// so that creating one doesn't redirect the package-level handles.
func WithoutDefault() Option {
	return func(c *config) { c.noDefault = true }
}

// NewCounter returns a Counter that is not tied to a Client: it records on the
// default Client (see SetDefault), looked up on every call, so it can be
// declared at package level before New runs and follows the default if it
// changes:
//
//	var vulnsFound = argvio.NewCounter("vulns.found", argvio.Dims("severity", "check"))
//
//	vulnsFound.Add(ctx, 1, "high", "bola")
//
// It does nothing while there is no default Client or the default is
// disabled. Otherwise it behaves exactly like Client.Counter, including the
// rules for names, keys and values and the anonymous-tier warning. Safe for
// concurrent use.
func NewCounter(name string, opts ...MetricOption) *Counter {
	h := newMetricHandle(nil, name, opts)
	h.lazy = true
	return &Counter{h: h}
}

// NewDuration is NewCounter for a Duration; see Client.Duration.
//
//	var scanTime = argvio.NewDuration("scan.time", argvio.Dims("kind"))
//
//	defer scanTime.Start(ctx, "openapi")()
func NewDuration(name string, opts ...MetricOption) *Duration {
	h := newMetricHandle(nil, name, opts)
	h.lazy = true
	return &Duration{h: h}
}

// NewValue is NewCounter for a Value; see Client.Value.
//
//	var opsInSpec = argvio.NewValue("spec.operations", argvio.Dims(), argvio.WithUnit(argvio.UnitCount))
func NewValue(name string, opts ...MetricOption) *Value {
	v := newValue(nil, name, opts)
	v.h.lazy = true
	return v
}
