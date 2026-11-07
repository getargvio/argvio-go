package argvio

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Declared at package level, before any New: nothing is resolved until use.
var (
	pkgCounter  = NewCounter("pkg.counter", Dims("severity"))
	pkgDuration = NewDuration("pkg.duration", Dims("target"))
	pkgValue    = NewValue("pkg.value", Dims("severity"), WithUnit(UnitBytes))
)

func TestPackageHandlesBeforeNewAndAfter(t *testing.T) {
	SetDefault(nil)
	ctx := context.Background()
	// No default: nothing happens, nothing panics.
	pkgCounter.Add(ctx, 1, "high")
	pkgDuration.Record(ctx, time.Second, "openapi")
	pkgValue.Record(ctx, 1, "high")
	pkgDuration.Start(ctx, "openapi")()

	c, srv := newTestClient(t, TierAnonymous)
	if Default() != c {
		t.Fatal("New must register its client as the default")
	}
	pkgCounter.Add(ctx, 2, "high")
	pkgCounter.Add(ctx, 3, "high")
	pkgDuration.Record(ctx, 20*time.Millisecond, "openapi")
	pkgValue.Record(ctx, 0, "high")
	flush(t, c)

	counters := 0
	for _, p := range dimPoints(srv) {
		switch p.metric {
		case MetricCounter:
			counters++
			if p.name != "pkg.counter" || p.dims["severity"] != "high" || p.value != 5 {
				t.Errorf("counter = %+v, want pkg.counter high 5", p)
			}
		case MetricMeasure:
			if p.name != "pkg.duration" || p.dims["target"] != "openapi" || p.count != 1 {
				t.Errorf("duration = %+v", p)
			}
		}
	}
	if counters != 1 {
		t.Errorf("want one counter data point, got %d", counters)
	}
	if v := valuePoints(srv)["pkg.value|high"]; v.count != 1 || v.unit != string(UnitBytes) {
		t.Errorf("value = %+v, want one point in bytes", v)
	}
}

func TestPackageHandleRebindsToNewDefault(t *testing.T) {
	ctx := context.Background()
	first, srv1 := newTestClient(t, TierAnonymous)
	h := NewCounter("rebind.counter")
	h.Add(ctx, 1)

	second, srv2 := newTestClient(t, TierAnonymous)
	if Default() != second {
		t.Fatal("a second New must replace the default")
	}
	h.Add(ctx, 7)
	flush(t, first)
	flush(t, second)

	sum := func(srvPoints []dimPoint) (n int64) {
		for _, p := range srvPoints {
			if p.name == "rebind.counter" {
				n += p.value
			}
		}
		return n
	}
	if got := sum(dimPoints(srv1)); got != 1 {
		t.Errorf("first client got %d, want 1", got)
	}
	if got := sum(dimPoints(srv2)); got != 7 {
		t.Errorf("second client got %d, want 7", got)
	}

	SetDefault(first)
	h.Add(ctx, 2)
	flush(t, first)
	if got := sum(dimPoints(srv1)); got != 3 {
		t.Errorf("after SetDefault(first), first client got %d, want 3", got)
	}
}

func TestWithoutDefaultKeepsDefault(t *testing.T) {
	c, _ := newTestClient(t, TierAnonymous)
	other, _ := newTestClient(t, TierAnonymous, WithoutDefault())
	if Default() == other || Default() != c {
		t.Error("WithoutDefault must leave the default alone")
	}
}

func TestDisabledAndShutdownDefaultIsNoop(t *testing.T) {
	ctx := context.Background()
	c, srv := newTestClient(t, TierAnonymous)
	h := NewCounter("disabled.counter")

	d, err := New("k", "cli", "1", WithDisabled())
	if err != nil || Default() != d {
		t.Fatalf("a disabled client is still registered: err=%v", err)
	}
	h.Add(ctx, 1)
	flush(t, c)
	for _, p := range dimPoints(srv) {
		if p.name == "disabled.counter" {
			t.Errorf("a disabled default must not record, got %+v", p)
		}
	}

	SetDefault(c)
	if err := c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	h.Add(ctx, 1) // after Shutdown: must not panic
}

func TestFailedNewRegistersDisabledClient(t *testing.T) {
	t.Setenv(otelEndpointEnvVar, "")
	c, _ := newTestClient(t, TierAnonymous)
	d, err := New("k", "cli", "1", WithDisableOTELEnvFallback(), WithInstallIDPath(filepath.Join(t.TempDir(), "id")))
	if err == nil || !d.Disabled() {
		t.Fatalf("want a disabled client and an error, got %v %v", d, err)
	}
	if Default() == c {
		t.Error("New's returned client replaces the default even when degraded")
	}
}

