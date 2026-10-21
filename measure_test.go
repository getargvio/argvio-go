package argvio

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

// measurePoint is one exported cli.measure data point.
type measurePoint struct {
	name, label, command string
	hasLabel             bool
	count                uint64
	sumMs                float64
}

func measurePoints(srv *fakeotlp.Server) []measurePoint {
	// The reader may export more than once; the last export holds the
	// final value of each (cumulative) data point.
	latest := map[string]measurePoint{}
	for _, rm := range srv.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() != MetricMeasure {
					continue
				}
				for _, dp := range m.GetHistogram().GetDataPoints() {
					p := measurePoint{count: dp.GetCount(), sumMs: dp.GetSum()}
					p.name = fakeotlp.Attr(dp.GetAttributes(), AttrMeasureName).GetStringValue()
					p.command = fakeotlp.Attr(dp.GetAttributes(), AttrCommandName).GetStringValue()
					if v := fakeotlp.Attr(dp.GetAttributes(), AttrMeasureLabel); v != nil {
						p.label, p.hasLabel = v.GetStringValue(), true
					}
					latest[p.name+"|"+p.label+"|"+p.command] = p
				}
			}
		}
	}
	out := make([]measurePoint, 0, len(latest))
	for _, p := range latest {
		out = append(out, p)
	}
	return out
}

func TestRecordDurationAggregatesPerNameAndLabel(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		c.RecordDuration(ctx, "john", time.Millisecond, WithMeasureLabel("hmac"))
	}
	c.RecordDuration(ctx, "john", 500*time.Millisecond, WithMeasureLabel("hmac"))
	c.RecordDuration(ctx, "john", 2*time.Second, WithMeasureLabel("rsa"))
	c.RecordDuration(ctx, "john", 3*time.Second)
	c.RecordDuration(ctx, "scan", 4*time.Second, WithMeasureLabel(""))
	flush(t, c)

	got := map[string]measurePoint{}
	for _, p := range measurePoints(srv) {
		got[p.name+"|"+p.label] = p
	}
	if len(got) != 4 {
		t.Fatalf("want one data point per name and label (4), got %d: %+v", len(got), got)
	}
	for key, want := range map[string]struct {
		count uint64
		sumMs float64
	}{
		"john|hmac": {1001, 1500},
		"john|rsa":  {1, 2000},
		"john|":     {1, 3000},
		"scan|":     {1, 4000},
	} {
		if got[key].count != want.count || math.Abs(got[key].sumMs-want.sumMs) > 1e-6 {
			t.Errorf("%s = {count %d, sum %v}, want {count %d, sum %v}", key, got[key].count, got[key].sumMs, want.count, want.sumMs)
		}
	}
	if got["john|"].hasLabel || got["scan|"].hasLabel {
		t.Error("an unlabelled measure must not carry the label attribute")
	}
	if len(srv.Logs()) != 0 {
		t.Errorf("measures must not emit log events, got %v", allLogEventNames(srv))
	}
}

func TestRecordDurationAttachesCurrentCommand(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	c.RecordDuration(ctx, "before", time.Second)
	c.RecordCommandInvocation(ctx, "testcli scan run", nil)
	c.RecordDuration(ctx, "after", time.Second)
	flush(t, c)

	for _, p := range measurePoints(srv) {
		want := ""
		if p.name == "after" {
			want = "scan.run"
		}
		if p.command != want {
			t.Errorf("%s: command = %q, want %q", p.name, p.command, want)
		}
	}
}

func TestRecordDurationDropsInvalidNamesAndLabels(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	long := strings.Repeat("a", maxIdentifierLength+1)

	for _, name := range []string{"", "Upper", "has space", "/etc/passwd", "https://example.com", "-lead", ".lead", long, "tok\x00en"} {
		c.RecordDuration(ctx, name, time.Second)
	}
	for _, label := range []string{"Upper", "has space", "user@example.com", "-lead", long} {
		c.RecordDuration(ctx, "ok", time.Second, WithMeasureLabel(label))
	}
	flush(t, c)

	if pts := measurePoints(srv); len(pts) != 0 {
		t.Fatalf("invalid names and labels must be dropped, not scrubbed or truncated; got %+v", pts)
	}

	// The limit itself is accepted.
	c.RecordDuration(ctx, strings.Repeat("a", maxIdentifierLength), time.Second, WithMeasureLabel(strings.Repeat("b", maxIdentifierLength)))
	flush(t, c)
	if pts := measurePoints(srv); len(pts) != 1 {
		t.Fatalf("a 64-byte name and label must be sent, got %+v", pts)
	}
}

