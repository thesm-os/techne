// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"

	"go.thesmos.sh/techne/pkg/fs"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// WorkspaceTransaction is the production implementation of [Transaction]. It
// accumulates staged edits in memory, then on Commit writes them atomically and
// gates them on a successful `go build ./...` (expanded per-module under
// go.work) — rolling every file back to its original snapshot if the build
// fails.
//
// Why this exists in one place, not per-action:
//
//   - Every refactor in this package wants the same guarantee: "either all the
//     files I changed land on disk and the module still builds, or nothing
//     changes." Centralizing the snapshot/commit/rollback logic means an action
//     only has to describe edits in terms of (filePath, original, modified)
//     tuples; the dangerous bits (atomic write, goimports, build verification,
//     rollback) live here and are exercised by every test.
//   - go.work support is non-trivial: in workspace mode `go build ./...` from
//     the workspace root matches nothing, so we have to expand one pattern per
//     use-module. Having that logic live on the transaction means every action
//     gets workspace-correct builds for free.
//
// Field layout:
//
//   - snapshots: original bytes keyed by absolute path. Populated on the first
//     AddChange/AddFileMove/AddDelete for each file; rollback reads from here.
//   - modified: staged new content, also keyed by absolute path. A second
//     AddChange for the same path overwrites the staged content.
//   - deletions: absolute paths queued for `os.Remove` at commit time.
//   - results / failed: per-file FileResult entries surfaced in the Output.
//   - notes: dedupe-on-insert advisory strings for the user.
//
// DryRun short-circuits Commit before any disk writes, so callers can preview a
// refactor without modifying the workspace. The struct is single-writer;
// callers (i.e., action.Execute methods) run sequentially against one
// transaction.
type WorkspaceTransaction struct {
	modRoot   string
	dryRun    bool
	snapshots map[string][]byte // original content keyed by absolute path
	modified  map[string][]byte // staged new content keyed by absolute path
	deletions map[string]bool   // files to delete (used by move_package)
	results   []FileResult
	failed    int
	notes     []string // advisory messages surfaced in Output.Notes
}

// AddNote appends message to the staged notes list, dropping the call when an
// identical message is already present so a refactor visiting N files (e.g., a
// cross-module move touching multiple importers) surfaces the advisory exactly
// once to the user.
//
// Notes are surfaced verbatim in Output.Notes and are reserved for side effects
// the transaction cannot perform itself — most commonly, asking the user to run
// `go mod tidy` after a refactor crosses a go.mod boundary.
func (ws *WorkspaceTransaction) AddNote(message string) {
	if slices.Contains(ws.notes, message) {
		return
	}
	ws.notes = append(ws.notes, message)
}

// AddSkipped records a [StatusSkipped] FileResult for filePath without staging
// any edit. Used when an action determines mid-flight that a file is already in
// the desired state — for example, implement_interface called on a struct that
// already satisfies the target interface, or document --list-missing reporting
// per-file results with no edits to make.
//
// Does not contribute to the failure count and does not trigger the build gate
// (the file was not touched). Surfaces in Output.Results so an agent can tell
// "intentionally untouched" from "missed by the tool."
func (ws *WorkspaceTransaction) AddSkipped(filePath, message string) {
	ws.results = append(ws.results, FileResult{
		FilePath: filePath,
		Status:   StatusSkipped,
		Message:  message,
	})
}

// NewTransaction constructs a WorkspaceTransaction rooted at the module
// containing pkg. The pkg argument is resolved with [ResolveWorkDir] and the
// module root with [ResolveModuleRoot], so any of: an import path
// ("core/ledger"), a relative directory ("./pkg/fs"), an absolute path, or the
// empty string (current directory) work.
//
// When dryRun is true, Commit will build the output structure but skip all disk
// writes and the build gate — useful for previewing a refactor without
// modifying the workspace.
//
// The returned transaction has empty snapshot/modified/deletion maps; it is
// single-writer and not safe for concurrent use across goroutines.
func NewTransaction(pkg string, dryRun bool) *WorkspaceTransaction {
	workDir, _ := ResolveWorkDir(pkg)
	modRoot := ResolveModuleRoot(workDir)
	return &WorkspaceTransaction{
		modRoot:   modRoot,
		dryRun:    dryRun,
		snapshots: make(map[string][]byte),
		modified:  make(map[string][]byte),
		deletions: make(map[string]bool),
	}
}

