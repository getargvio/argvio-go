package cobrasdk

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	argvio "github.com/getargvio/argvio-go"
	"github.com/getargvio/argvio-go/internal/fakeotlp"
)

// newTreeCLI is a CLI with a runnable leaf at the root ("deploy"), a
// non-runnable parent ("config") with subcommands, and a hidden command.
func newTreeCLI() *cobra.Command {
	root := &cobra.Command{Use: "samplecli", Version: "9.9.9"}
	deploy := &cobra.Command{Use: "deploy", RunE: func(*cobra.Command, []string) error { return nil }}
	config := &cobra.Command{Use: "config"}
	config.AddCommand(
		&cobra.Command{Use: "set", Run: func(*cobra.Command, []string) {}},
		&cobra.Command{Use: "get", Run: func(*cobra.Command, []string) {}},
	)
	root.AddCommand(deploy, config,
		&cobra.Command{Use: "secret-internal", Hidden: true, Run: func(*cobra.Command, []string) {}})
	return root
}

// runExecute runs root through Execute with args and returns what cobra
// printed and its error.
func runExecute(t *testing.T, root *cobra.Command, client *argvio.Client, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := Execute(root, client)
	return out.String(), err
}

// wireText renders everything the fake collector received.
func wireText(srv *fakeotlp.Server) string {
	var sb strings.Builder
	for _, m := range srv.Logs() {
		sb.WriteString(m.String())
	}
	for _, m := range srv.Traces() {
		sb.WriteString(m.String())
	}
	for _, m := range srv.Metrics() {
		sb.WriteString(m.String())
	}
	for _, h := range srv.Headers() {
		for k, v := range h {
			sb.WriteString(k + "=" + strings.Join(v, ",") + "\n")
		}
	}
	return sb.String()
}

func TestExecuteRecordsUnknownRootCommandWithSuggestion(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newTreeCLI()
	InstrumentCobra(root, client)

	_, err := runExecute(t, root, client, "deploi")
	flush(t, client)

	if err == nil {
		t.Fatal("expected cobra's unknown command error")
	}
	if got, ok := eventAttr(srv, argvio.EventCommandUnknown, argvio.AttrSuggestion); !ok || got != "deploy" {
		t.Fatalf("suggestion = (%q, %v), want deploy", got, ok)
	}
	if hasEvent(srv, argvio.EventCommandInvoked) || hasEvent(srv, argvio.EventCommandCompleted) {
		t.Fatal("an unknown command was recorded as an invocation")
	}
}

func TestExecuteLeavesTheHostCLIsFailureUntouched(t *testing.T) {
	client, _ := newTestClient(t, argvio.TierBasic)

	plain := newTreeCLI()
	wantOut, wantErr := runExecute(t, plain, nil, "deploi")

	instrumented := newTreeCLI()
	InstrumentCobra(instrumented, client)
	gotOut, gotErr := runExecute(t, instrumented, client, "deploi")

	if wantErr == nil || gotErr == nil || gotErr.Error() != wantErr.Error() {
		t.Fatalf("error = %v, want %v", gotErr, wantErr)
	}
	if gotOut != wantOut {
		t.Fatalf("output changed:\n got %q\nwant %q", gotOut, wantOut)
	}
}

func TestUnknownRootCommandWithNoCloseCommandIsABareAttempt(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newTreeCLI()
	InstrumentCobra(root, client)

	_, _ = runExecute(t, root, client, "zzzzzzzz")
	flush(t, client)

	if !hasEvent(srv, argvio.EventCommandUnknown) {
		t.Fatal("attempt not recorded")
	}
	if _, ok := eventAttr(srv, argvio.EventCommandUnknown, argvio.AttrSuggestion); ok {
		t.Fatal("suggestion sent although no command is close")
	}
}

func TestUnknownCommandSuggestionIsGatedByTier(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierAnonymous)
	root := newTreeCLI()
	InstrumentCobra(root, client)

	_, _ = runExecute(t, root, client, "deploi")
	flush(t, client)

	if !hasEvent(srv, argvio.EventCommandUnknown) {
		t.Fatal("attempt not recorded at anonymous tier")
	}
	if _, ok := eventAttr(srv, argvio.EventCommandUnknown, argvio.AttrSuggestion); ok {
		t.Fatal("suggestion sent at anonymous tier")
	}
}

func TestMistypedSubcommandOfNonRunnableParentIsRecorded(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newTreeCLI()
	InstrumentCobra(root, client)

	out, err := runExecute(t, root, client, "config", "sett")
	flush(t, client)

	if err != nil {
		t.Fatalf("cobra's behaviour changed, err = %v", err)
	}
	if !strings.Contains(out, "Available Commands") {
		t.Fatalf("help no longer shown: %q", out)
	}
	if got, ok := eventAttr(srv, argvio.EventCommandUnknown, argvio.AttrSuggestion); !ok || got != "config.set" {
		t.Fatalf("suggestion = (%q, %v), want config.set", got, ok)
	}
	if hasEvent(srv, argvio.EventHelpViewed) {
		t.Fatal("a mistyped command was also counted as a help view")
	}
}

func TestPlainHelpOfNonRunnableParentIsNotAnUnknownCommand(t *testing.T) {
	for _, args := range [][]string{{"config"}, {"config", "--help"}, {"help", "config"}, {"config", "--help", "sett"}} {
		client, srv := newTestClient(t, argvio.TierBasic)
		root := newTreeCLI()
		InstrumentCobra(root, client)

		_, _ = runExecute(t, root, client, args...)
		flush(t, client)

		if hasEvent(srv, argvio.EventCommandUnknown) {
			t.Errorf("%v recorded as an unknown command", args)
		}
		if !hasEvent(srv, argvio.EventHelpViewed) {
			t.Errorf("%v no longer recorded as a help view", args)
		}
	}
}

