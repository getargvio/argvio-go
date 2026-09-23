package argvio

import (
	"context"
	"testing"
)

// clearCIEnv unsets every env var any CI detector checks (provider-specific
// ones plus everything ciEnvVars covers for IsCI), so tests start from a
// clean slate regardless of the environment they run in (e.g. real CI).
func clearCIEnv(t *testing.T) {
	t.Helper()
	for _, name := range ciEnvVars {
		t.Setenv(name, "")
	}
	vars := []string{
		"GITHUB_ACTIONS", "GITHUB_EVENT_NAME",
		"GITLAB_CI", "CI_MERGE_REQUEST_IID",
		"CIRCLECI", "CIRCLE_PULL_REQUEST",
		"BUILDKITE", "BUILDKITE_PULL_REQUEST",
		"JENKINS_URL", "CHANGE_ID",
		"TRAVIS", "TRAVIS_PULL_REQUEST",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

func TestDetectCI_NoEnv_ReturnsNotOK(t *testing.T) {
	clearCIEnv(t)

	_, ok := DetectCI()
	if ok {
		t.Fatal("expected ok=false when no CI env vars are set")
	}
}

func TestDetectCI_GenericFallback(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("CI", "true")

	info, ok := DetectCI()
	if !ok {
		t.Fatal("expected ok=true when CI=true")
	}
	if info.Provider != CIProviderUnknown {
		t.Errorf("provider: got %q, want %q", info.Provider, CIProviderUnknown)
	}
}

func TestDetectCI_GitHubActions(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "pull_request")

	info, ok := DetectCI()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if info.Provider != CIProviderGitHubActions {
		t.Errorf("provider: got %q", info.Provider)
	}
	if !info.IsPullRequest {
		t.Error("expected IsPullRequest=true for pull_request event")
	}
}

func TestDetectCI_GitHubActions_NotPR(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "push")

	info, ok := DetectCI()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if info.IsPullRequest {
		t.Error("expected IsPullRequest=false for push event")
	}
}

func TestDetectCI_GitLabCI(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("GITLAB_CI", "true")
	t.Setenv("CI_MERGE_REQUEST_IID", "9")

	info, ok := DetectCI()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if info.Provider != CIProviderGitLabCI {
		t.Errorf("provider: got %q", info.Provider)
	}
	if !info.IsPullRequest {
		t.Error("expected IsPullRequest=true when CI_MERGE_REQUEST_IID is set")
	}
}

func TestDetectCI_CircleCI(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("CIRCLECI", "true")
	t.Setenv("CIRCLE_PULL_REQUEST", "https://github.com/cerberauth/x/pull/3")

	info, ok := DetectCI()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if info.Provider != CIProviderCircleCI {
		t.Errorf("provider: got %q", info.Provider)
	}
	if !info.IsPullRequest {
		t.Error("expected IsPullRequest=true when CIRCLE_PULL_REQUEST is set")
	}
}

func TestDetectCI_Buildkite(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("BUILDKITE", "true")
	t.Setenv("BUILDKITE_PULL_REQUEST", "false")

	info, ok := DetectCI()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if info.Provider != CIProviderBuildkite {
		t.Errorf("provider: got %q", info.Provider)
	}
	if info.IsPullRequest {
		t.Error("expected IsPullRequest=false when BUILDKITE_PULL_REQUEST=false")
	}
}

func TestDetectCI_Jenkins(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("JENKINS_URL", "https://jenkins.example.com")
	t.Setenv("CHANGE_ID", "7")

	info, ok := DetectCI()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if info.Provider != CIProviderJenkins {
		t.Errorf("provider: got %q", info.Provider)
	}
	if !info.IsPullRequest {
		t.Error("expected IsPullRequest=true when CHANGE_ID is set")
	}
}

func TestDetectCI_Travis(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("TRAVIS", "true")
	t.Setenv("TRAVIS_PULL_REQUEST", "false")

	info, ok := DetectCI()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if info.Provider != CIProviderTravis {
		t.Errorf("provider: got %q", info.Provider)
	}
	if info.IsPullRequest {
		t.Error("expected IsPullRequest=false when TRAVIS_PULL_REQUEST=false")
	}
}

func TestCIInfo_ResourceAttributes(t *testing.T) {
	info := CIInfo{Provider: CIProviderGitHubActions, IsPullRequest: true}
	attrs := info.resourceAttributes()

	if len(attrs) != 2 {
		t.Fatalf("expected 2 attributes, got %d: %v", len(attrs), attrs)
	}
	if attrs[0].key != ResourceAttrCIProviderName || attrs[0].value != CIProviderGitHubActions {
		t.Errorf("unexpected provider attribute: %v", attrs[0])
	}
	if attrs[1].key != ResourceAttrCIPullRequest || attrs[1].value != "true" {
		t.Errorf("unexpected isPR attribute: %v", attrs[1])
	}
}

func TestCIInfo_ResourceAttributes_EmptyProviderReturnsNil(t *testing.T) {
	info := CIInfo{}
	if attrs := info.resourceAttributes(); attrs != nil {
		t.Errorf("expected nil attributes for empty provider, got %v", attrs)
	}
}

func TestNew_AttachesCIResourceAttributesAutomatically(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "pull_request_target")

	c, srv := newTestClient(t, TierAnonymous)
	c.RecordSessionStart(context.Background())
	flush(t, c)

	if got, ok := resourceAttr(srv, ResourceAttrCIProviderName); !ok || got != CIProviderGitHubActions {
		t.Errorf("%s: got %q (present=%v), want %q", ResourceAttrCIProviderName, got, ok, CIProviderGitHubActions)
	}
	if got, ok := resourceAttr(srv, ResourceAttrCIPullRequest); !ok || got != "true" {
		t.Errorf("%s: got %q (present=%v), want %q", ResourceAttrCIPullRequest, got, ok, "true")
	}
}

func TestDetectCI_FalseValueIsNotDetected(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("GITHUB_ACTIONS", "false")

	if info, ok := DetectCI(); ok {
		t.Fatalf("expected ok=false for GITHUB_ACTIONS=false (IsCI agrees), got %+v", info)
	}
}
