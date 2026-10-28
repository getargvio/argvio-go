package argvio

import (
	"os"
	"runtime"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// This file detects the environment a CLI runs in — shell, terminal
// emulator, whether stdout is a terminal, and the Go runtime — for the
// session start event. The mapping tables below are the privacy control:
// every string that leaves the process comes out of one of them (or is a
// fixed name), never from an environment variable's raw value, so a
// spoofed or garbage variable can at worst land in "other". Detection is
// local and read-only: environment variables, one stat, no subprocesses.

// Shell names reported on AttrShell. Anything else is ShellOther.
const (
	ShellBash       = "bash"
	ShellZsh        = "zsh"
	ShellFish       = "fish"
	ShellSh         = "sh"
	ShellDash       = "dash"
	ShellKsh        = "ksh"
	ShellTcsh       = "tcsh"
	ShellCsh        = "csh"
	ShellNushell    = "nushell"
	ShellElvish     = "elvish"
	ShellXonsh      = "xonsh"
	ShellPowerShell = "powershell"
	ShellCmd        = "cmd"
	ShellOther      = "other"
)

// Terminal emulator names reported on AttrTerminal. A recognised marker
// maps to its name, an unrecognised one to TerminalOther, and no marker at
// all to TerminalUnknown.
const (
	TerminalAppleTerminal   = "apple_terminal"
	TerminalITerm2          = "iterm2"
	TerminalVSCode          = "vscode"
	TerminalWindowsTerminal = "windows_terminal"
	TerminalWezTerm         = "wezterm"
	TerminalKitty           = "kitty"
	TerminalAlacritty       = "alacritty"
	TerminalGhostty         = "ghostty"
	TerminalHyper           = "hyper"
	TerminalWarp            = "warp"
	TerminalKonsole         = "konsole"
	TerminalVTE             = "vte" // GNOME Terminal, Tilix and other VTE-based terminals
	TerminalJetBrains       = "jetbrains"
	TerminalTmux            = "tmux"
	TerminalScreen          = "screen"
	TerminalOther           = "other"
	TerminalUnknown         = "unknown"
)

// shellNames maps a lower-cased shell executable name, without any
// directory or ".exe", to the name reported for it.
var shellNames = map[string]string{
	"bash":       ShellBash,
	"zsh":        ShellZsh,
	"fish":       ShellFish,
	"sh":         ShellSh,
	"dash":       ShellDash,
	"ksh":        ShellKsh,
	"ksh93":      ShellKsh,
	"mksh":       ShellKsh,
	"tcsh":       ShellTcsh,
	"csh":        ShellCsh,
	"nu":         ShellNushell,
	"elvish":     ShellElvish,
	"xonsh":      ShellXonsh,
	"pwsh":       ShellPowerShell,
	"powershell": ShellPowerShell,
	"cmd":        ShellCmd,
}

// terminalMarkers are environment variables a terminal emulator sets for
// itself. Only their presence is read, never their value. They are checked
// first, in order, because they survive running under a multiplexer, which
// overrides TERM_PROGRAM. Specific emulators come before VTE_VERSION,
// which several of them share with their VTE base.
var terminalMarkers = []struct{ env, name string }{
	{"WT_SESSION", TerminalWindowsTerminal},
	{"KITTY_WINDOW_ID", TerminalKitty},
	{"ALACRITTY_SOCKET", TerminalAlacritty},
	{"ALACRITTY_LOG", TerminalAlacritty},
	{"ALACRITTY_WINDOW_ID", TerminalAlacritty},
	{"WEZTERM_PANE", TerminalWezTerm},
	{"WEZTERM_EXECUTABLE", TerminalWezTerm},
	{"GHOSTTY_RESOURCES_DIR", TerminalGhostty},
	{"ITERM_SESSION_ID", TerminalITerm2},
	{"KONSOLE_VERSION", TerminalKonsole},
	{"VTE_VERSION", TerminalVTE},
}

// termPrograms maps a lower-cased TERM_PROGRAM value to the name reported
// for it.
var termPrograms = map[string]string{
	"apple_terminal": TerminalAppleTerminal,
	"iterm.app":      TerminalITerm2,
	"vscode":         TerminalVSCode,
	"wezterm":        TerminalWezTerm,
	"kitty":          TerminalKitty,
	"alacritty":      TerminalAlacritty,
	"ghostty":        TerminalGhostty,
	"hyper":          TerminalHyper,
	"warpterminal":   TerminalWarp,
	"tmux":           TerminalTmux,
}

// detectShell returns the user's shell as a name from the fixed
// vocabulary, and false when it can't be determined. getenv returns ""
// for an unset variable.
//
// $SHELL decides wherever it is set, which includes Git Bash and MSYS on
// Windows. Otherwise, on Windows only, it falls back to heuristics:
// PowerShell prepends a per-user Documents\PowerShell(\WindowsPowerShell)
// entry to PSModulePath that cmd doesn't have, and ComSpec points at the
// command interpreter (cmd.exe). Both are best-effort: a cmd.exe started
// from PowerShell inherits PSModulePath and reads as PowerShell.
func detectShell(getenv func(string) string, goos string) (string, bool) {
	if sh := getenv("SHELL"); sh != "" {
		return shellName(sh), true
	}
	if goos != "windows" {
		return "", false
	}
	if hasPowerShellUserModulePath(getenv("PSModulePath")) {
		return ShellPowerShell, true
	}
	if getenv("ComSpec") != "" {
		return ShellCmd, true
	}
	return "", false
}

// shellName maps a shell path (either separator) to its vocabulary name,
// or ShellOther. The path itself is never returned.
func shellName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		path = path[i+1:]
	}
	path = strings.ToLower(strings.TrimPrefix(path, "-")) // "-bash": a login shell
	path = strings.TrimSuffix(path, ".exe")
	if name, ok := shellNames[path]; ok {
		return name
	}
	return ShellOther
}

