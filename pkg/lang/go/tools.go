// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import "go.thesmos.sh/techne/internal/tool"

// Tools is the ordered list of every public Go-language tool that
// the `lang.go` group exposes — the union of read-only navigation
// tools ([Explore], [Search], [SearchExplore], [Callers],
// [Implementations], [References], [Invocations]), the verification
// and patch surface ([Verify], [GoPatch], [Fix]), and the
// refactoring toolbox ([Rename], [ChangeSignature], [ChangeType],
// [ImplementInterface], [ExtractFunction], [ExtractInterface],
// [ExtractVariable], [InlineConstant], [Document], [MoveFile],
// [MovePackage], [MoveSymbol], [MoveSymbols], [DeleteFile],
// [AddTests]).
//
// The slice is the single source of truth that the package's init
// function below registers with the tool framework. Adding a new tool
// means appending one entry here and writing the matching
// `var X = tool.New(...)` declaration in its own file — no further
// wiring is required; the MCP server, CLI command tree, and TUI
// command palette all pull from this registration.
var Tools = []tool.Tool{
	Workspace, Explore, Verify, Search, SearchExplore, GoPatch, Fix,
	Callers, Implementations, References, Invocations,
	Rename, ChangeSignature, ImplementInterface,
	ExtractFunction, ExtractInterface, ExtractVariable,
	InlineConstant, MovePackage, MoveSymbol, MoveSymbols, MoveFile, ChangeType, Document, DeleteFile, AddTests,
}

// init wires the `lang.go` group into the global tool registry. It
// registers the group descriptor (path + human-readable description)
// and a factory that returns [Tools] when the framework asks for the
// group's contents. The factory signature accepts an opaque config
// decoder; this group has no configuration, so the parameter is
// ignored. Registration runs at package import time, so simply
// importing this package is enough to make every lang.go.* tool
// available to every presenter.
func init() {
	tool.RegisterGroup(tool.Group{
		Path:        "lang.go",
		Description: "Go programming tools",
	})
	tool.RegisterTools("lang.go", func(decode func(any) error) []tool.Tool {
		return Tools
	})
}