func TestSuccessfulAndFailingCommandsAreNotUnknown(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newTreeCLI()
	InstrumentCobra(root, client)

	_, _ = runExecute(t, root, client, "deploy")
	_, _ = runExecute(t, root, client, "config", "set")
	flush(t, client)

	if hasEvent(srv, argvio.EventCommandUnknown) {
		t.Fatal("a known command was recorded as unknown")
	}
}

func TestMisusedLeafIsNotAnUnknownCommand(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newTreeCLI()
	for _, c := range root.Commands() {
		if c.Name() == "deploy" {
			c.Args = cobra.NoArgs // "unknown command "x" for "samplecli deploy""
		}
	}
	InstrumentCobra(root, client)

	_, err := runExecute(t, root, client, "deploy", "extra")
	flush(t, client)

	if err == nil || !strings.HasPrefix(err.Error(), "unknown command") {
		t.Fatalf("test premise broken, err = %v", err)
	}
	if hasEvent(srv, argvio.EventCommandUnknown) {
		t.Fatal("positional arguments to a leaf were recorded as an unknown command")
	}
}

func TestNeverSuggestsHiddenCommandsOrSubstrings(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newTreeCLI()
	InstrumentCobra(root, client)

	// "dep" is a prefix of deploy, and "secret-internl" is close to a hidden
	// command; neither may be suggested.
	_, _ = runExecute(t, root, client, "dep")
	_, _ = runExecute(t, root, client, "secret-internl")
	flush(t, client)

	if got, ok := eventAttr(srv, argvio.EventCommandUnknown, argvio.AttrSuggestion); ok {
		t.Fatalf("suggested %q", got)
	}
}

func TestSuggestForAndDisableSuggestions(t *testing.T) {
	client, srv := newTestClient(t, argvio.TierBasic)
	root := newTreeCLI()
	for _, c := range root.Commands() {
		if c.Name() == "deploy" {
			c.SuggestFor = []string{"publish"}
		}
	}
	InstrumentCobra(root, client)
	_, _ = runExecute(t, root, client, "publish")
	flush(t, client)
	if got, _ := eventAttr(srv, argvio.EventCommandUnknown, argvio.AttrSuggestion); got != "deploy" {
		t.Fatalf("suggestion = %q, want deploy", got)
	}

	client2, srv2 := newTestClient(t, argvio.TierBasic)
	root2 := newTreeCLI()
	root2.DisableSuggestions = true
	InstrumentCobra(root2, client2)
	_, _ = runExecute(t, root2, client2, "deploi")
	flush(t, client2)
	if !hasEvent(srv2, argvio.EventCommandUnknown) {
		t.Fatal("attempt not recorded")
	}
	if _, ok := eventAttr(srv2, argvio.EventCommandUnknown, argvio.AttrSuggestion); ok {
		t.Fatal("suggestion sent although the vendor disabled suggestions")
	}
}

func TestTypedTextNeverReachesTheWire(t *testing.T) {
	//nolint:gosec // deliberately secret-looking input: it must never reach the wire
	typed := map[string]string{
		"secret":    "ghp_R3allyS3cretT0kenValue0123456789abcdef",
		"path":      "/home/alice/.ssh/id_rsa",
		"url":       "https://user:p4ssw0rd@internal.example.com/x?token=abc",
		"sentence":  "please-deploy-my-private-thing-now",
		"near-miss": "deploi", // gets a suggestion; the typo itself must not be sent
		"long":      "deploy" + strings.Repeat("Z9", 100000),
		"unicode":   "déploy-ünïcode-セキュリティ",
	}
	for _, tier := range []argvio.Tier{argvio.TierAnonymous, argvio.TierBasic, argvio.TierFull, argvio.TierOptIn} {
		for name, text := range typed {
			for _, nested := range []bool{false, true} {
				client, srv := newTestClient(t, tier)
				root := newTreeCLI()
				InstrumentCobra(root, client)

				args := []string{text}
				if nested {
					args = []string{"config", text}
				}
				_, _ = runExecute(t, root, client, args...)
				flush(t, client)

				if !hasEvent(srv, argvio.EventCommandUnknown) {
					t.Errorf("%s/%s: attempt not recorded", tier, name)
				}
				wire := wireText(srv)
				if !strings.Contains(wire, argvio.EventCommandUnknown) {
					t.Fatalf("%s/%s: wire dump is missing the event, the check below would be vacuous", tier, name)
				}
				// The whole text and every distinctive piece of it.
				for _, needle := range []string{text, "ghp_", "R3allyS3cret", ".ssh", "alice", "p4ssw0rd",
					"internal.example.com", "please-deploy", "ünïcode", "セキュリティ", "Z9Z9Z9"} {
					if strings.Contains(text, needle) && strings.Contains(wire, needle) {
						t.Errorf("%s/%s nested=%v: %q reached the wire", tier, name, nested, needle)
					}
				}
				rec := srv.LogRecord(argvio.EventCommandUnknown)
				for _, kv := range rec.GetAttributes() {
					switch kv.GetKey() {
					case argvio.AttrSuggestion, argvio.ResourceAttrTier:
					default:
						t.Errorf("%s/%s: unexpected attribute %q", tier, name, kv.GetKey())
					}
				}
			}
		}
	}
}
