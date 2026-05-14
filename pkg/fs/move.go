// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"os"

	"go.thesmos.sh/techne/internal/tool"
)

// MoveInput is the wire-format request for fs.move. Src and Dst are
// both required. The handler invokes a single os.Rename, so cross-
// filesystem moves fail with EXDEV — those callers must fall back to
// fs.copy followed by fs.delete.
//
// For Go source files, prefer lang.go.move_file instead: it rewrites
// the package clause, updates importers, and runs through the build
// gate. fs.move is byte-level and would leave imports of the old path
// dangling.
type MoveInput struct {
	// Src is the absolute or workspace-relative path of the entry to
	// move. It may name a regular file, a directory, or a symlink. The
	// source must exist; the call fails with os.ErrNotExist otherwise.
	Src string `json:"src" jsonschema:"Source path of the file or directory to move."`
	// Dst is the destination path. The parent directory must already
	// exist — fs.move does not create missing ancestors; that is the
	// caller's responsibility (e.g. via fs.write CreateDirs on a sibling).
	// If Dst already exists the call fails unless Force is set.
	Dst string `json:"dst" jsonschema:"Destination path."`
	// Force, when true, removes whatever currently lives at Dst (file or
	// directory tree) before the rename, so the move acts as a clobbering
	// upsert. When false (the default) the underlying os.Rename surfaces
	// the OS's "destination exists" error and the source is left in
	// place. Force does not bypass cross-filesystem rename restrictions.
	Force bool `json:"force,omitempty" jsonschema:"Overwrite the destination if it already exists. Optional, defaults to false."`
}

// MoveOutput is the wire-format response for fs.move. Success is
// strictly redundant with a nil error — the handler returns a zero
// MoveOutput on every failure path — but is included so callers can
// use a single boolean check when post-processing batched results.
type MoveOutput struct {
	// Src echoes the input source path so callers chaining tool calls do
	// not need to retain the input.
	Src string `json:"src" jsonschema:"Source path."`
	// Dst echoes the destination path the entry now resides at.
	Dst string `json:"dst" jsonschema:"Destination path."`
	// Success reports whether the rename completed. False with a non-nil
	// handler error means nothing changed on disk; true with a nil error
	// means Src has been moved to Dst (and any prior occupant of Dst was
	// removed when Force was set).
	Success bool `json:"success" jsonschema:"Whether the move succeeded."`
}

// Move is the fs.move tool entry point. It performs a single
// os.Rename, either moving a file or directory to a new location or
// renaming it in place when Src and Dst share a parent directory.
//
// Limitations to know about: the call cannot cross file system
// boundaries (os.Rename returns EXDEV when Src and Dst live on
// different mounts) and is not atomic in the multi-step sense — if
// Force removes an existing Dst and the subsequent rename fails, the
// prior Dst data is already gone. For Go source files specifically,
// the right tool is lang.go.move_file: it updates the package clause
// and every importer, which fs.move cannot do.
//
// Use Force sparingly; it is destructive. Prefer fs.delete + fs.move
// in two explicit steps when the agent's intent is clearly to replace
// a file, so the audit trail is clearer.
var Move = tool.New[MoveInput, MoveOutput](
	"fs.move",
	"Moves or renames a file or directory.",
	moveHandler,
)

// moveHandler implements fs.move. When Force is set it first removes
// the destination via os.RemoveAll (treating ENOENT as benign) and
// then runs os.Rename. Any rename failure is wrapped and returned;
// on success a populated MoveOutput is returned.
func moveHandler(_ context.Context, input MoveInput) (MoveOutput, error) {
	if input.Src == "" {
		return MoveOutput{}, fmt.Errorf("fs.move: src is required")
	}
	if input.Dst == "" {
		return MoveOutput{}, fmt.Errorf("fs.move: dst is required")
	}

	if input.Force {
		if err := os.RemoveAll(input.Dst); err != nil && !os.IsNotExist(err) {
			return MoveOutput{}, fmt.Errorf("fs.move: removing destination %q: %w", input.Dst, err)
		}
	}

	if err := os.Rename(input.Src, input.Dst); err != nil {
		return MoveOutput{}, fmt.Errorf("fs.move: %w", err)
	}

	return MoveOutput{Src: input.Src, Dst: input.Dst, Success: true}, nil
}
