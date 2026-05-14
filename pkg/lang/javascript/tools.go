// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package javascript

import "go.thesmos.sh/techne/internal/tool"

// Tools enumerates every tool exported by the lang.javascript package,
// in the order they appear under the lang.javascript group when the
// tool registry is introspected (e.g. via the MCP `list_tools` call or
// the `techne tools` CLI).
//
// The slice is consumed by the package init function below. The set is
// deliberately a copy of the lang.typescript surface — plain JavaScript
// is treated as a special case of TypeScript by the eventual
// implementation (a .js file is parsed as TS with `allowJs: true`),
// but the public tool names remain distinct so agents can target either
// language explicitly. All entries are currently stubs.
var Tools = []tool.Tool{Explore, Verify, Deps}

// init registers the lang.javascript tool group with the global tool
// registry and installs a factory that returns [Tools] on demand.
//
// The factory closure accepts a config decoder so future JavaScript
// tools can be parameterised (Node binary, package manager, ESLint
// config path), but the current stub set ignores the decoder.
// Registration happens at package-import time, so simply importing
// `pkg/lang/javascript` from a binary's wiring layer is enough to
// make every JavaScript tool stub reachable over MCP, CLI, and TUI.
func init() {
	tool.RegisterGroup(tool.Group{
		Path:        "lang.javascript",
		Description: "JavaScript programming tools",
	})
	tool.RegisterTools("lang.javascript", func(decode func(any) error) []tool.Tool {
		return Tools
	})
}
