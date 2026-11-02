package argvio

import (
	"context"
	"math"
	"slices"
	"testing"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

type valuePoint struct {
	name, unit, severity string
	count                uint64
	bounds               []float64
	buckets              []uint64
	metricUnit           string
}

func valuePoints(srv *fakeotlp.Server) map[string]valuePoint {
	out := map[string]valuePoint{}
	for _, rm := range srv.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() != MetricValue {
					continue
				}
				for _, dp := range m.GetHistogram().GetDataPoints() {
					p := valuePoint{
						name:       fakeotlp.Attr(dp.GetAttributes(), AttrValueName).GetStringValue(),
						unit:       fakeotlp.Attr(dp.GetAttributes(), AttrValueUnit).GetStringValue(),
						severity:   fakeotlp.Attr(dp.GetAttributes(), AttrDimPrefix+"severity").GetStringValue(),
						count:      dp.GetCount(),
						bounds:     dp.GetExplicitBounds(),
						buckets:    dp.GetBucketCounts(),
						metricUnit: m.GetUnit(),
					}
					out[p.name+"|"+p.severity] = p
				}
			}
		}
	}
	return out
}

func TestValueBucketsMatchServer(t *testing.T) {
	// schema/allowlist/v1.yaml explicit_bounds of cli.value in getargvio/argvio.
	want := []float64{0, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 5000, 10000, 100000}
	if !slices.Equal(valueBuckets, want) {
		t.Errorf("valueBuckets = %v, want the server's %v", valueBuckets, want)
	}
}

func TestValueHandleRecordsADistribution(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	findings := c.Value("scan.findings", Dims("severity"))
	for _, n := range []float64{0, 0, 3, 40} {
		findings.Record(ctx, n, "high")
	}
	findings.Record(ctx, 1, "low")
	c.Value("spec.size", WithUnit(UnitBytes)).Record(ctx, 2048)
	flush(t, c)

	got := valuePoints(srv)
	high := got["scan.findings|high"]
	if high.count != 4 || high.unit != "1" || high.metricUnit != "1" || !slices.Equal(high.bounds, valueBuckets) {
		t.Fatalf("high = %+v, want 4 values in unit 1 with the server's bounds", high)
	}
	if high.buckets[0] != 2 {
		t.Errorf("zeros land in the first bucket: buckets = %v", high.buckets)
	}
	if got["scan.findings|low"].count != 1 {
		t.Errorf("low = %+v", got["scan.findings|low"])
	}
	if size := got["spec.size|"]; size.unit != "By" || size.count != 1 {
		t.Errorf("spec.size = %+v, want one value in By", size)
	}
}

func TestValueHandleDropsInvalidCalls(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	v := c.Value("ok", Dims("severity"))
	for _, x := range []float64{-1, math.NaN(), math.Inf(1)} {
		v.Record(ctx, x, "high")
	}
	v.Record(ctx, 1)
	v.Record(ctx, 1, "High")
	c.Value("ok", WithUnit("ms")).Record(ctx, 1)
	c.Value("Bad").Record(ctx, 1)
	v.Record(ctx, 1, "high")
	flush(t, c)

	got := valuePoints(srv)
	if len(got) != 1 || got["ok|high"].count != 1 {
		t.Errorf("points = %+v, want only the valid call", got)
	}
}

func TestValueOnDisabledAndNilClients(t *testing.T) {
	ctx := context.Background()
	var nilValue *Value
	nilValue.Record(ctx, 1)
	var nilClient *Client
	for _, c := range []*Client{nilClient, {}, disabledClient()} {
		c.Value("ok", Dims("a")).Record(ctx, 1, "x")
	}
}
