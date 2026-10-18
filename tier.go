package argvio

import "fmt"

// Tier is the consent tier a CLI user (or their organization) has
// granted for telemetry collection. It gates both which Record*
// functions are callable with which fields, and the value sent on the
// cli.analytics.tier resource attribute, which the public ingest server
// re-validates and enforces as a backstop.
//
// Tiers are strictly ordered: each tier is a superset of the fields
// allowed at the tier below it. Callers should treat the ordering as
// significant (Tier values compare with <, <=, etc.) rather than
// switching on the underlying int.
type Tier int

const (
	// TierAnonymous permits no persistent or cross-invocation
	// identifiers: command name, exit code, duration, CLI version,
	// OS/arch and CI environment only.
	TierAnonymous Tier = iota

	// TierBasic adds an opaque per-process session ID, an opaque
	// per-install ID, flag names (never values), and classified error
	// types.
	TierBasic

	// TierFull is reserved for fields the server allowlist permits at
	// this level; this SDK sends nothing extra here beyond TierBasic.
	TierFull

	// TierOptIn is the explicit opt-in tier: raw error detail (stack
	// traces, messages) and any other field the allowlist marks
	// opt-in-only. Never the default; a vendor or user must positively
	// select it.
	TierOptIn
)

// String returns the wire value sent on the cli.analytics.tier resource
// attribute. These MUST match the consent_tiers names in the `public`
// server's allowlist (schema/allowlist/v1.yaml in getargvio/argvio)
// exactly, or the server rejects the record — see CONTRIBUTING.md.
func (t Tier) String() string {
	switch t {
	case TierAnonymous:
		return "anonymous"
	case TierBasic:
		return "basic"
	case TierFull:
		return "full"
	case TierOptIn:
		return "optin_plus"
	default:
		return "unknown"
	}
}

// ResourceAttrTier is the OTel resource attribute key the tier is
// reported under. Confirmed by task spec; server treats this as the
// declared tier for allowlist enforcement.
const ResourceAttrTier = "cli.analytics.tier"

// ParseTier parses a wire tier string (as produced by Tier.String) back
// into a Tier. "opt-in", the wire value before v0.2.0, is still accepted
// so consent files written by older versions keep resolving. It returns
// false if s does not match a known tier; the
// zero value (TierAnonymous) is returned in that case so callers that
// ignore the ok result fail closed to the least-privileged tier.
func ParseTier(s string) (Tier, bool) {
	switch s {
	case "anonymous":
		return TierAnonymous, true
	case "basic":
		return TierBasic, true
	case "full":
		return TierFull, true
	case "optin_plus", "opt-in":
		return TierOptIn, true
	default:
		return TierAnonymous, false
	}
}

// Allows reports whether this tier permits emitting a field scoped to
// required. A session configured at TierFull, for example, Allows(TierBasic)
// and Allows(TierFull) but not Allows(TierOptIn).
func (t Tier) Allows(required Tier) bool {
	return t >= required
}

// clamp returns the lower of t and ceiling. It is used to enforce that
// no emitted signal ever carries a tier above the locally configured
// consent ceiling, independent of what an individual Record* call site
// requests.
func (t Tier) clamp(ceiling Tier) Tier {
	if t > ceiling {
		return ceiling
	}
	return t
}

func (t Tier) valid() bool {
	return t >= TierAnonymous && t <= TierOptIn
}

var _ fmt.Stringer = TierAnonymous
