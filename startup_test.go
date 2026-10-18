package argvio

import (
	"context"
	"testing"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

// stubBuildCost replaces the binary size and startup readers for the
// length of the test.
func stubBuildCost(t *testing.T, size int64, sizeOK bool, startupMs int64) {
	t.Helper()
	origSize, origStartup := readBinarySize, readStartupMs
	readBinarySize = func() (int64, bool) { return size, sizeOK }
	readStartupMs = func() int64 { return startupMs }
	t.Cleanup(func() { readBinarySize, readStartupMs = origSize, origStartup })
}

func TestReadBinarySizeStatsTheExecutable(t *testing.T) {
	size, ok := readBinarySize()
	if !ok {
		t.Fatal("readBinarySize() = false, want the test binary's size")
	}
	// A Go test binary is never smaller than 1 MiB.
	if size < 1<<20 {
		t.Errorf("binary size = %d bytes, want at least 1 MiB", size)
	}
}

func TestReadStartupMsIsNonNegative(t *testing.T) {
	if ms := readStartupMs(); ms < 0 {
		t.Errorf("startup = %d ms, want >= 0", ms)
	}
}

func TestSessionStartBuildCostAtBasicTier(t *testing.T) {
	for _, tier := range []Tier{TierBasic, TierFull, TierOptIn} {
		t.Run(tier.String(), func(t *testing.T) {
			stubBuildCost(t, 12345678, true, 42)
			c, srv := newTestClient(t, tier)

			c.RecordSessionStart(context.Background())
			flush(t, c)

			rec := srv.LogRecord(EventSessionStarted)
			if rec == nil {
				t.Fatal("expected a session started event")
			}
			size := fakeotlp.Attr(rec.GetAttributes(), AttrBinarySizeBytes)
			if size == nil || size.GetIntValue() != 12345678 {
				t.Errorf("%s = %v, want int 12345678", AttrBinarySizeBytes, size)
			}
			startup := fakeotlp.Attr(rec.GetAttributes(), AttrStartupMs)
			if startup == nil || startup.GetIntValue() != 42 {
				t.Errorf("%s = %v, want int 42", AttrStartupMs, startup)
			}
		})
	}
}

func TestSessionStartBuildCostGatedBelowBasic(t *testing.T) {
	sizeCalled, startupCalled := false, false
	origSize, origStartup := readBinarySize, readStartupMs
	readBinarySize = func() (int64, bool) { sizeCalled = true; return 1000, true }
	readStartupMs = func() int64 { startupCalled = true; return 5 }
	t.Cleanup(func() { readBinarySize, readStartupMs = origSize, origStartup })

	c, srv := newTestClient(t, TierAnonymous)
	c.RecordSessionStart(context.Background())
	flush(t, c)

	rec := srv.LogRecord(EventSessionStarted)
	if rec == nil {
		t.Fatal("expected a session started event at TierAnonymous")
	}
	for _, key := range []string{AttrBinarySizeBytes, AttrStartupMs} {
		if fakeotlp.Attr(rec.GetAttributes(), key) != nil {
			t.Errorf("TierAnonymous must not include %s", key)
		}
	}
	if sizeCalled || startupCalled {
		t.Errorf("TierAnonymous should not measure anything (size=%v, startup=%v)", sizeCalled, startupCalled)
	}
}

func TestSessionStartOmitsUnmeasurableBuildCost(t *testing.T) {
	tests := []struct {
		name        string
		size        int64
		sizeOK      bool
		startup     int64
		wantSize    bool
		wantStartup bool
	}{
		{"size unresolvable", 0, false, 7, false, true},
		{"size reads as zero", 0, true, 7, false, true},
		{"startup negative", 4096, true, -1, true, false},
		{"startup zero is real", 4096, true, 0, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubBuildCost(t, tt.size, tt.sizeOK, tt.startup)
			c, srv := newTestClient(t, TierBasic)

			c.RecordSessionStart(context.Background())
			flush(t, c)

			rec := srv.LogRecord(EventSessionStarted)
			if rec == nil {
				t.Fatal("expected a session started event")
			}
			if got := fakeotlp.Attr(rec.GetAttributes(), AttrBinarySizeBytes) != nil; got != tt.wantSize {
				t.Errorf("%s present = %v, want %v", AttrBinarySizeBytes, got, tt.wantSize)
			}
			if got := fakeotlp.Attr(rec.GetAttributes(), AttrStartupMs) != nil; got != tt.wantStartup {
				t.Errorf("%s present = %v, want %v", AttrStartupMs, got, tt.wantStartup)
			}
		})
	}
}

func TestSessionStartMeasuresBuildCostOnce(t *testing.T) {
	sizeCalls := 0
	origSize := readBinarySize
	readBinarySize = func() (int64, bool) { sizeCalls++; return 1000, true }
	t.Cleanup(func() { readBinarySize = origSize })

	c, _ := newTestClient(t, TierBasic)
	c.RecordSessionStart(context.Background())
	c.RecordSessionStart(context.Background())
	flush(t, c)

	if sizeCalls != 1 {
		t.Errorf("binary size read %d times, want 1", sizeCalls)
	}
}
