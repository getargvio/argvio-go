package argvio

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// FileConsentProvider is the reference ConsentProvider implementation:
// it reads a small JSON file at a conventional per-CLI config location.
//
// The path defaults to filepath.Join(os.UserConfigDir(), cliName,
// "argvio-consent.json"), which respects XDG_CONFIG_HOME on Linux,
// ~/Library/Application Support on macOS, and %AppData% on Windows (via
// the standard library's os.UserConfigDir — see its docs for exact
// per-OS behavior). Namespacing under the vendor's own CLI name avoids
// collisions between unrelated CLIs that both embed this SDK, and puts
// the file next to config a user already expects that CLI to own.
//
// Use WithConfigPath to override the location entirely (e.g. a vendor
// that already has its own config file and wants to store the tier
// alongside it under a different mechanism should implement their own
// ConsentProvider instead).
type FileConsentProvider struct {
	path string
}

// FileConsentOption configures a FileConsentProvider.
type FileConsentOption func(*FileConsentProvider)

// WithConfigPath overrides the default config file location.
func WithConfigPath(path string) FileConsentOption {
	return func(p *FileConsentProvider) { p.path = path }
}

// NewFileConsentProvider builds a FileConsentProvider namespaced under
// cliName. cliName should match the CLI's own config directory name
// (typically the binary name).
func NewFileConsentProvider(cliName string, opts ...FileConsentOption) *FileConsentProvider {
	p := &FileConsentProvider{}
	for _, opt := range opts {
		opt(p)
	}
	if p.path == "" {
		p.path = defaultConsentPath(cliName)
	}
	return p
}

func defaultConsentPath(cliName string) string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	if cliName == "" {
		cliName = "argvio"
	}
	return filepath.Join(dir, cliName, "argvio-consent.json")
}

type consentFile struct {
	// Tier is the wire tier string, as produced by Tier.String.
	Tier string `json:"tier"`
}

// Resolve implements ConsentProvider. A missing file, unreadable file,
// or malformed contents all resolve as an error (never a panic), so
// callers should place FileConsentProvider inside ChainProviders with a
// sane fallback (e.g. StaticProvider{Tier: TierAnonymous}).
func (p *FileConsentProvider) Resolve(ctx context.Context) (tier Tier, err error) {
	defer func() {
		if r := recover(); r != nil {
			tier, err = TierAnonymous, errRecoveredPanic
		}
	}()

	if p == nil || p.path == "" {
		return TierAnonymous, errNoConsentFile
	}

	// #nosec G304 -- path is either an explicit vendor override or
	// derived from os.UserConfigDir(), not attacker-controlled input.
	data, readErr := os.ReadFile(p.path)
	if readErr != nil {
		return TierAnonymous, readErr
	}

	var cf consentFile
	if jsonErr := json.Unmarshal(data, &cf); jsonErr != nil {
		return TierAnonymous, jsonErr
	}

	t, ok := ParseTier(cf.Tier)
	if !ok {
		return TierAnonymous, errUnknownTierValue
	}
	return t, nil
}

// Store persists tier as the chosen consent tier, creating the parent
// directory if needed. Intended for use by a vendor's first-run consent
// prompt. Store does not itself enforce any tier ceiling — it is the
// vendor's own UX writing the user's explicit choice.
func (p *FileConsentProvider) Store(tier Tier) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errRecoveredPanic
		}
	}()

	if p == nil || p.path == "" {
		return errNoConsentFile
	}
	if !tier.valid() {
		return errUnknownTierValue
	}

	if mkErr := os.MkdirAll(filepath.Dir(p.path), 0o700); mkErr != nil {
		return mkErr
	}
	data, jsonErr := json.Marshal(consentFile{Tier: tier.String()})
	if jsonErr != nil {
		return jsonErr
	}
	return os.WriteFile(p.path, data, 0o600)
}

// Path returns the resolved config file path (after defaults/overrides
// have been applied), primarily for diagnostics and tests.
func (p *FileConsentProvider) Path() string {
	if p == nil {
		return ""
	}
	return p.path
}

var (
	errNoConsentFile    = errors.New("argvio: no consent config file path resolved")
	errUnknownTierValue = errors.New("argvio: unknown tier value in consent config")
)
