// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package lang

import "go.thesmos.sh/techne/internal/tool"

// init registers the top-level "lang" tool group with the framework's
// global tool registry on package import.
//
// This group is the parent namespace under which every language-specific
// family (lang.go.*, lang.py.*, lang.rs.*, lang.ts.*, lang.js.*) hangs.
// Because Go's init() runs exactly once per package load and the registry
// is populated as a side effect of import, callers (typically the CLI or
// MCP entrypoint) must transitively import this package — directly or
// through one of the language-specific sub-packages — before the lang.*
// tools become discoverable.
//
// Registration only declares the group's path and description; it does
// not bind any tools. Individual tool registrations live in each
// language-specific package and self-attach to this group by sharing the
// "lang.<language>.<tool>" path prefix.
func init() {
	tool.RegisterGroup(tool.Group{
		Path:        "lang",
		Description: "Language toolchain tools — symbol exploration, verification, dependency mapping",
	})
}
