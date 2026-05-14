// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package python

import "go.thesmos.sh/techne/internal/tool"

// Tools enumerates every tool exported by the lang.python package, in
// the order they appear under the lang.python group when the tool
// registry is introspected (e.g. via the MCP `list_tools` call or the
// `techne tools` CLI).
//
// The slice is consumed by the package init function below — extending
// it is the canonical way to add a new Python-language tool: declare
// the `tool.Tool` variable at file scope, then append it here. All
// entries are currently stubs registered via tool.Stub; the registry
// still surfaces them so that agents can plan around the eventual
// Python surface even before the implementations land.
var Tools = []tool.Tool{Explore, Verify, Deps}

// init registers the lang.python tool group with the global tool
// registry and installs a factory that returns [Tools] on demand.
//
// The factory closure accepts a config decoder so future Python tools
// can be parameterised (interpreter path, virtualenv selection, mypy
// strictness profile, etc.), but the current set of tools is config-free
// and ignores the decoder. Registration happens at package-import time,
// so simply importing `pkg/lang/python` from a binary's wiring layer
// is enough to make every Python tool stub reachable over MCP, CLI, and
// TUI.
func init() {
	tool.RegisterGroup(tool.Group{
		Path:        "lang.python",
		Description: "Python programming tools",
	})
	tool.RegisterTools("lang.python", func(decode func(any) error) []tool.Tool {
		return Tools
	})
}