func TestRecordDurationIgnoresNonPositiveDurations(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	c.RecordDuration(ctx, "d", 0)
	c.RecordDuration(ctx, "d", -time.Second)
	c.RecordDuration(ctx, "d", time.Duration(math.MinInt64))
	flush(t, c)
	if pts := measurePoints(srv); len(pts) != 0 {
		t.Fatalf("non-positive durations must be ignored, got %+v", pts)
	}

	c.RecordDuration(ctx, "d", time.Nanosecond)
	c.RecordDuration(ctx, "big", time.Duration(math.MaxInt64))
	flush(t, c)
	got := map[string]uint64{}
	for _, p := range measurePoints(srv) {
		got[p.name] = p.count
	}
	if got["d"] != 1 || got["big"] != 1 {
		t.Fatalf("positive durations must be recorded, got %+v", got)
	}
}

func TestRecordDurationIsSentAtEveryTier(t *testing.T) {
	for _, tier := range []Tier{TierAnonymous, TierBasic, TierFull, TierOptIn} {
		t.Run(tier.String(), func(t *testing.T) {
			c, srv := newTestClient(t, tier)
			c.RecordDuration(context.Background(), "ok", 2*time.Second, WithMeasureLabel("x"))
			flush(t, c)

			pts := measurePoints(srv)
			if len(pts) != 1 || pts[0].count != 1 {
				t.Fatalf("measure must be sent at %s, got %+v", tier, pts)
			}
			if got := c.Stats().BelowTierDropped; got != 0 {
				t.Errorf("BelowTierDropped = %d, want 0", got)
			}
		})
	}
}

func TestRecordDurationCarriesNoIdentifiers(t *testing.T) {
	c, srv := newTestClient(t, TierOptIn)
	c.RecordCommandInvocation(context.Background(), "testcli sub", nil)
	c.RecordDuration(context.Background(), "ok", time.Second)
	flush(t, c)

	found := false
	for _, rm := range srv.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() != MetricMeasure {
					continue
				}
				found = true
				for _, dp := range m.GetHistogram().GetDataPoints() {
					for _, key := range []string{AttrSessionID, AttrInstallID} {
						if fakeotlp.Attr(dp.GetAttributes(), key) != nil {
							t.Errorf("measure must not carry %s", key)
						}
					}
					want := measureBucketsMs
					got := dp.GetExplicitBounds()
					if len(got) != len(want) {
						t.Fatalf("bucket bounds = %v, want %v", got, want)
					}
					for i := range want {
						if got[i] != want[i] {
							t.Fatalf("bucket bounds = %v, want %v", got, want)
						}
					}
				}
				if m.GetUnit() != "ms" {
					t.Errorf("unit = %q, want ms", m.GetUnit())
				}
			}
		}
	}
	if !found {
		t.Fatal("no cli.measure metric exported")
	}
}

func TestRecordDurationOnDisabledAndNilClients(t *testing.T) {
	ctx := context.Background()
	var nilClient *Client
	for name, c := range map[string]*Client{
		"nil":      nilClient,
		"zero":     {},
		"disabled": disabledClient(),
	} {
		t.Run(name, func(t *testing.T) {
			c.RecordDuration(ctx, "ok", time.Second, WithMeasureLabel("x"), nil)
			c.RecordDuration(nil, "ok", time.Second) //nolint:staticcheck // nil ctx must not panic
		})
	}

	c, srv := newTestClient(t, TierAnonymous, WithDisabled())
	c.RecordDuration(ctx, "ok", time.Second)
	_ = c.Flush(ctx)
	if len(srv.Metrics()) != 0 {
		t.Fatal("a disabled client must send nothing")
	}
}

func TestRecordDurationAfterShutdownIsNoOp(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	c.RecordDuration(context.Background(), "late", time.Second)
	for _, p := range measurePoints(srv) {
		t.Fatalf("nothing may be recorded after Shutdown, got %+v", p)
	}
}

func TestRecordDurationIsSafeConcurrently(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			for i := 0; i < 500; i++ {
				c.RecordDuration(ctx, "hot", time.Millisecond, WithMeasureLabel("x"))
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	flush(t, c)
	pts := measurePoints(srv)
	if len(pts) != 1 || pts[0].count != 4000 {
		t.Fatalf("want one point of 4000, got %+v", pts)
	}
	if got := c.Stats().QueueOverflowDropped; got != 0 {
		t.Errorf("measures must not use the job queue, overflow = %d", got)
	}
}
