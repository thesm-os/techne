// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package typescript

import "go.thesmos.sh/techne/internal/tool"

// Tools enumerates every tool exported by the lang.typescript package,
// in the order they appear under the lang.typescript group when the
// tool registry is introspected (e.g. via the MCP `list_tools` call or
// the `techne tools` CLI).
//
// The slice is consumed by the package init function below. lang.typescript
// is intended to be the canonical implementation that the lang.javascript
// tools delegate to once the backend lands — the TypeScript compiler
// API parses .js with `allowJs: true`, so a single Node-subprocess
// backend can power both languages. All entries are currently stubs.
var Tools = []tool.Tool{Explore, Verify, Deps}

// init registers the lang.typescript tool group with the global tool
// registry and installs a factory that returns [Tools] on demand.
//
// The factory closure accepts a config decoder so future TypeScript
// tools can be parameterised (Node binary, tsconfig.json path, package
// manager), but the current stub set ignores the decoder.
// Registration happens at package-import time, so simply importing
// `pkg/lang/typescript` from a binary's wiring layer is enough to
// make every TypeScript tool stub reachable over MCP, CLI, and TUI.
func init() {
	tool.RegisterGroup(tool.Group{
		Path:        "lang.typescript",
		Description: "TypeScript programming tools",
	})
	tool.RegisterTools("lang.typescript", func(decode func(any) error) []tool.Tool {
		return Tools
	})
}
