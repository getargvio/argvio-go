package argvio

import (
	"context"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestShellName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/bin/bash", ShellBash},
		{"/usr/bin/zsh", ShellZsh},
		{"/usr/local/bin/fish", ShellFish},
		{"/bin/sh", ShellSh},
		{"/bin/dash", ShellDash},
		{"/bin/ksh93", ShellKsh},
		{"/bin/tcsh", ShellTcsh},
		{"/bin/csh", ShellCsh},
		{"/usr/bin/nu", ShellNushell},
		{"/usr/bin/elvish", ShellElvish},
		{"/usr/bin/xonsh", ShellXonsh},
		{"/usr/bin/pwsh", ShellPowerShell},
		{`C:\Windows\System32\cmd.exe`, ShellCmd},
		{`C:\Program Files\Git\usr\bin\BASH.EXE`, ShellBash},
		{"-bash", ShellBash},
		{"bash", ShellBash},
		{"/home/alice/bin/my-secret-shell", ShellOther},
		{"/opt/bash-5.2/bin/custom", ShellOther},
		{"/bin/bash\nfoo", ShellOther},
		{"/bin/", ShellOther},
		{"/", ShellOther},
		{strings.Repeat("a", 5000), ShellOther},
	}
	for _, tt := range tests {
		if got := shellName(tt.in); got != tt.want {
			t.Errorf("shellName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDetectShell(t *testing.T) {
	psUser := `C:\Users\alice\Documents\PowerShell\Modules;C:\Program Files\PowerShell\Modules;C:\WINDOWS\system32\WindowsPowerShell\v1.0\Modules`
	psSystem := `C:\Program Files\WindowsPowerShell\Modules;C:\WINDOWS\system32\WindowsPowerShell\v1.0\Modules`
	tests := []struct {
		name   string
		env    map[string]string
		goos   string
		want   string
		wantOK bool
	}{
		{"unix zsh", map[string]string{"SHELL": "/bin/zsh"}, "linux", ShellZsh, true},
		{"unix unset", nil, "linux", "", false},
		{"unix ignores windows variables", map[string]string{"ComSpec": `C:\Windows\System32\cmd.exe`}, "linux", "", false},
		{"unknown shell is other", map[string]string{"SHELL": "/usr/bin/whatever"}, "darwin", ShellOther, true},
		{"git bash on windows", map[string]string{"SHELL": `C:\Program Files\Git\usr\bin\bash.exe`, "ComSpec": `C:\Windows\System32\cmd.exe`}, "windows", ShellBash, true},
		{"powershell", map[string]string{"PSModulePath": psUser, "ComSpec": `C:\Windows\System32\cmd.exe`}, "windows", ShellPowerShell, true},
		{"windows powershell 5", map[string]string{"PSModulePath": `C:\Users\alice\OneDrive\Documents\WindowsPowerShell\Modules;` + psSystem}, "windows", ShellPowerShell, true},
		{"cmd", map[string]string{"PSModulePath": psSystem, "ComSpec": `C:\Windows\System32\cmd.exe`}, "windows", ShellCmd, true},
		{"cmd without PSModulePath", map[string]string{"ComSpec": `C:\Windows\System32\cmd.exe`}, "windows", ShellCmd, true},
		{"windows nothing", nil, "windows", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := detectShell(envFrom(tt.env), tt.goos)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("detectShell = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDetectTerminal(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing", nil, TerminalUnknown},
		{"raw TERM alone is not a marker", map[string]string{"TERM": "xterm-256color"}, TerminalUnknown},
		{"apple terminal", map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, TerminalAppleTerminal},
		{"iterm via program", map[string]string{"TERM_PROGRAM": "iTerm.app"}, TerminalITerm2},
		{"iterm via session", map[string]string{"ITERM_SESSION_ID": "w0t0p0:ABC"}, TerminalITerm2},
		{"vscode", map[string]string{"TERM_PROGRAM": "vscode"}, TerminalVSCode},
		{"wezterm", map[string]string{"TERM_PROGRAM": "WezTerm"}, TerminalWezTerm},
		{"wezterm pane", map[string]string{"WEZTERM_PANE": "3"}, TerminalWezTerm},
		{"ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, TerminalGhostty},
		{"ghostty resources", map[string]string{"GHOSTTY_RESOURCES_DIR": "/usr/share/ghostty"}, TerminalGhostty},
		{"hyper", map[string]string{"TERM_PROGRAM": "Hyper"}, TerminalHyper},
		{"warp", map[string]string{"TERM_PROGRAM": "WarpTerminal"}, TerminalWarp},
		{"windows terminal", map[string]string{"WT_SESSION": "1234-abcd"}, TerminalWindowsTerminal},
		{"kitty", map[string]string{"KITTY_WINDOW_ID": "1"}, TerminalKitty},
		{"alacritty socket", map[string]string{"ALACRITTY_SOCKET": "/run/user/1000/alacritty.sock"}, TerminalAlacritty},
		{"alacritty log", map[string]string{"ALACRITTY_LOG": "/tmp/Alacritty-1.log"}, TerminalAlacritty},
		{"konsole", map[string]string{"KONSOLE_VERSION": "230401"}, TerminalKonsole},
		{"vte", map[string]string{"VTE_VERSION": "7003"}, TerminalVTE},
		{"jetbrains", map[string]string{"TERMINAL_EMULATOR": "JetBrains-JediTerm"}, TerminalJetBrains},
		{"tmux alone", map[string]string{"TMUX": "/tmp/tmux-1000/default,1,0", "TERM_PROGRAM": "tmux"}, TerminalTmux},
		{"tmux without program", map[string]string{"TMUX": "/tmp/tmux-1000/default,1,0"}, TerminalTmux},
		{"screen", map[string]string{"STY": "1234.pts-0.host"}, TerminalScreen},
		{"emulator under tmux wins", map[string]string{"TMUX": "x", "TERM_PROGRAM": "tmux", "KITTY_WINDOW_ID": "1"}, TerminalKitty},
		{"specific emulator before vte", map[string]string{"VTE_VERSION": "7003", "KITTY_WINDOW_ID": "1"}, TerminalKitty},
		{"unrecognised program is other", map[string]string{"TERM_PROGRAM": "SomeNewTerm"}, TerminalOther},
		{"marker beats unrecognised program", map[string]string{"TERM_PROGRAM": "SomeNewTerm", "WT_SESSION": "1"}, TerminalWindowsTerminal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectTerminal(envFrom(tt.env)); got != tt.want {
				t.Errorf("detectTerminal = %q, want %q", got, tt.want)
			}
		})
	}
}

// Whatever the environment holds, only vocabulary names come out: a
// spoofed or garbage value must never be forwarded.
func TestDetectTerminalNeverForwardsEnvironmentValues(t *testing.T) {
	vocabulary := map[string]bool{
		TerminalAppleTerminal: true, TerminalITerm2: true, TerminalVSCode: true,
		TerminalWindowsTerminal: true, TerminalWezTerm: true, TerminalKitty: true,
		TerminalAlacritty: true, TerminalGhostty: true, TerminalHyper: true,
		TerminalWarp: true, TerminalKonsole: true, TerminalVTE: true,
		TerminalJetBrains: true, TerminalTmux: true, TerminalScreen: true,
		TerminalOther: true, TerminalUnknown: true,
	}
	garbage := []string{
		"secret-token-abc123", "/home/alice/projects", "\x00\xff\xfe", "alice@example.com",
		"tmux\nsecret", " vscode", strings.Repeat("x", 100000), "WezTerm; rm -rf /",
	}
	for _, g := range garbage {
		for _, key := range []string{"TERM_PROGRAM", "TERM", "TERMINAL_EMULATOR", "TMUX", "STY", "VTE_VERSION", "WT_SESSION", "KITTY_WINDOW_ID"} {
			got := detectTerminal(envFrom(map[string]string{key: g}))
			if !vocabulary[got] {
				t.Errorf("%s=%q produced %q, outside the vocabulary", key, g, got)
			}
		}
	}
	for _, g := range garbage {
		got, ok := detectShell(envFrom(map[string]string{"SHELL": g}), "linux")
		if !ok || (got != ShellOther && got != ShellSh && got != ShellBash) {
			t.Errorf("SHELL=%q produced (%q, %v), want other", g, got, ok)
		}
	}
}

func TestRuntimeVersion(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"go1.22.1", "1.22.1", true},
		{"go1.26.0", "1.26.0", true},
		{"go1.22", "1.22", true},
		{"go1.23rc1", "1.23rc1", true},
		{"go1.22.1 X:boringcrypto", "1.22.1", true},
		{"go1.22.1-bigcorp", "1.22.1", true},
		{"go1.22.1+build5", "1.22.1", true},
		{"devel go1.23-abcdef Mon Jan 1 00:00:00 2024 +0000", "", false},
		{"devel +abcdef", "", false},
		{"", "", false},
		{"go", "", false},
		{"go-secret", "", false},
		{"gofoo", "", false},
		{"go1.22/home/alice", "", false},
		{"go" + strings.Repeat("1", 40), "", false},
	}
	for _, tt := range tests {
		got, ok := runtimeVersion(tt.in)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("runtimeVersion(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestSessionEnvironmentOmitsWhatItCannotDetect(t *testing.T) {
	noTTY := func() (bool, bool) { return false, false }
	attrs := sessionEnvironment(envFrom(nil), "linux", "devel go1.23-abc", noTTY)
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range attrs {
		got[kv.Key] = kv.Value
	}
	for _, key := range []string{AttrShell, AttrStdoutIsTTY, AttrRuntimeVersion} {
		if _, ok := got[attribute.Key(key)]; ok {
			t.Errorf("%s should be omitted when it can't be detected", key)
		}
	}
	if v := got[AttrTerminal]; v.AsString() != TerminalUnknown {
		t.Errorf("%s = %v, want %q", AttrTerminal, v, TerminalUnknown)
	}
	if v := got[AttrRuntimeName]; v.AsString() != "go" {
		t.Errorf("%s = %v, want go", AttrRuntimeName, v)
	}
}

func TestSessionEnvironmentReportsEverythingDetected(t *testing.T) {
	env := map[string]string{"SHELL": "/bin/zsh", "TERM_PROGRAM": "vscode"}
	attrs := sessionEnvironment(envFrom(env), "linux", "go1.22.1", func() (bool, bool) { return false, true })
	got := map[attribute.Key]attribute.Value{}
	for _, kv := range attrs {
		got[kv.Key] = kv.Value
	}
	if got[AttrShell].AsString() != ShellZsh || got[AttrTerminal].AsString() != TerminalVSCode ||
		got[AttrRuntimeName].AsString() != "go" || got[AttrRuntimeVersion].AsString() != "1.22.1" {
		t.Errorf("unexpected attributes: %v", attrs)
	}
	// A piped stdout is a real value, not an omission.
	v, ok := got[AttrStdoutIsTTY]
	if !ok || v.AsBool() {
		t.Errorf("%s = (%v, %v), want false present", AttrStdoutIsTTY, v, ok)
	}
}

func TestStdoutIsTerminal(t *testing.T) {
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })

	// A pipe is not a terminal.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })
	os.Stdout = w
	if isTTY, ok := stdoutIsTerminal(); !ok || isTTY {
		t.Errorf("pipe: stdoutIsTerminal = (%v, %v), want (false, true)", isTTY, ok)
	}

	// A regular file is not a terminal.
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	os.Stdout = f
	if isTTY, ok := stdoutIsTerminal(); !ok || isTTY {
		t.Errorf("file: stdoutIsTerminal = (%v, %v), want (false, true)", isTTY, ok)
	}

	// /dev/null is a character device but not a terminal.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = null.Close() })
	os.Stdout = null
	if isTTY, ok := stdoutIsTerminal(); !ok || isTTY {
		t.Errorf("devnull: stdoutIsTerminal = (%v, %v), want (false, true)", isTTY, ok)
	}

	// A closed file can't be stat'ed: unknown, not false.
	closed, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	os.Stdout = closed
	if _, ok := stdoutIsTerminal(); ok {
		t.Error("closed stdout: want ok = false")
	}
}

// stubEnvironment replaces the environment reader for the length of the
// test and counts its calls.
func stubEnvironment(t *testing.T, attrs ...attribute.KeyValue) *int {
	t.Helper()
	calls := 0
	orig := readSessionEnvironment
	readSessionEnvironment = func() []attribute.KeyValue { calls++; return attrs }
	t.Cleanup(func() { readSessionEnvironment = orig })
	return &calls
}

func TestSessionStartEnvironmentAtBasicTier(t *testing.T) {
	for _, tier := range []Tier{TierBasic, TierFull, TierOptIn} {
		t.Run(tier.String(), func(t *testing.T) {
			stubEnvironment(t,
				attribute.String(AttrShell, ShellZsh),
				attribute.String(AttrTerminal, TerminalKitty),
				attribute.Bool(AttrStdoutIsTTY, true),
				attribute.String(AttrRuntimeName, "go"),
				attribute.String(AttrRuntimeVersion, "1.22.1"),
			)
			c, srv := newTestClient(t, tier)

			c.RecordSessionStart(context.Background())
			flush(t, c)

			rec := srv.LogRecord(EventSessionStarted)
			if rec == nil {
				t.Fatal("expected a session started event")
			}
			for key, want := range map[string]string{
				AttrShell: ShellZsh, AttrTerminal: TerminalKitty,
				AttrRuntimeName: "go", AttrRuntimeVersion: "1.22.1",
			} {
				v := fakeotlp.Attr(rec.GetAttributes(), key)
				if v == nil || v.GetStringValue() != want {
					t.Errorf("%s = %v, want %q", key, v, want)
				}
			}
			if v := fakeotlp.Attr(rec.GetAttributes(), AttrStdoutIsTTY); v == nil || !v.GetBoolValue() {
				t.Errorf("%s = %v, want true", AttrStdoutIsTTY, v)
			}
		})
	}
}

func TestSessionStartEnvironmentGatedBelowBasic(t *testing.T) {
	calls := stubEnvironment(t,
		attribute.String(AttrShell, ShellZsh),
		attribute.String(AttrTerminal, TerminalKitty),
		attribute.Bool(AttrStdoutIsTTY, true),
		attribute.String(AttrRuntimeName, "go"),
		attribute.String(AttrRuntimeVersion, "1.22.1"),
	)
	c, srv := newTestClient(t, TierAnonymous)

	c.RecordSessionStart(context.Background())
	flush(t, c)

	rec := srv.LogRecord(EventSessionStarted)
	if rec == nil {
		t.Fatal("expected a session started event at TierAnonymous")
	}
	for _, key := range []string{AttrShell, AttrTerminal, AttrStdoutIsTTY, AttrRuntimeName, AttrRuntimeVersion} {
		if fakeotlp.Attr(rec.GetAttributes(), key) != nil {
			t.Errorf("TierAnonymous must not include %s", key)
		}
	}
	if *calls != 0 {
		t.Errorf("TierAnonymous should not inspect the environment (called %d times)", *calls)
	}
}

func TestSessionStartEnvironmentOnRealHost(t *testing.T) {
	c, srv := newTestClient(t, TierBasic)

	c.RecordSessionStart(context.Background())
	flush(t, c)

	rec := srv.LogRecord(EventSessionStarted)
	if rec == nil {
		t.Fatal("expected a session started event")
	}
	if v := fakeotlp.Attr(rec.GetAttributes(), AttrRuntimeName); v == nil || v.GetStringValue() != "go" {
		t.Errorf("%s = %v, want go", AttrRuntimeName, v)
	}
	if v := fakeotlp.Attr(rec.GetAttributes(), AttrTerminal); v == nil || v.GetStringValue() == "" {
		t.Errorf("%s = %v, want a value", AttrTerminal, v)
	}
}