// ModRoot returns the directory containing the go.mod that was resolved at
// construction time. Actions use this to anchor module-relative file paths
// supplied by the caller (e.g., input.File = "pkg/foo/bar.go") and as the
// working directory for shelling out to the Go toolchain.
func (ws *WorkspaceTransaction) ModRoot() string {
	return ws.modRoot
}

// LoadPackages discovers the workspace via [workspace.Discover] (which
// understands go.work) and loads every package with the full set of mode flags
// an action needs: syntax, type info, compiled files, imports, and transitive
// deps. Tests are included via WithTests so the loaded set covers `_test.go`
// files in the same call.
//
// For any non-trivial refactor (rename, change_signature, move_*), this is the
// single most expensive operation in the pipeline — typically dwarfing the
// actual edit logic. Callers should invoke it at most once per Execute and pass
// the returned slice around explicitly.
func (ws *WorkspaceTransaction) LoadPackages(ctx context.Context) ([]*packages.Package, error) {
	w, err := workspace.Discover(ws.modRoot)
	if err != nil {
		return nil, fmt.Errorf("discover workspace: %w", err)
	}
	mode := packages.NeedSyntax | packages.NeedName | packages.NeedFiles |
		packages.NeedCompiledGoFiles | packages.NeedTypes | packages.NeedTypesInfo |
		packages.NeedImports | packages.NeedDeps
	pkgs, err := w.Load(ctx, mode, nil, workspace.WithTests())
	if err != nil {
		return nil, err
	}
	// Surface build-tag coverage gaps: any .go files excluded by the host's
	// active build tags are listed as advisory notes so the caller knows the
	// refactor only covered the current-tag set. See [detectExcludedFiles].
	for _, note := range detectExcludedFiles(pkgs) {
		ws.AddNote(note)
	}
	return pkgs, nil
}

// AddChange stages a file edit, taking responsibility for snapshotting the
// original bytes on first sight, validating that modified is parseable Go, and
// running goimports in-memory before storing the new content.
//
// When ValidateGoSource rejects the bytes (empty or syntactically invalid), the
// failure is recorded in the per-file FileResult and the failure counter is
// incremented; an error is also returned so the calling action can bail out
// immediately rather than continue staging edits that won't survive Commit.
//
// goimports is best-effort: if it fails (e.g., ambiguous import paths in a
// partially loaded workspace), AddChange falls back to the raw modified bytes.
// The build gate at Commit time will catch any compile errors the unformatted
// version produces.
func (ws *WorkspaceTransaction) AddChange(filePath string, original, modified []byte, message string) error {
	// Snapshot the original on first encounter.
	if _, seen := ws.snapshots[filePath]; !seen {
		ws.snapshots[filePath] = original
	}

	if err := ValidateGoSource(filePath, modified); err != nil {
		ws.failed++
		ws.results = append(ws.results, FileResult{
			FilePath: filePath,
			Status:   StatusFailure,
			Error:    err.Error(),
		})
		return fmt.Errorf("validation failed for %s: %w", filePath, err)
	}

	formatted, err := imports.Process(filePath, modified, nil)
	if err != nil {
		// goimports failure is non-fatal — use the raw content.
		formatted = modified
	}

	ws.modified[filePath] = formatted
	ws.results = append(ws.results, FileResult{
		FilePath:    filePath,
		Status:      StatusSuccess,
		DiffSnippet: fs.GenerateDiff(filePath, original, formatted),
		Message:     message,
	})
	return nil
}

// AddFileMove stages a rename: oldPath is snapshotted from disk and queued for
// deletion, while newPath is added through [WorkspaceTransaction.AddChange]
// with newContent. The two operations commit together — on rollback, the
// deletion is undone (oldPath restored from snapshot) and the new file is
// removed.
//
// Used by move_file and move_package. nil passed as the original argument to
// AddChange is intentional: the destination did not exist before the move, so
// there is no original to snapshot for it, and rollback simply deletes it if
// commit fails.
func (ws *WorkspaceTransaction) AddFileMove(oldPath, newPath string, newContent []byte, message string) error {
	oldContent, _ := os.ReadFile(oldPath)
	if _, seen := ws.snapshots[oldPath]; !seen {
		ws.snapshots[oldPath] = oldContent
	}
	ws.deletions[oldPath] = true

	return ws.AddChange(newPath, nil, newContent, message)
}

