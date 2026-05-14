// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package rust

import "go.thesmos.sh/techne/internal/tool"

// Tools enumerates every tool exported by the lang.rust package, in the
// order they appear under the lang.rust group when the tool registry is
// introspected (e.g. via the MCP `list_tools` call or the `techne tools`
// CLI).
//
// The slice is consumed by the package `init` function below — extending
// it is the canonical way to add a new Rust-language tool: declare the
// `tool.Tool` variable at file scope, then append it here. The registry
// clones the slice on registration, so callers may mutate the returned
// value without polluting the registry.
var Tools = []tool.Tool{Explore, Verify, Deps}

// init registers the lang.rust tool group with the global tool registry
// and installs a factory that returns [Tools] on demand.
//
// The factory closure accepts a config decoder so that future Rust tools
// can be parameterised (e.g. a custom cargo path via [Config]'s CargoPath
// field), but the current set of tools is config-free and ignores the
// decoder. Registration happens at package-import time, so simply
// importing `pkg/lang/rust` from a binary's wiring layer is enough to
// make every Rust tool reachable over MCP, CLI, and TUI.
func init() {
	tool.RegisterGroup(tool.Group{
		Path:        "lang.rust",
		Description: "Rust programming tools",
	})
	tool.RegisterTools("lang.rust", func(decode func(any) error) []tool.Tool {
		return Tools
	})
}
