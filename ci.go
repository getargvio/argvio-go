package argvio

import (
	"os"
	"strconv"
)

// CI provider names reported on the ResourceAttrCIProviderName resource
// attribute.
const (
	CIProviderGitHubActions = "github_actions"
	CIProviderGitLabCI      = "gitlab_ci"
	CIProviderCircleCI      = "circleci"
	CIProviderBuildkite     = "buildkite"
	CIProviderJenkins       = "jenkins"
	CIProviderTravis        = "travis"
	CIProviderUnknown       = "unknown"
)

// Resource attribute keys set automatically from DetectCI by New.
const (
	ResourceAttrCIProviderName = "cicd.provider.name"
	ResourceAttrCIPullRequest  = "cicd.pipeline.is_pull_request"
)

// CIInfo holds minimal information about the CI environment the process
// is running in: which provider it is, and whether the run is for a
// pull/merge request.
type CIInfo struct {
	Provider      string
	IsPullRequest bool
}

// DetectCI inspects well-known CI provider environment variables and
// reports the provider name and whether the run is for a pull/merge
// request. ok is false when no CI environment could be detected.
//
// The provider-specific cases identify things IsCI alone cannot (which
// provider, whether it's a PR); the catch-all case defers to IsCI,
// which already recognizes a broader set of CI systems than are worth
// enumerating individually here.
//
// New calls this automatically and attaches the result as resource
// attributes — vendors do not need to call DetectCI themselves unless
// they want the raw info for their own purposes.
func DetectCI() (info CIInfo, ok bool) {
	switch {
	case envTruthy("GITHUB_ACTIONS"):
		return CIInfo{Provider: CIProviderGitHubActions, IsPullRequest: isGitHubPREvent(os.Getenv("GITHUB_EVENT_NAME"))}, true
	case envTruthy("GITLAB_CI"):
		return CIInfo{Provider: CIProviderGitLabCI, IsPullRequest: os.Getenv("CI_MERGE_REQUEST_IID") != ""}, true
	case envTruthy("CIRCLECI"):
		return CIInfo{Provider: CIProviderCircleCI, IsPullRequest: os.Getenv("CIRCLE_PULL_REQUEST") != ""}, true
	case envTruthy("BUILDKITE"):
		return CIInfo{Provider: CIProviderBuildkite, IsPullRequest: isTruthyPR(os.Getenv("BUILDKITE_PULL_REQUEST"))}, true
	case envTruthy("JENKINS_URL"):
		return CIInfo{Provider: CIProviderJenkins, IsPullRequest: os.Getenv("CHANGE_ID") != ""}, true
	case envTruthy("TRAVIS"):
		return CIInfo{Provider: CIProviderTravis, IsPullRequest: isTruthyPR(os.Getenv("TRAVIS_PULL_REQUEST"))}, true
	case IsCI():
		return CIInfo{Provider: CIProviderUnknown}, true
	default:
		return CIInfo{}, false
	}
}

// isGitHubPREvent reports whether a GITHUB_EVENT_NAME value is one of
// the events GitHub Actions triggers for a pull request.
func isGitHubPREvent(event string) bool {
	return event == "pull_request" || event == "pull_request_target"
}

// isTruthyPR reports whether a provider's pull-request env var denotes
// an actual PR. Buildkite and Travis use "false" (or unset) to mean
// "not a PR".
func isTruthyPR(v string) bool {
	return v != "" && v != "false"
}

// resourceAttributes maps info to the resource attribute key/value pairs
// New attaches automatically. Returns nil for the zero value (no CI
// detected).
func (info CIInfo) resourceAttributes() []keyValue {
	if info.Provider == "" {
		return nil
	}
	return []keyValue{
		{ResourceAttrCIProviderName, info.Provider},
		{ResourceAttrCIPullRequest, strconv.FormatBool(info.IsPullRequest)},
	}
}