// AddDelete reads filePath, snapshots its original bytes so rollback can
// restore it, and queues it for `os.Remove` at commit time. Returns an error if
// the file is unreadable — without a snapshot the rollback path can't put it
// back, so it's better to fail loudly than risk an unrecoverable delete.
//
// The build gate still runs after deletions are applied: if other code in the
// module references symbols defined here, the build fails and the rollback path
// recreates the deleted file from its snapshot.
func (ws *WorkspaceTransaction) AddDelete(filePath, message string) error {
	original, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}
	if _, seen := ws.snapshots[filePath]; !seen {
		ws.snapshots[filePath] = original
	}
	ws.deletions[filePath] = true
	ws.results = append(ws.results, FileResult{
		FilePath:    filePath,
		Status:      StatusSuccess,
		Message:     message,
		DiffSnippet: fs.GenerateDiff(filePath, original, nil),
	})
	return nil
}

// Commit applies every staged change to disk under an atomic, all-or-nothing
// semantics gated by a successful Go build, then assembles the [Output]
// returned to the caller.
//
// Commit flow (non-dry-run):
//
//  1. Apply deletions via os.Remove.
//  2. Write every staged modification via fs.AtomicWrite (write-to-temp +
//     rename).
//  3. Snapshot go.mod and go.sum so they too can be restored.
//  4. Run `go mod tidy` (best-effort — failure is non-fatal).
//  5. Run `go build` over the workspace's modules; in go.work mode each module
//     is expanded into its own `./<mod>/...` pattern so the build covers the
//     whole workspace.
//  6. On build failure: rollback every written file to its snapshot, restore
//     deleted files, restore go.mod/go.sum, and return [StatusFailure] with
//     the first compiler diagnostic.
//  7. On success: best-effort prune empty directories left by deletions,
//     return [StatusSuccess] with per-file diffs and a high-confidence
//     NextAction pointing at lang.go.verify.
//
// Three fast paths:
//   - DryRun: returns the staged Output without touching disk.
//   - No staged changes: skips the build gate and returns Status pass; useful
//     for read-only modes like document --list_missing.
//   - All non-Go work (notes-only, e.g.): same.
//
// The rollback is best-effort — `_ = os.Remove(...)` and `_ =
// fs.AtomicWrite(...)`. In practice the snapshots written before the build gate
// ran are the same bytes the user had before the call, so restore failures are
// vanishingly rare; the build gate failure message takes precedence over any
// cleanup hiccup.
func (ws *WorkspaceTransaction) Commit(ctx context.Context) (Output, error) {
	if ws.dryRun {
		return ws.buildOutput(), nil
	}

	// Nothing staged (e.g. list_missing read-only pass) — skip build gate entirely.
	if len(ws.modified) == 0 && len(ws.deletions) == 0 {
		out := ws.buildOutput()
		out.BuildStatus = "pass"
		return out, nil
	}

	var written []string

	// 1. Process deletions (e.g. from move_package).
	for filePath := range ws.deletions {
		_ = os.Remove(filePath)
	}

	// 2. Write staged modifications atomically.
	for filePath, content := range ws.modified {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			ws.rollback(written)
			return Output{Status: StatusFailure, BuildStatus: "fail"}, fmt.Errorf("mkdir failed: %w", err)
		}
		if err := fs.AtomicWrite(filePath, content); err != nil {
			ws.rollback(written)
			return Output{
					Status:      StatusFailure,
					BuildStatus: "fail",
				}, fmt.Errorf(
					"atomic write failed for %s: %w",
					filePath,
					err,
				)
		}
		written = append(written, filePath)
	}

	// 3. Snapshot go.mod / go.sum before tidy.
	modFile := filepath.Join(ws.modRoot, "go.mod")
	sumFile := filepath.Join(ws.modRoot, "go.sum")
	modOriginal, _ := os.ReadFile(modFile)
	sumOriginal, _ := os.ReadFile(sumFile)

	// 4. Run go mod tidy (best-effort — don't abort on failure).
	tidyCmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	tidyCmd.Dir = ws.modRoot
	_ = tidyCmd.Run()

	// 5. Verify the module builds.
	if err := ws.buildModule(ctx); err != nil {
		// Rollback all writes.
		ws.rollback(written)

		// Restore go.mod / go.sum.
		if len(modOriginal) > 0 {
			_ = os.WriteFile(modFile, modOriginal, 0o644)
		}
		if len(sumOriginal) > 0 {
			_ = os.WriteFile(sumFile, sumOriginal, 0o644)
		}

		return Output{Status: StatusFailure, BuildStatus: "fail"},
			fmt.Errorf("module build failed after refactor (rolled back): %w", err)
	}

	// Cleanup empty directories left by deletions.
	for filePath := range ws.deletions {
		_ = os.Remove(filepath.Dir(filePath)) // Fails safely if non-empty.
	}

	// Build output — build already passed so skip re-running it.
	out := ws.buildOutput()
	out.BuildStatus = "pass"
	return out, nil
}