func hasPowerShellUserModulePath(psModulePath string) bool {
	for _, entry := range strings.Split(psModulePath, ";") {
		entry = strings.ToLower(strings.ReplaceAll(entry, "/", `\`))
		if strings.HasSuffix(entry, `\documents\powershell\modules`) ||
			strings.HasSuffix(entry, `\documents\windowspowershell\modules`) {
			return true
		}
	}
	return false
}

// detectTerminal returns the terminal emulator as a name from the fixed
// vocabulary: TerminalOther when a marker is present but not recognised,
// TerminalUnknown when there is none. It never returns an environment
// value.
func detectTerminal(getenv func(string) string) string {
	for _, m := range terminalMarkers {
		if getenv(m.env) != "" {
			return m.name
		}
	}
	if strings.HasPrefix(getenv("TERMINAL_EMULATOR"), "JetBrains") {
		return TerminalJetBrains
	}
	program := getenv("TERM_PROGRAM")
	if name, ok := termPrograms[strings.ToLower(program)]; ok {
		return name
	}
	// A multiplexer is only the answer when nothing names the terminal
	// underneath it.
	if getenv("TMUX") != "" {
		return TerminalTmux
	}
	if getenv("STY") != "" {
		return TerminalScreen
	}
	if program != "" {
		return TerminalOther
	}
	return TerminalUnknown
}

// maxRuntimeVersionLength is the server's max_length for
// cli.runtime.version.
const maxRuntimeVersionLength = 32

// runtimeVersion extracts the release from a runtime.Version() string:
// "go1.22.1" gives "1.22.1", and build suffixes ("go1.22.1 X:boringcrypto",
// "go1.22.1-bigcorp", "go1.22.1+build") are dropped. It returns false for
// toolchains built from source ("devel go1.23-abc123 ...") or anything else
// that isn't a release version.
func runtimeVersion(v string) (string, bool) {
	v, ok := strings.CutPrefix(v, "go")
	if !ok {
		return "", false
	}
	if i := strings.IndexAny(v, " -+"); i >= 0 {
		v = v[:i]
	}
	if v == "" || len(v) > maxRuntimeVersionLength || v[0] < '0' || v[0] > '9' {
		return "", false
	}
	for i := 0; i < len(v); i++ {
		ch := v[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'z') && ch != '.' {
			return "", false // not a plain release like 1.22.1 or 1.23rc1
		}
	}
	return v, true
}

// stdoutIsTerminal reports whether standard output is a terminal, and
// false for ok when that can't be determined. It is a character-device
// check, which needs no dependency, with /dev/null (also a character
// device) ruled out.
func stdoutIsTerminal() (isTTY, ok bool) {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false, false
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		return false, true
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false, true
	}
	return true, true
}

// readSessionEnvironment returns the environment attributes for the
// session start event: shell, terminal, stdout TTY and runtime. An
// attribute that can't be determined is left out. It is a variable so
// tests can stand in for environments the host can't produce.
var readSessionEnvironment = func() []attribute.KeyValue {
	return sessionEnvironment(os.Getenv, runtime.GOOS, runtime.Version(), stdoutIsTerminal)
}

func sessionEnvironment(getenv func(string) string, goos, goVersion string, stdoutTTY func() (bool, bool)) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if shell, ok := detectShell(getenv, goos); ok {
		attrs = append(attrs, attribute.String(AttrShell, shell))
	}
	attrs = append(attrs, attribute.String(AttrTerminal, detectTerminal(getenv)))
	if isTTY, ok := stdoutTTY(); ok {
		attrs = append(attrs, attribute.Bool(AttrStdoutIsTTY, isTTY))
	}
	attrs = append(attrs, attribute.String(AttrRuntimeName, "go"))
	if v, ok := runtimeVersion(goVersion); ok {
		attrs = append(attrs, attribute.String(AttrRuntimeVersion, v))
	}
	return attrs
}