func TestNilPackageHandlesAreNoops(t *testing.T) {
	ctx := context.Background()
	var (
		k *Counter
		d *Duration
		v *Value
	)
	k.Add(ctx, 1)
	d.Record(ctx, time.Second)
	v.Record(ctx, 1)
	stop := d.Start(ctx)
	late := d.StartLate(ctx)
	if stop == nil || late == nil {
		t.Fatal("Start and StartLate must never return nil")
	}
	stop()
	late("x")
}

func TestPackageHandleValidation(t *testing.T) {
	ctx := context.Background()
	c, srv := newTestClient(t, TierAnonymous)

	bad := []struct {
		name string
		call func()
	}{
		{"invalid name", func() { NewCounter("Bad Name").Add(ctx, 1) }},
		{"too many dims", func() { NewCounter("v.a", Dims("a", "b", "c", "d")).Add(ctx, 1, "x", "x", "x", "x") }},
		{"invalid dim key", func() { NewCounter("v.b", Dims("Bad")).Add(ctx, 1, "x") }},
		{"repeated dim key", func() { NewCounter("v.c", Dims("a", "a")).Add(ctx, 1, "x", "x") }},
		{"too few values", func() { NewCounter("v.d", Dims("a", "b")).Add(ctx, 1, "x") }},
		{"too many values", func() { NewCounter("v.e", Dims("a")).Add(ctx, 1, "x", "y") }},
		{"invalid value", func() { NewCounter("v.f", Dims("a")).Add(ctx, 1, "/etc/passwd") }},
		{"zero counter", func() { NewCounter("v.g").Add(ctx, 0) }},
		{"negative counter", func() { NewCounter("v.h").Add(ctx, -1) }},
		{"zero duration", func() { NewDuration("v.i").Record(ctx, 0) }},
		{"negative duration", func() { NewDuration("v.j").Record(ctx, -time.Second) }},
		{"negative value", func() { NewValue("v.k").Record(ctx, -1) }},
		{"NaN value", func() { var z float64; NewValue("v.l").Record(ctx, z/z) }},
		{"bad unit", func() { NewValue("v.m", WithUnit("ms")).Record(ctx, 1) }},
		{"wrong values on Start", func() { NewDuration("v.n", Dims("a")).Start(ctx)() }},
		{"wrong values on StartLate", func() { NewDuration("v.o", Dims("a")).StartLate(ctx)() }},
	}
	for _, tc := range bad {
		tc.call() // must neither panic nor record
	}
	flush(t, c)
	if pts := dimPoints(srv); len(pts) != 0 {
		t.Errorf("invalid calls must be dropped, got %+v", pts)
	}
	if pts := valuePoints(srv); len(pts) != 0 {
		t.Errorf("invalid value calls must be dropped, got %+v", pts)
	}
}

func TestZeroValueIsAllowedForValues(t *testing.T) {
	c, srv := newTestClient(t, TierAnonymous)
	NewValue("zero.value").Record(context.Background(), 0)
	flush(t, c)
	if v := valuePoints(srv)["zero.value|"]; v.count != 1 {
		t.Errorf("a zero must be recorded, got %+v", v)
	}
}

func TestDurationStartAndStartLate(t *testing.T) {
	ctx := context.Background()
	c, srv := newTestClient(t, TierAnonymous)
	d := NewDuration("timed", Dims("target"))

	func() {
		defer d.Start(ctx, "openapi")()
		time.Sleep(2 * time.Millisecond)
	}()
	done := d.StartLate(ctx)
	time.Sleep(2 * time.Millisecond)
	done("late")
	done("again") // only the first call records

	// The same helpers on a handle bound to a client.
	c.Duration("timed", Dims("target")).Start(ctx, "bound")()
	flush(t, c)

	got := map[string]uint64{}
	for _, p := range dimPoints(srv) {
		if p.metric == MetricMeasure && p.name == "timed" {
			got[p.dims["target"]] = p.count
		}
	}
	for _, target := range []string{"openapi", "late", "bound"} {
		if got[target] != 1 {
			t.Errorf("target %q count = %d, want 1 (all: %v)", target, got[target], got)
		}
	}
}

func TestPackageHandlesConcurrentUse(t *testing.T) {
	ctx := context.Background()
	c, srv := newTestClient(t, TierAnonymous)
	k := NewCounter("concurrent.counter", Dims("a"))
	d := NewDuration("concurrent.duration")
	v := NewValue("concurrent.value")

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				k.Add(ctx, 1, "x")
				d.Start(ctx)()
				v.Record(ctx, float64(i))
				if g == 0 && i%50 == 0 {
					SetDefault(c) // rebinding while others record
				}
			}
		}(g)
	}
	wg.Wait()
	flush(t, c)

	for _, p := range dimPoints(srv) {
		if p.name == "concurrent.counter" && p.value != 8*200 {
			t.Errorf("counter = %d, want %d", p.value, 8*200)
		}
	}
}
