package argvio

import (
	"context"
	"strings"
	"testing"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

// counterPoint is one exported cli.counter data point.
type counterPoint struct {
	name, label, command string
	hasLabel             bool
	value                int64
}

func counterPoints(srv *fakeotlp.Server) []counterPoint {
	// The reader may export more than once; the last export holds the
	// final value of each (cumulative) data point.
	latest := map[string]counterPoint{}
	for _, rm := range srv.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() != MetricCounter {
					continue
				}
				for _, dp := range m.GetSum().GetDataPoints() {
					p := counterPoint{value: dp.GetAsInt()}
					p.name = fakeotlp.Attr(dp.GetAttributes(), AttrCounterName).GetStringValue()
					p.command = fakeotlp.Attr(dp.GetAttributes(), AttrCommandName).GetStringValue()
					if v := fakeotlp.Attr(dp.GetAttributes(), AttrCounterLabel); v != nil {
						p.label, p.hasLabel = v.GetStringValue(), true
					}
					latest[p.name+"|"+p.label+"|"+p.command] = p
				}
			}
		}
	}
	out := make([]counterPoint, 0, len(latest))
	for _, p := range latest {
		out = append(out, p)
	}
	return out
}

func TestValidIdentifier(t *testing.T) {
	long := strings.Repeat("a", maxIdentifierLength)
	for s, want := range map[string]bool{
		"a":                       true,
		"0":                       true,
		"openapi.scheme":          true,
		"rules_generated-by.verb": true,
		long:                      true,
		long + "a":                false,
		"":                        false,
		".a":                      false,
		"_a":                      false,
		"-a":                      false,
		"A":                       false,
		"aB":                      false,
		"a b":                     false,
		"a/b":                     false,
		"a:b":                     false,
		"https://x.test":          false,
		"é":                       false,
		"a\n":                     false,
	} {
		if got := validIdentifier(s); got != want {
			t.Errorf("validIdentifier(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestRecordCounterAggregatesPerNameAndLabel(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		c.RecordCounter(ctx, "schemes", 1, WithLabel("bearer"))
	}
	c.RecordCounter(ctx, "schemes", 5, WithLabel("bearer"))
	c.RecordCounter(ctx, "schemes", 2, WithLabel("basic"))
	c.RecordCounter(ctx, "schemes", 3)
	c.RecordCounter(ctx, "rules", 4, WithLabel(""))
	flush(t, c)

	got := map[string]counterPoint{}
	for _, p := range counterPoints(srv) {
		got[p.name+"|"+p.label] = p
	}
	if len(got) != 4 {
		t.Fatalf("want one data point per name and label (4), got %d: %+v", len(got), got)
	}
	for key, want := range map[string]int64{
		"schemes|bearer": 1005,
		"schemes|basic":  2,
		"schemes|":       3,
		"rules|":         4,
	} {
		if got[key].value != want {
			t.Errorf("%s = %d, want %d", key, got[key].value, want)
		}
	}
	if got["schemes|"].hasLabel || got["rules|"].hasLabel {
		t.Error("an unlabelled counter must not carry the label attribute")
	}
	if len(srv.Logs()) != 0 {
		t.Errorf("counters must not emit log events, got %v", allLogEventNames(srv))
	}
}

func TestRecordCounterAttachesCurrentCommand(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	c.RecordCounter(ctx, "before", 1)
	c.RecordCommandInvocation(ctx, "testcli scan run", nil)
	c.RecordCounter(ctx, "after", 1)
	flush(t, c)

	for _, p := range counterPoints(srv) {
		want := ""
		if p.name == "after" {
			want = "scan.run"
		}
		if p.command != want {
			t.Errorf("%s: command = %q, want %q", p.name, p.command, want)
		}
	}
}

func TestRecordCounterDropsInvalidNamesAndLabels(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	long := strings.Repeat("a", maxIdentifierLength+1)

	for _, name := range []string{"", "Upper", "has space", "/etc/passwd", "https://example.com", "-lead", ".lead", long, "tok\x00en"} {
		c.RecordCounter(ctx, name, 1)
	}
	for _, label := range []string{"Upper", "has space", "user@example.com", "-lead", long} {
		c.RecordCounter(ctx, "ok", 1, WithLabel(label))
	}
	flush(t, c)

	if pts := counterPoints(srv); len(pts) != 0 {
		t.Fatalf("invalid names and labels must be dropped, not scrubbed or truncated; got %+v", pts)
	}

	// The limit itself is accepted.
	c.RecordCounter(ctx, strings.Repeat("a", maxIdentifierLength), 1, WithLabel(strings.Repeat("b", maxIdentifierLength)))
	flush(t, c)
	if pts := counterPoints(srv); len(pts) != 1 {
		t.Fatalf("a 64-byte name and label must be sent, got %+v", pts)
	}
}

func TestRecordCounterIgnoresNonPositiveN(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	c.RecordCounter(ctx, "n", 0)
	c.RecordCounter(ctx, "n", -1)
	c.RecordCounter(ctx, "n", -1<<63)
	flush(t, c)
	if pts := counterPoints(srv); len(pts) != 0 {
		t.Fatalf("non-positive n must be ignored, got %+v", pts)
	}

	c.RecordCounter(ctx, "n", 1)
	c.RecordCounter(ctx, "big", 1<<62)
	flush(t, c)
	got := map[string]int64{}
	for _, p := range counterPoints(srv) {
		got[p.name] = p.value
	}
	if got["n"] != 1 || got["big"] != 1<<62 {
		t.Fatalf("positive n must be recorded, got %+v", got)
	}
}

func TestRecordCounterIsSentAtEveryTier(t *testing.T) {
	for _, tier := range []Tier{TierAnonymous, TierBasic, TierFull, TierOptIn} {
		t.Run(tier.String(), func(t *testing.T) {
			c, srv := newTestClient(t, tier)
			c.RecordCounter(context.Background(), "ok", 2, WithLabel("x"))
			flush(t, c)

			pts := counterPoints(srv)
			if len(pts) != 1 || pts[0].value != 2 {
				t.Fatalf("counter must be sent at %s, got %+v", tier, pts)
			}
			if got := c.Stats().BelowTierDropped; got != 0 {
				t.Errorf("BelowTierDropped = %d, want 0", got)
			}
		})
	}
}

func TestRecordCounterCarriesNoIdentifiers(t *testing.T) {
	c, srv := newTestClient(t, TierOptIn)
	c.RecordCommandInvocation(context.Background(), "testcli sub", nil)
	c.RecordCounter(context.Background(), "ok", 1)
	flush(t, c)

	for _, rm := range srv.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() != MetricCounter {
					continue
				}
				for _, dp := range m.GetSum().GetDataPoints() {
					for _, key := range []string{AttrSessionID, AttrInstallID} {
						if fakeotlp.Attr(dp.GetAttributes(), key) != nil {
							t.Errorf("counter must not carry %s", key)
						}
					}
				}
				if !m.GetSum().GetIsMonotonic() {
					t.Error("cli.counter must be monotonic")
				}
				if m.GetUnit() != "1" {
					t.Errorf("unit = %q, want 1", m.GetUnit())
				}
			}
		}
	}
}

