package argvio

import (
	"context"
	"testing"
	"time"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

// installIDs maps each exported vendor metric name to the install ID its data
// points carried ("" for none).
func vendorInstallIDs(srv *fakeotlp.Server) map[string]string {
	out := map[string]string{}
	for _, rm := range srv.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				switch m.GetName() {
				case MetricCounter:
					for _, dp := range m.GetSum().GetDataPoints() {
						out[fakeotlp.Attr(dp.GetAttributes(), AttrCounterName).GetStringValue()] = fakeotlp.Attr(dp.GetAttributes(), AttrInstallID).GetStringValue()
					}
				case MetricMeasure:
					for _, dp := range m.GetHistogram().GetDataPoints() {
						out[fakeotlp.Attr(dp.GetAttributes(), AttrMeasureName).GetStringValue()] = fakeotlp.Attr(dp.GetAttributes(), AttrInstallID).GetStringValue()
					}
				case MetricValue:
					for _, dp := range m.GetHistogram().GetDataPoints() {
						out[fakeotlp.Attr(dp.GetAttributes(), AttrValueName).GetStringValue()] = fakeotlp.Attr(dp.GetAttributes(), AttrInstallID).GetStringValue()
					}
				}
			}
		}
	}
	return out
}

func recordVendorMetrics(ctx context.Context, c *Client) {
	c.RecordCounter(ctx, "plain.counter", 1)
	c.RecordDuration(ctx, "plain.duration", time.Second)
	c.Counter("dim.counter", Dims("a")).Add(ctx, 1, "x")
	c.Duration("dim.duration").Record(ctx, time.Second)
	c.Value("dim.value").Record(ctx, 0)
}

func TestVendorMetricsCarryInstallIDAtBasic(t *testing.T) {
	ctx := context.Background()
	for _, tier := range []Tier{TierBasic, TierFull, TierOptIn} {
		t.Run(tier.String(), func(t *testing.T) {
			c, srv := newTestClient(t, tier)
			c.RecordSessionStart(ctx) // loads the install ID in the background
			flush(t, c)
			recordVendorMetrics(ctx, c)
			flush(t, c)
			got := vendorInstallIDs(srv)
			if len(got) != 5 {
				t.Fatalf("metrics = %v, want 5", got)
			}
			for name, id := range got {
				if len(id) != 36 {
					t.Errorf("%s: install id = %q, want the install's UUID", name, id)
				}
			}
		})
	}
}

func TestVendorMetricsCarryNoInstallIDBelowBasic(t *testing.T) {
	ctx := context.Background()
	c, srv := newTestClient(t, TierAnonymous)
	c.RecordSessionStart(ctx)
	flush(t, c)
	recordVendorMetrics(ctx, c)
	flush(t, c)
	for name, id := range vendorInstallIDs(srv) {
		if id != "" {
			t.Errorf("%s carries install id %q at anonymous", name, id)
		}
	}
}

func TestFirstVendorMetricLoadsTheInstallIDInTheBackground(t *testing.T) {
	ctx := context.Background()
	c, srv := newTestClient(t, TierBasic)
	c.RecordCounter(ctx, "before", 1) // no ID yet: it is loaded off this goroutine
	flush(t, c)
	c.RecordCounter(ctx, "after", 1)
	flush(t, c)
	got := vendorInstallIDs(srv)
	if got["before"] != "" || len(got["after"]) != 36 {
		t.Errorf("install ids = %v, want none before the load and the UUID after", got)
	}
}
