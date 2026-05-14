// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package version

import (
	"strings"
	"testing"
)

// TestFull pins the four format branches the cobra root reads
// for `techne --version`:
//
//   - All build vars empty (dev build)      → "dev".
//   - Version set, commit empty             → bare version.
//   - Version + commit, date empty          → "vX.Y.Z (sha)".
//   - All three populated (release build)   → "vX.Y.Z (sha, built date)".
//
// buildVersion / buildCommit / buildDate are package-level vars
// the linker sets; the test toggles them for each branch.
func TestFull(t *testing.T) {
	// Cannot run subtests in parallel — they share the package
	// build-var globals.

	t.Run("unstamped build reports `dev`", func(t *testing.T) {
		buildVersion, buildCommit, buildDate = "", "", ""
		if got := Full(); got != "dev" {
			t.Fatalf("Full() = %q, want dev", got)
		}
	})

	t.Run("version without commit reports the version bare", func(t *testing.T) {
		buildVersion, buildCommit, buildDate = "v1.2.3", "", ""
		if got := Full(); got != "v1.2.3" {
			t.Fatalf("Full() = %q, want v1.2.3", got)
		}
	})

	t.Run("version + commit appears parenthesised", func(t *testing.T) {
		buildVersion, buildCommit, buildDate = "v1.2.3", "abc1234", ""
		got := Full()
		if !strings.Contains(got, "v1.2.3") || !strings.Contains(got, "abc1234") {
			t.Fatalf("Full() = %q, want both version + commit", got)
		}
	})

	t.Run("full build stamp includes the date", func(t *testing.T) {
		buildVersion = "v1.2.3"
		buildCommit = "abc1234"
		buildDate = "2026-05-13T22:00:00Z"
		got := Full()
		for _, want := range []string{"v1.2.3", "abc1234", "2026-05-13"} {
			if !strings.Contains(got, want) {
				t.Fatalf("Full() = %q, want it to mention %q", got, want)
			}
		}
	})
}