// rollback undoes written files and restores deleted files.
func (ws *WorkspaceTransaction) rollback(written []string) {
	for _, wf := range written {
		snap := ws.snapshots[wf]
		if len(snap) == 0 {
			_ = os.Remove(wf)
		} else {
			_ = fs.AtomicWrite(wf, snap)
		}
	}
	// Restore files that were deleted as part of a move.
	for path, content := range ws.snapshots {
		if ws.deletions[path] && len(content) > 0 {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = fs.AtomicWrite(path, content)
		}
	}
}

func (ws *WorkspaceTransaction) buildModule(ctx context.Context) error {
	if ws.modRoot == "" {
		return nil
	}
	// In go.work mode, "./..." from a workspace root does not match anything
	// — we have to spell out one pattern per use module. Discover the
	// workspace and expand accordingly.
	w, err := workspace.Discover(ws.modRoot)
	if err != nil {
		// Fall back to the module-only build if workspace discovery fails;
		// this preserves single-module behavior.
		cmd := exec.CommandContext(ctx, "go", "build", "./...")
		cmd.Dir = ws.modRoot
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("%s", parseFirstBuildError(out))
		}
		return nil
	}

	args := []string{"build"}
	if w.IsGoWork() {
		for _, m := range w.Modules() {
			rel, relErr := filepath.Rel(w.Root(), m.Dir)
			if relErr != nil || rel == "." {
				args = append(args, "./...")
				continue
			}
			args = append(args, "./"+filepath.ToSlash(rel)+"/...")
		}
	} else {
		args = append(args, "./...")
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = w.Root()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", parseFirstBuildError(out))
	}
	return nil
}

// goBuildErrorPattern matches a single Go compiler diagnostic line of the
// form "./file.go:42:5: message" or "/abs/path/file.go:42: message".
var goBuildErrorPattern = regexp.MustCompile(`^\S.*\.go:\d+(?::\d+)?:\s+\S.*$`)

// parseFirstBuildError extracts the first compiler-style diagnostic from
// `go build` output. Falls back to the first non-empty non-`# package`
// line, then to the trimmed full output. The narrowed message is what
// surfaces to users when a refactor's build gate fails — much more
// useful than a multi-screen dump of `go build`'s stderr, which they
// can reproduce by hand if needed.
func parseFirstBuildError(out []byte) string {
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if goBuildErrorPattern.MatchString(trimmed) {
			return trimmed
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return trimmed
	}
	return strings.TrimSpace(string(out))
}

func (ws *WorkspaceTransaction) buildOutput() Output {
	filesModified := len(ws.modified) + len(ws.deletions)
	buildStatus := "pass"

	var nextActions []lang.NextAction
	if filesModified > 0 {
		nextActions = append(nextActions, lang.NextAction{
			Tool:       "lang.go.verify",
			Confidence: lang.ConfidenceHigh,
			Reason:     "Refactoring complete — run full verification",
			Input:      lang.VerifyInput{Targets: []string{"./..."}},
		})
	}

	// Count per-file successes vs failures from the results slice.
	successCount := 0
	failCount := ws.failed
	for _, r := range ws.results {
		if r.Status == StatusSuccess {
			successCount++
		}
	}

	return Output{
		Status:        StatusSuccess,
		FilesModified: successCount,
		FilesFailed:   failCount,
		BuildStatus:   buildStatus,
		Results:       ws.results,
		NextActions:   nextActions,
		Notes:         ws.notes,
	}
}
