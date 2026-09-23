package argvio

import "testing"

func TestWithCommit(t *testing.T) {
	cfg := newConfig("key", "cli", "1.0.0")
	WithCommit("abc123")(cfg)

	if len(cfg.extraResourceAttrs) != 1 {
		t.Fatalf("expected 1 resource attribute, got %d", len(cfg.extraResourceAttrs))
	}
	got := cfg.extraResourceAttrs[0]
	if got.key != ResourceAttrVCSRevision || got.value != "abc123" {
		t.Errorf("unexpected attribute: %v", got)
	}
}

func TestWithCommit_BlankIsNoop(t *testing.T) {
	cfg := newConfig("key", "cli", "1.0.0")
	WithCommit("")(cfg)

	if len(cfg.extraResourceAttrs) != 0 {
		t.Errorf("expected no resource attributes for a blank commit, got %v", cfg.extraResourceAttrs)
	}
}

func TestWithBuildDate(t *testing.T) {
	cfg := newConfig("key", "cli", "1.0.0")
	WithBuildDate("2026-01-01")(cfg)

	if len(cfg.extraResourceAttrs) != 1 {
		t.Fatalf("expected 1 resource attribute, got %d", len(cfg.extraResourceAttrs))
	}
	got := cfg.extraResourceAttrs[0]
	if got.key != ResourceAttrBuildDate || got.value != "2026-01-01" {
		t.Errorf("unexpected attribute: %v", got)
	}
}

func TestWithBuildDate_BlankIsNoop(t *testing.T) {
	cfg := newConfig("key", "cli", "1.0.0")
	WithBuildDate("")(cfg)

	if len(cfg.extraResourceAttrs) != 0 {
		t.Errorf("expected no resource attributes for a blank date, got %v", cfg.extraResourceAttrs)
	}
}
