// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeleteFileAction removes a single Go source file from the workspace.
//
// The deletion is staged through the standard transaction commit path — atomic,
// gated on `go mod tidy` and `go build` — so if any other file in the module
// still references symbols defined here, the build fails and the deletion rolls
// back automatically. The original bytes are snapshotted before deletion so the
// rollback restores the file exactly.
//
// Prefer this action over a raw `rm` from a shell:
//
//   - Workspace-aware: handles go.work expansion via [WorkspaceTransaction].
//   - Atomic: a build failure unwinds the deletion, leaving the workspace in
//     its prior state.
//   - Consistent: shares output semantics with every other lang.go.* tool, so
//     agents get the same FileResult / NextAction shape regardless of which
//     refactor ran.
//
// Only .go files are accepted; deletion of non-source files is out of scope and
// the action rejects them up-front.
type DeleteFileAction struct{}

// Name implements [RefactorStrategy] and returns [ActionDeleteFile].
func (*DeleteFileAction) Name() string { return ActionDeleteFile }

func init() { RegisterAction(&DeleteFileAction{}) }

// Execute is the [RefactorStrategy] entry point. It resolves input.File against
// the module root (if relative), validates the extension and existence, then
// calls ws.AddDelete to queue the removal for transaction commit.
//
// Failure modes:
//
//   - input.File is empty — returns an error before touching anything.
//   - The path does not end in .go — returns an error; this action is for Go
//     sources only.
//   - The file does not exist on disk — returns an error rather than silently
//     succeeding so callers learn about typos in the path.
//   - The build gate fails after deletion — ws.Commit rolls the file back from
//     its snapshot and surfaces the compiler diagnostic.
//
// The context argument is unused; the action's only IO is the eventual
// os.Remove invoked by Commit, which happens too quickly to benefit from
// cancellation.
func (*DeleteFileAction) Execute(_ context.Context, input Input, ws Transaction) error {
	if input.File == "" {
		return fmt.Errorf("file is required (path of .go file to delete)")
	}

	target := input.File
	if !filepath.IsAbs(target) {
		target = filepath.Join(ws.ModRoot(), target)
	}
	target = filepath.Clean(target)

	if !strings.HasSuffix(target, ".go") {
		return fmt.Errorf("file must be a .go source file: %s", target)
	}
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("file not found: %s", target)
	}

	return ws.AddDelete(target, fmt.Sprintf("deleted %s", target))
}
