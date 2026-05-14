// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"os"

	"go.thesmos.sh/techne/internal/tool"
)

// DeleteInput is the wire-format request for fs.delete. Path is
// required. The remaining flags (Recursive, Force) opt into the more
// forgiving behaviours; the zero value behaves like POSIX rmdir/unlink
// — strict, single-entry, error on absent target.
type DeleteInput struct {
	// Path is the absolute or workspace-relative path of the entry to
	// remove. The handler does not enforce a sandbox root; the host must
	// guard against escape paths ("../../..") if the agent's filesystem
	// access is meant to be confined.
	Path string `json:"path" jsonschema:"Path to the file or directory to delete."`
	// Recursive, when true, removes the entry and its entire descendant
	// tree (delegating to os.RemoveAll). When false (the default) a
	// directory with any contents fails with the underlying OS error, and
	// an empty directory or regular file is removed in one syscall via
	// os.Remove. Recursive must be true to delete a non-empty directory —
	// this is the single safety rail protecting against accidental
	// recursive deletes.
	Recursive bool `json:"recursive,omitempty" jsonschema:"Delete directories and their contents recursively. Optional, defaults to false."`
	// Force, when true, turns "path does not exist" into a successful
	// no-op so the call is idempotent. Useful when an agent is cleaning up
	// artifacts that may have already been removed by an earlier step.
	// Other errors (permission denied, directory not empty without
	// Recursive, etc.) still surface normally.
	Force bool `json:"force,omitempty" jsonschema:"Suppress errors for non-existent paths. Optional, defaults to false."`
}

// DeleteOutput is the wire-format response for fs.delete. Success
// reflects the final state — it is true when the entry no longer
// exists at the path, whether because the handler removed it or
// because Force absorbed an ENOENT.
type DeleteOutput struct {
	// Path echoes the deleted target so a caller chaining tool calls does
	// not need to retain the input.
	Path string `json:"path" jsonschema:"Path that was deleted."`
	// Success is true when the entry was either removed or never existed
	// under Force. A handler that returns an error always returns the zero
	// DeleteOutput, so a non-nil error and Success=false coincide.
	Success bool `json:"success" jsonschema:"Whether the deletion succeeded."`
}

// Delete is the fs.delete tool entry point. It removes a single file
// or directory from disk and is the destructive counterpart to fs.write
// and fs.move.
//
// The operation is not atomic and not reversible: once the syscall
// returns, the data is gone. Agents should prefer fs.patch for batch
// edits because patch supports rollback on verify failure, whereas
// fs.delete leaves no trail. There is no dry-run mode — callers that
// need to preview a deletion should fs.list or fs.stat first.
//
// The Recursive flag is mandatory for any non-empty directory; an
// attempt to delete a populated tree without it returns the OS's
// "directory not empty" error. The Force flag only suppresses the
// specific case of a missing target so repeated cleanup calls are
// idempotent; it does not paper over permission errors, busy files, or
// other failure modes.
var Delete = tool.New[DeleteInput, DeleteOutput](
	"fs.delete",
	"Deletes a file or directory. Use recursive=true for directories.",
	deleteHandler,
	tool.WithShortDescription("Delete a file or directory (recursive for non-empty dirs)"),
)

// deleteHandler implements fs.delete. It dispatches to os.RemoveAll
// when Recursive is set, otherwise to os.Remove. Both paths translate
// an os.ErrNotExist into success when Force is true, leaving all
// other errors (permission denied, EBUSY, ENOTEMPTY without
// Recursive) surfaced to the caller.
func deleteHandler(_ context.Context, input DeleteInput) (DeleteOutput, error) {
	if input.Path == "" {
		return DeleteOutput{}, fmt.Errorf("fs.delete: path is required")
	}

	var err error
	if input.Recursive {
		err = os.RemoveAll(input.Path)
	} else {
		err = os.Remove(input.Path)
	}

	if err != nil {
		if input.Force && os.IsNotExist(err) {
			return DeleteOutput{Path: input.Path, Success: true}, nil
		}
		return DeleteOutput{}, fmt.Errorf("fs.delete: %w", err)
	}

	return DeleteOutput{Path: input.Path, Success: true}, nil
}
