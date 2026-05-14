// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package version exposes the build metadata ldflags inject at
// link time. The cobra root reads [Full] to populate `techne
// --version`; the release pipeline (goreleaser) sets the three
// build-time variables via `-X go.thesmos.sh/techne/internal/version...`.
package version

import "fmt"

// buildVersion is the semver tag the binary was built from
// (`v1.2.3`). Empty when built outside the release pipeline; in
// that case [Full] reports `dev`.
var buildVersion string

// buildCommit is the short git SHA the binary was built from.
// Empty when built outside the release pipeline.
var buildCommit string

// buildDate is the RFC3339 timestamp the binary was built at.
// Empty when built outside the release pipeline.
var buildDate string

// Full returns the human-facing version string techne prints for
// `--version`. Falls back to `dev` for unstamped local builds so
// the output stays informative.
func Full() string {
	if buildVersion == "" {
		return "dev"
	}
	if buildCommit == "" {
		return buildVersion
	}
	if buildDate == "" {
		return fmt.Sprintf("%s (%s)", buildVersion, buildCommit)
	}
	return fmt.Sprintf("%s (%s, built %s)", buildVersion, buildCommit, buildDate)
}
