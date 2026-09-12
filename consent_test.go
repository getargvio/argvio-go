package argvio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticProvider(t *testing.T) {
	p := StaticProvider{Tier: TierFull}
	tier, err := p.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tier != TierFull {
		t.Fatalf("got %v, want TierFull", tier)
	}
}

func TestStaticProviderInvalidTierFailsClosed(t *testing.T) {
	p := StaticProvider{Tier: Tier(99)}
	tier, err := p.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tier != TierAnonymous {
		t.Fatalf("invalid static tier should resolve to TierAnonymous, got %v", tier)
	}
}

func TestDisabledProvider(t *testing.T) {
	tier, err := DisabledProvider{}.Resolve(context.Background())
	if err != nil || tier != TierAnonymous {
		t.Fatalf("got (%v, %v), want (TierAnonymous, nil)", tier, err)
	}
}

type errProvider struct{}

func (errProvider) Resolve(context.Context) (Tier, error) {
	return TierOptIn, errors.New("boom")
}

type panicProvider struct{}

func (panicProvider) Resolve(context.Context) (Tier, error) {
	panic("provider bug")
}

func TestChainProvidersFirstSuccessWins(t *testing.T) {
	chain := ChainProviders(errProvider{}, StaticProvider{Tier: TierBasic}, StaticProvider{Tier: TierFull})
	tier, err := chain.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tier != TierBasic {
		t.Fatalf("got %v, want TierBasic (first successful provider)", tier)
	}
}

func TestChainProvidersAllFailReturnsAnonymous(t *testing.T) {
	chain := ChainProviders(errProvider{}, errProvider{})
	tier, err := chain.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error when every provider fails")
	}
	if tier != TierAnonymous {
		t.Fatalf("got %v, want TierAnonymous", tier)
	}
}

func TestChainProvidersRecoversPanic(t *testing.T) {
	chain := ChainProviders(panicProvider{})
	tier, err := chain.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error after recovering panic")
	}
	if tier != TierAnonymous {
		t.Fatalf("got %v, want TierAnonymous", tier)
	}
}

func TestChainProvidersSkipsNil(t *testing.T) {
	chain := ChainProviders(nil, StaticProvider{Tier: TierFull})
	tier, err := chain.Resolve(context.Background())
	if err != nil || tier != TierFull {
		t.Fatalf("got (%v, %v), want (TierFull, nil)", tier, err)
	}
}

func TestFileConsentProviderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consent.json")
	p := NewFileConsentProvider("testcli", WithConfigPath(path))

	if _, err := p.Resolve(context.Background()); err == nil {
		t.Fatal("expected error reading nonexistent consent file")
	}

	if err := p.Store(TierFull); err != nil {
		t.Fatal(err)
	}
	tier, err := p.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tier != TierFull {
		t.Fatalf("got %v, want TierFull", tier)
	}
}

func TestFileConsentProviderMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consent.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewFileConsentProvider("testcli", WithConfigPath(path))
	if _, err := p.Resolve(context.Background()); err == nil {
		t.Fatal("expected error for malformed consent file")
	}
}

func TestFileConsentProviderUnknownTierValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "consent.json")
	if err := os.WriteFile(path, []byte(`{"tier":"super-admin"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewFileConsentProvider("testcli", WithConfigPath(path))
	if _, err := p.Resolve(context.Background()); err == nil {
		t.Fatal("expected error for unknown tier value")
	}
}

func TestFileConsentProviderStoreRejectsInvalidTier(t *testing.T) {
	dir := t.TempDir()
	p := NewFileConsentProvider("testcli", WithConfigPath(filepath.Join(dir, "consent.json")))
	if err := p.Store(Tier(99)); err == nil {
		t.Fatal("expected Store to reject invalid tier")
	}
}

func TestFileConsentProviderNilSafe(t *testing.T) {
	var p *FileConsentProvider
	if _, err := p.Resolve(context.Background()); err == nil {
		t.Fatal("nil FileConsentProvider should error, not panic")
	}
	if err := p.Store(TierBasic); err == nil {
		t.Fatal("nil FileConsentProvider Store should error, not panic")
	}
	if p.Path() != "" {
		t.Fatal("nil FileConsentProvider Path should be empty")
	}
}

func TestDefaultConsentPathNamespacedByCLIName(t *testing.T) {
	p1 := NewFileConsentProvider("cli-one")
	p2 := NewFileConsentProvider("cli-two")
	if p1.Path() == "" || p2.Path() == "" {
		t.Skip("os.UserConfigDir unavailable in this environment")
	}
	if p1.Path() == p2.Path() {
		t.Fatal("different CLI names must not collide on the same consent file")
	}
}

func TestIsDoNotTrackRequested(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"1":     true,
		"true":  true,
		"yes":   true,
	}
	for val, want := range cases {
		t.Setenv("DO_NOT_TRACK", val)
		if got := IsDoNotTrackRequested(); got != want {
			t.Errorf("DO_NOT_TRACK=%q: got %v, want %v", val, got, want)
		}
	}
}

func TestIsCI(t *testing.T) {
	for _, name := range ciEnvVars {
		t.Setenv(name, "")
	}
	if IsCI() {
		t.Fatal("expected IsCI() to be false with no CI env vars set")
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	if !IsCI() {
		t.Fatal("expected IsCI() to be true with GITHUB_ACTIONS=true")
	}
}

func TestEnvConsentProviderCapsAtAnonymousInCI(t *testing.T) {
	for _, name := range ciEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("CI", "true")

	tier, err := (EnvConsentProvider{}).Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tier != TierAnonymous {
		t.Fatalf("got %v, want TierAnonymous", tier)
	}
}

func TestEnvConsentProviderDefersOutsideCI(t *testing.T) {
	for _, name := range ciEnvVars {
		t.Setenv(name, "")
	}
	if _, err := (EnvConsentProvider{}).Resolve(context.Background()); err == nil {
		t.Fatal("expected EnvConsentProvider to defer (error) outside CI")
	}
}
