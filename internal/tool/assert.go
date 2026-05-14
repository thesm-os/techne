// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package tool

import (
	"strings"
	"testing"
)

// AssertTools is the contract test every domain package uses to
// validate its tool list. It enforces the cross-cutting invariants
// that the rest of the codebase relies on:
//
//   - Name is non-empty, so presenters can route to the tool.
//   - Description is non-empty, so the MCP descriptor and CLI
//     --help output are not blank.
//   - InputSchema is non-nil, so schema validation and CLI flag
//     derivation have something to work with.
//   - Name starts with groupPrefix when one is supplied, so that
//     tools live under the registered Group.Path they claim and
//     --tools prefix filtering in the application works.
//
// The assertion lives in the framework rather than as ad-hoc tests
// in each domain package so that the invariants are tested once,
// identically, everywhere. Adding a new check here automatically
// tightens the contract for every domain.
//
// Each failure is reported via t.Errorf, allowing a single test
// run to surface every offender; a missing Name is the only fatal
// condition because subsequent checks would produce noise without
// it. The function is a test helper (t.Helper) so failure file:line
// is reported at the caller, and it must be invoked from a *_test
// file.
func AssertTools(t *testing.T, groupPrefix string, tools []Tool) {
	t.Helper()
	for _, tl := range tools {
		name := tl.Name()
		if name == "" {
			t.Error("tool has empty Name()")
			continue
		}
		if tl.Description() == "" {
			t.Errorf("tool %q has empty Description()", name)
		}
		if tl.InputSchema() == nil {
			t.Errorf("tool %q has nil InputSchema()", name)
		}
		if groupPrefix != "" && !strings.HasPrefix(name, groupPrefix) {
			t.Errorf("tool %q: name does not start with group prefix %q", name, groupPrefix)
		}
	}
}
