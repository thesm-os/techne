// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import "go.thesmos.sh/techne/internal/tool"

// Tools is the complete, ordered registry of tool entry points published
// by the fs package. The host application iterates this slice to expose
// each tool through every presenter (MCP, CLI, TUI) without having to
// name them individually.
//
// Ordering is intentional and shapes how the tools appear in agent-
// facing catalogues and help output: read/write before listing, listing
// before searching, and destructive operations (move, copy, delete)
// last. The slice is exposed (not unexported) so out-of-tree callers
// that wire their own presenters can iterate the same canonical list.
//
// fs.patch is registered separately in patch.go's init and is therefore
// intentionally not present here — it is wired through the lang
// dispatcher rather than as a plain fs.* tool.
var Tools = []tool.Tool{
	Read,
	Write,
	List,
	Stat,
	Find,
	Grep,
	Replace,
	Move,
	Copy,
	Delete,
}

// init wires the fs package into the global tool registry. It calls
// [tool.RegisterTools] with the prefix "fs" so every tool returned by
// the callback is exposed under that namespace (fs.read, fs.list, ...).
// The registration callback receives a decode func that the host
// resolves against the active configuration source; it is invoked here
// to bind a [Config] value even though the current fs.* implementations
// do not yet consume the parsed limits at runtime — wiring the decode
// now keeps the contract stable as those limits are adopted.
func init() {
	tool.RegisterTools("fs", func(decode func(any) error) []tool.Tool {
		// Config can be decoded here if tools need runtime configuration.
		var _ Config
		_ = decode(new(Config))
		return Tools
	})
}
