package argvio

import "testing"

func TestTierOrdering(t *testing.T) {
	if !TierOptIn.Allows(TierFull) || !TierOptIn.Allows(TierBasic) || !TierOptIn.Allows(TierAnonymous) {
		t.Fatal("TierOptIn should allow all lower tiers")
	}
	if TierAnonymous.Allows(TierBasic) {
		t.Fatal("TierAnonymous must not allow TierBasic")
	}
	if TierBasic.Allows(TierFull) {
		t.Fatal("TierBasic must not allow TierFull")
	}
	if TierFull.Allows(TierOptIn) {
		t.Fatal("TierFull must not allow TierOptIn")
	}
}

func TestTierStringRoundTrip(t *testing.T) {
	for _, tier := range []Tier{TierAnonymous, TierBasic, TierFull, TierOptIn} {
		s := tier.String()
		got, ok := ParseTier(s)
		if !ok {
			t.Fatalf("ParseTier(%q) failed to parse round-tripped Tier.String()", s)
		}
		if got != tier {
			t.Fatalf("ParseTier(%q) = %v, want %v", s, got, tier)
		}
	}
}

func TestParseTierUnknownFailsClosed(t *testing.T) {
	got, ok := ParseTier("super-admin")
	if ok {
		t.Fatal("ParseTier should reject unknown tier strings")
	}
	if got != TierAnonymous {
		t.Fatalf("unknown tier should fail closed to TierAnonymous, got %v", got)
	}
}

func TestTierClamp(t *testing.T) {
	cases := []struct {
		t, ceiling, want Tier
	}{
		{TierOptIn, TierBasic, TierBasic},
		{TierAnonymous, TierOptIn, TierAnonymous},
		{TierFull, TierFull, TierFull},
	}
	for _, c := range cases {
		if got := c.t.clamp(c.ceiling); got != c.want {
			t.Errorf("Tier(%v).clamp(%v) = %v, want %v", c.t, c.ceiling, got, c.want)
		}
	}
}

func TestTierValid(t *testing.T) {
	if Tier(-1).valid() {
		t.Fatal("negative Tier must be invalid")
	}
	if Tier(99).valid() {
		t.Fatal("out-of-range Tier must be invalid")
	}
	if !TierAnonymous.valid() || !TierOptIn.valid() {
		t.Fatal("boundary Tier values must be valid")
	}
}
