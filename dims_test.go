package argvio

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// dimPoint is one exported cli.counter or cli.measure data point with its
// dimensions.
type dimPoint struct {
	metric, name, command string
	dims                  map[string]string
	hasLabel              bool
	value                 int64  // counters
	count                 uint64 // measures
}

func dimPoints(srv *fakeotlp.Server) []dimPoint {
	latest := map[string]dimPoint{}
	read := func(metric string, attrs []*commonpb.KeyValue) dimPoint {
		p := dimPoint{metric: metric, dims: map[string]string{}}
		for _, kv := range attrs {
			if k, ok := strings.CutPrefix(kv.GetKey(), AttrDimPrefix); ok {
				p.dims[k] = kv.GetValue().GetStringValue()
			}
		}
		nameAttr, labelAttr := AttrCounterName, AttrCounterLabel
		if metric == MetricMeasure {
			nameAttr, labelAttr = AttrMeasureName, AttrMeasureLabel
		}
		p.name = fakeotlp.Attr(attrs, nameAttr).GetStringValue()
		p.command = fakeotlp.Attr(attrs, AttrCommandName).GetStringValue()
		p.hasLabel = fakeotlp.Attr(attrs, labelAttr) != nil
		return p
	}
	key := func(p dimPoint) string {
		k := p.metric + "|" + p.name + "|" + p.command
		for _, d := range []string{"severity", "check", "target", "a", "b", "c"} {
			k += "|" + p.dims[d]
		}
		return k
	}
	for _, rm := range srv.Metrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				switch m.GetName() {
				case MetricCounter:
					for _, dp := range m.GetSum().GetDataPoints() {
						p := read(MetricCounter, dp.GetAttributes())
						p.value = dp.GetAsInt()
						latest[key(p)] = p
					}
				case MetricMeasure:
					for _, dp := range m.GetHistogram().GetDataPoints() {
						p := read(MetricMeasure, dp.GetAttributes())
						p.count = dp.GetCount()
						latest[key(p)] = p
					}
				}
			}
		}
	}
	out := make([]dimPoint, 0, len(latest))
	for _, p := range latest {
		out = append(out, p)
	}
	return out
}

func TestCounterHandleSendsDimensions(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	vulns := c.Counter("vulns.found", Dims("severity", "check"))

	for i := 0; i < 500; i++ {
		vulns.Add(ctx, 1, "high", "bola")
	}
	vulns.Add(ctx, 3, "low", "bola")
	vulns.Add(ctx, 2, "high", "jwt.alg-none")
	flush(t, c)

	got := map[string]dimPoint{}
	for _, p := range dimPoints(srv) {
		if p.metric == MetricCounter {
			got[p.dims["severity"]+"/"+p.dims["check"]] = p
		}
	}
	if len(got) != 3 {
		t.Fatalf("want one data point per dimension values (3), got %+v", got)
	}
	for k, want := range map[string]int64{"high/bola": 500, "low/bola": 3, "high/jwt.alg-none": 2} {
		p := got[k]
		if p.value != want || p.name != "vulns.found" || len(p.dims) != 2 || p.hasLabel {
			t.Errorf("%s = %+v, want %d with exactly two dims and no label", k, p, want)
		}
	}
}

func TestDurationHandleSendsDimensions(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	scan := c.Duration("scan.run", Dims("target"))
	scan.Record(ctx, 20*time.Millisecond, "openapi")
	scan.Record(ctx, 40*time.Millisecond, "openapi")
	scan.Record(ctx, time.Second, "graphql")
	flush(t, c)

	got := map[string]dimPoint{}
	for _, p := range dimPoints(srv) {
		if p.metric == MetricMeasure {
			got[p.dims["target"]] = p
		}
	}
	if got["openapi"].count != 2 || got["graphql"].count != 1 || got["openapi"].name != "scan.run" {
		t.Errorf("measure points = %+v, want openapi 2 and graphql 1", got)
	}
}

func TestHandleWithoutDimsAndCurrentCommand(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()
	runs := c.Counter("runs")
	c.RecordCommandInvocation(ctx, "testcli scan", nil)
	runs.Add(ctx, 4)
	flush(t, c)

	ps := dimPoints(srv)
	if len(ps) != 1 || ps[0].value != 4 || len(ps[0].dims) != 0 || ps[0].command != "scan" {
		t.Errorf("points = %+v, want runs 4 under command scan, no dims", ps)
	}
}

func TestHandleDropsInvalidCalls(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	ctx := context.Background()

	ok := c.Counter("ok", Dims("severity"))
	ok.Add(ctx, 1)                     // too few values
	ok.Add(ctx, 1, "high", "extra")    // too many values
	ok.Add(ctx, 1, "High")             // invalid value
	ok.Add(ctx, 1, "/home/alice/spec") // invalid value
	ok.Add(ctx, 0, "high")             // non-positive
	ok.Add(ctx, -2, "high")
	c.Duration("ok.d", Dims("t")).Record(ctx, 0, "x")
	for _, h := range []*Counter{
		c.Counter("Bad"),
		c.Counter("bad", Dims("Sev")),
		c.Counter("bad", Dims("a", "a")),
		c.Counter("bad", Dims("a", "b", "c", "d")),
		c.Counter("bad", Dims("")),
	} {
		h.Add(ctx, 1, "x", "x", "x", "x")
		h.Add(ctx, 1, "x")
		h.Add(ctx, 1, "x", "x")
		h.Add(ctx, 1)
	}
	ok.Add(ctx, 1, "high")
	c.Counter("max", Dims("a", "b", "c")).Add(ctx, 1, "1", "2", "3")
	flush(t, c)

	got := map[string]int64{}
	for _, p := range dimPoints(srv) {
		got[p.name] = p.value
	}
	if !maps.Equal(got, map[string]int64{"ok": 1, "max": 1}) {
		t.Errorf("points = %v, want only the valid ok and max calls", got)
	}
}

func TestHandlesOnDisabledAndNilClients(t *testing.T) {
	ctx := context.Background()
	var nilClient *Client
	var nilCounter *Counter
	var nilDuration *Duration
	nilCounter.Add(ctx, 1)
	nilDuration.Record(ctx, time.Second)
	for name, c := range map[string]*Client{
		"nil":      nilClient,
		"zero":     {},
		"disabled": disabledClient(),
	} {
		t.Run(name, func(t *testing.T) {
			c.Counter("ok", Dims("a"), nil).Add(ctx, 1, "x")
			c.Duration("ok", Dims("a")).Record(ctx, time.Second, "x")
			c.Counter("ok").Add(nil, 1) //nolint:staticcheck // nil ctx must not panic
		})
	}

	c, srv := newTestClient(t, TierAnonymous, WithDisabled())
	c.Counter("ok").Add(ctx, 1)
	_ = c.Flush(ctx)
	if len(srv.Metrics()) != 0 {
		t.Fatal("a disabled client must send nothing")
	}
}
