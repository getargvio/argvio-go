package argvio

import "context"

// ConsentProvider resolves the consent tier that should be used for the
// current process. Implementations are consulted once per Client
// (cached for the process lifetime — consent is not expected to change
// mid-invocation of a short-lived CLI command).
//
// Implementations must not panic and must not block indefinitely; New
// applies an internal timeout when calling Resolve. A ConsentProvider
// that errors or times out causes the Client to fail closed to
// TierAnonymous (see StaticProvider for a ceiling override).
type ConsentProvider interface {
	// Resolve returns the consent tier to use. An error indicates the
	// provider could not determine a tier (e.g. missing/corrupt config);
	// the Client treats that identically to TierAnonymous.
	Resolve(ctx context.Context) (Tier, error)
}

// StaticProvider is a ConsentProvider that always returns a fixed tier.
// Useful for tests, for vendors who resolve consent themselves before
// constructing the Client, or as a fallback wrapped by ChainProviders.
type StaticProvider struct {
	Tier Tier
}

// Resolve implements ConsentProvider.
func (p StaticProvider) Resolve(context.Context) (Tier, error) {
	if !p.Tier.valid() {
		return TierAnonymous, nil
	}
	return p.Tier, nil
}

// DisabledProvider is a ConsentProvider that always resolves to
// TierAnonymous. It exists as an explicit, self-documenting choice
// distinct from "a provider that happens to return anonymous".
type DisabledProvider struct{}

// Resolve implements ConsentProvider.
func (DisabledProvider) Resolve(context.Context) (Tier, error) {
	return TierAnonymous, nil
}

// ChainProviders returns a ConsentProvider that tries each provider in
// order and returns the first successful (err == nil) result. If all
// providers error, it returns TierAnonymous. This lets a vendor compose,
// e.g., "env var override, then config file, then interactive prompt,
// then anonymous default" without writing that logic themselves.
func ChainProviders(providers ...ConsentProvider) ConsentProvider {
	return chainProvider{providers: providers}
}

type chainProvider struct {
	providers []ConsentProvider
}

func (c chainProvider) Resolve(ctx context.Context) (tier Tier, err error) {
	defer func() {
		if r := recover(); r != nil {
			tier, err = TierAnonymous, errRecoveredPanic
		}
	}()
	for _, p := range c.providers {
		if p == nil {
			continue
		}
		if t, e := p.Resolve(ctx); e == nil {
			return t, nil
		}
	}
	return TierAnonymous, errNoProviderResolved
}
