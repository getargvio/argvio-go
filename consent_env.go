package argvio

import (
	"context"
	"os"
)

// ciEnvVars are environment variables commonly set to a truthy value by
// CI systems. Presence of any of these (with a non-empty, non-"false"
// value) is treated as "running in CI".
var ciEnvVars = []string{
	"CI",
	"CONTINUOUS_INTEGRATION",
	"GITHUB_ACTIONS",
	"GITLAB_CI",
	"CIRCLECI",
	"TRAVIS",
	"JENKINS_URL",
	"BUILDKITE",
	"TEAMCITY_VERSION",
	"APPVEYOR",
	"TF_BUILD", // Azure Pipelines
	"DRONE",    // Drone CI
	"CODEBUILD_BUILD_ID",
	"BITBUCKET_BUILD_NUMBER",
}

// IsCI reports whether the current process appears to be running inside
// a CI system, based on common CI-provider environment variables.
func IsCI() bool {
	for _, name := range ciEnvVars {
		if v := os.Getenv(name); v != "" && v != "false" && v != "0" {
			return true
		}
	}
	return false
}

// IsDoNotTrackRequested reports whether the DO_NOT_TRACK environment
// variable (https://do-not-track.dev) requests telemetry be disabled.
// Any value other than empty, "0", or "false" is treated as a request.
func IsDoNotTrackRequested() bool {
	v, ok := os.LookupEnv("DO_NOT_TRACK")
	if !ok {
		return false
	}
	return v != "" && v != "0" && v != "false"
}

// EnvConsentProvider is a ConsentProvider that caps the resolved tier at
// TierAnonymous when the process appears to be running in a CI
// environment. It resolves (TierAnonymous, nil) in that case and errors
// otherwise, so it is meant to be placed ahead of other providers in a
// ChainProviders call — it "wins" only when CI is detected, deferring to
// the next provider otherwise.
//
// Reasoning: CI runs are usually unattended, share no single human
// user's stored consent preference, and can run at high volume (every
// commit, every matrix leg). We still allow anonymous-tier telemetry
// (e.g. install/build counts) by default rather than fully disabling,
// because that data has real product value and carries no identifying
// fields at TierAnonymous by construction. Vendors who want CI runs
// fully silent can use WithDisabled or check IsCI() themselves.
//
// DO_NOT_TRACK is handled separately (see IsDoNotTrackRequested) and is
// enforced by Client as a full disable, not a tier cap: it is a
// standing, cross-tool opt-out signal a user or environment sets
// deliberately, so we honor it as "no telemetry" rather than "least
// telemetry".
type EnvConsentProvider struct{}

// Resolve implements ConsentProvider.
func (EnvConsentProvider) Resolve(ctx context.Context) (Tier, error) {
	if IsCI() {
		return TierAnonymous, nil
	}
	return TierAnonymous, errNoProviderResolved
}