func TestRecordCounterOnDisabledAndNilClients(t *testing.T) {
	ctx := context.Background()
	var nilClient *Client
	for name, c := range map[string]*Client{
		"nil":      nilClient,
		"zero":     {},
		"disabled": disabledClient(),
	} {
		t.Run(name, func(t *testing.T) {
			c.RecordCounter(ctx, "ok", 1, WithLabel("x"), nil)
			c.RecordCounter(nil, "ok", 1) //nolint:staticcheck // nil ctx must not panic
		})
	}

	c, srv := newTestClient(t, TierAnonymous, WithDisabled())
	c.RecordCounter(ctx, "ok", 1)
	_ = c.Flush(ctx)
	if len(srv.Metrics()) != 0 {
		t.Fatal("a disabled client must send nothing")
	}
}

func TestRecordCounterAfterShutdownIsNoOp(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	c.RecordCounter(context.Background(), "late", 1)
	for _, p := range counterPoints(srv) {
		t.Fatalf("nothing may be recorded after Shutdown, got %+v", p)
	}
}

func TestRecordCounterIsSafeConcurrently(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			for i := 0; i < 500; i++ {
				c.RecordCounter(ctx, "hot", 1, WithLabel("x"))
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	flush(t, c)
	pts := counterPoints(srv)
	if len(pts) != 1 || pts[0].value != 4000 {
		t.Fatalf("want one point of 4000, got %+v", pts)
	}
	if got := c.Stats().QueueOverflowDropped; got != 0 {
		t.Errorf("counters must not use the job queue, overflow = %d", got)
	}
}
