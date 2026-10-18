package argvio

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

// stubUsage replaces the process usage reader for the length of the test.
func stubUsage(t *testing.T, rss, cpu int64) {
	t.Helper()
	orig := readProcessUsage
	readProcessUsage = func() (int64, int64) { return rss, cpu }
	t.Cleanup(func() { readProcessUsage = orig })
}

func TestProcessUsageReportsPlausibleValues(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "netbsd", "openbsd":
	default:
		rss, cpu := processUsage()
		if rss != 0 || cpu != 0 {
			t.Fatalf("processUsage() = (%d, %d) on %s, want zeros", rss, cpu, runtime.GOOS)
		}
		return
	}

	// Burn a little CPU so the counters are comfortably non-zero.
	deadline := time.Now().Add(20 * time.Millisecond)
	for sum := 0; time.Now().Before(deadline); sum++ {
	}

	rss, cpu := processUsage()
	// A Go test binary is never smaller than 1 MiB resident; a value
	// below that means Maxrss was not scaled to bytes.
	if rss < 1<<20 {
		t.Errorf("peak RSS = %d bytes, want at least 1 MiB (is Maxrss normalised to bytes?)", rss)
	}
	if cpu <= 0 {
		t.Errorf("CPU time = %d ms, want > 0", cpu)
	}
}

func TestRecordCommandProcessUsageAtBasicTier(t *testing.T) {
	stubUsage(t, 123456789, 4321)
	c, srv := newTestClient(t, TierBasic)

	c.RecordCommand(context.Background(), CommandResult{Path: "testcli run", Duration: time.Millisecond})
	flush(t, c)

	span := srv.Span(SpanCommandPrefix + "run")
	if span == nil {
		t.Fatal("expected a command span")
	}
	rss := fakeotlp.Attr(span.GetAttributes(), AttrPeakRSSBytes)
	if rss == nil || rss.GetIntValue() != 123456789 {
		t.Errorf("%s = %v, want int 123456789", AttrPeakRSSBytes, rss)
	}
	cpu := fakeotlp.Attr(span.GetAttributes(), AttrCPUTimeMs)
	if cpu == nil || cpu.GetIntValue() != 4321 {
		t.Errorf("%s = %v, want int 4321", AttrCPUTimeMs, cpu)
	}

	// The allowlist permits these on the command span only.
	rec := srv.LogRecord(EventCommandCompleted)
	if rec == nil {
		t.Fatal("expected a command completed event")
	}
	for _, key := range []string{AttrPeakRSSBytes, AttrCPUTimeMs} {
		if fakeotlp.Attr(rec.GetAttributes(), key) != nil {
			t.Errorf("%s must not be sent on %s", key, EventCommandCompleted)
		}
	}
}

func TestRecordCommandProcessUsageAtFullTier(t *testing.T) {
	stubUsage(t, 1000, 2)
	c, srv := newTestClient(t, TierFull)

	c.RecordCommand(context.Background(), CommandResult{Path: "testcli run"})
	flush(t, c)

	span := srv.Span(SpanCommandPrefix + "run")
	if span == nil {
		t.Fatal("expected a command span")
	}
	for _, key := range []string{AttrPeakRSSBytes, AttrCPUTimeMs} {
		if fakeotlp.Attr(span.GetAttributes(), key) == nil {
			t.Errorf("%s missing at TierFull", key)
		}
	}
}

func TestRecordCommandProcessUsageGatedBelowBasic(t *testing.T) {
	called := false
	orig := readProcessUsage
	readProcessUsage = func() (int64, int64) { called = true; return 1000, 2 }
	t.Cleanup(func() { readProcessUsage = orig })

	c, srv := newTestClient(t, TierAnonymous)
	c.RecordCommand(context.Background(), CommandResult{Path: "testcli run"})
	flush(t, c)

	span := srv.Span(SpanCommandPrefix + "run")
	if span == nil {
		t.Fatal("expected a command span at TierAnonymous")
	}
	for _, key := range []string{AttrPeakRSSBytes, AttrCPUTimeMs} {
		if fakeotlp.Attr(span.GetAttributes(), key) != nil {
			t.Errorf("TierAnonymous must not include %s", key)
		}
	}
	if called {
		t.Error("TierAnonymous should not even read the process counters")
	}
}

func TestRecordCommandOmitsUnavailableProcessUsage(t *testing.T) {
	tests := []struct {
		name             string
		rss, cpu         int64
		wantRSS, wantCPU bool
	}{
		{"neither measurable", 0, 0, false, false},
		{"negative values", -1, -5, false, false},
		{"only memory", 4096, 0, true, false},
		{"only cpu", 0, 7, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubUsage(t, tt.rss, tt.cpu)
			c, srv := newTestClient(t, TierBasic)

			c.RecordCommand(context.Background(), CommandResult{Path: "testcli run", Duration: time.Millisecond})
			flush(t, c)

			span := srv.Span(SpanCommandPrefix + "run")
			if span == nil {
				t.Fatal("expected a command span even when usage is unavailable")
			}
			if got := fakeotlp.Attr(span.GetAttributes(), AttrPeakRSSBytes) != nil; got != tt.wantRSS {
				t.Errorf("%s present = %v, want %v", AttrPeakRSSBytes, got, tt.wantRSS)
			}
			if got := fakeotlp.Attr(span.GetAttributes(), AttrCPUTimeMs) != nil; got != tt.wantCPU {
				t.Errorf("%s present = %v, want %v", AttrCPUTimeMs, got, tt.wantCPU)
			}
			if fakeotlp.Attr(span.GetAttributes(), AttrDurationMs) == nil {
				t.Error("duration must still be sent when usage is unavailable")
			}
		})
	}
}

func TestRecordCommandSurvivesPanickingUsageReader(t *testing.T) {
	orig := readProcessUsage
	readProcessUsage = func() (int64, int64) { panic("boom") }
	t.Cleanup(func() { readProcessUsage = orig })

	c, _ := newTestClient(t, TierBasic)
	c.RecordCommand(context.Background(), CommandResult{Path: "testcli run"}) // must not panic
}
