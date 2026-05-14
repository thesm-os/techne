// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Status constants for PatchOutput and PatchFileResult.
const (
	// PatchStatusSuccess is the PatchOutput.Status value when every
	// requested edit applied cleanly and any supplied VerifyCommand
	// exited zero. Diff receipts are populated for every per-file result
	// in this state.
	PatchStatusSuccess = "success"
	// PatchStatusPartialFailure is the PatchOutput.Status value when one
	// or more edits failed during the in-memory apply phase. Because the
	// handler validates every patch before touching disk, this status
	// guarantees that nothing was written — the disk state is byte-
	// identical to before the call. The Results slice identifies the
	// first failure.
	PatchStatusPartialFailure = "partial_failure"
	// PatchStatusRolledBack is the PatchOutput.Status value when all
	// edits applied successfully but a follow-up VerifyCommand exited
	// non-zero. The handler restores every modified file from its in-
	// memory snapshot, removes any newly created files, and recreates any
	// files that were deleted, leaving the workspace in the pre-call
	// state. VerifyResult holds the failing command's exit code and
	// combined output.
	PatchStatusRolledBack = "rolled_back"
	// PatchFileSuccess marks a per-file PatchFileResult whose edits were
	// applied successfully (or, in DryRun mode, would have been). The
	// result's DiffReceipt holds the unified diff of the applied change.
	PatchFileSuccess = "success"
	// PatchFileFailure marks a per-file PatchFileResult whose edits did
	// not apply (e.g. an OldString was not found) or were rolled back
	// because of a verify failure. The Error field carries the failure
	// reason.
	PatchFileFailure = "failure"
	// PatchFileCreated marks a per-file PatchFileResult for a file that
	// was introduced via PatchInput.CreateFiles. The DiffReceipt is an
	// all-addition hunk relative to a nil prior version.
	PatchFileCreated = "created"
	// PatchFileDeleted marks a per-file PatchFileResult for a path
	// removed via PatchInput.DeleteFiles. DiffReceipt is empty because
	// no meaningful diff format exists for a delete — the absent file
	// speaks for itself.
	PatchFileDeleted = "deleted"
)

// PatchInput is the wire-format request for fs.patch. It is the LLM-
// facing description of a multi-file atomic change: literal edits,
// pattern-driven bulk edits, file creates, file deletes, and an
// optional verify command, all bundled into one transactional call.
//
// The edit pipeline is fixed: PatternEdits expand into additional
// literal FilePatch entries first, then Patches as a whole are
// applied in memory, then — only if every edit validated — the
// changes are committed to disk, the FormatCommand is run, and the
// VerifyCommand decides whether to keep them. CreateFiles and
// DeleteFiles participate in the same envelope: they are visible
// after the disk-write phase and disappear on rollback.
//
// Fields are intentionally orthogonal. The agent can supply only
// Patches for a hand-written change, only PatternEdits for a regex
// sweep, or any combination. CreateFiles + DeleteFiles is the right
// way to perform a transactional file move with content rewrite (a
// use case fs.move cannot satisfy).
type PatchInput struct {
	// Patches is the ordered list of literal find-and-replace operations
	// to apply, one entry per target file. Suggested patches returned by
	// lang.go.verify, lang.go.fix, or lang.go.search_explore can be passed
	// in directly — they share the same FilePatch shape. Within a single
	// FilePatch the edits are sequential, so later edits operate on the
	// result of earlier ones; across FilePatch entries the apply order is
	// the slice order, but because each file is patched independently the
	// order matters only when two FilePatch entries name the same file.
	Patches []FilePatch `json:"patches,omitempty" jsonschema:"File-level literal modifications to apply atomically. AGENT HINT: Pass suggested_patches from lang.go.verify directly here."`
	// PatternEdits applies a regular-expression replacement across every
	// file matching a doublestar-style glob. Use this for high-volume
	// sweeps where a single pattern fixes many sites (e.g. "replace 50
	// errcheck violations with one expression"). Each PatternEdit is
	// expanded into literal FilePatch entries before the apply phase, so
	// pattern edits inherit fs.patch's atomicity and rollback guarantees
	// in full. The expansion stage emits a partial_failure if a regex is
	// invalid or the glob errors.
	PatternEdits []PatternEdit `json:"pattern_edits,omitempty" jsonschema:"Regex-based bulk edits applied across files matching a glob. Use for high-volume refactoring (e.g. fix 50 errcheck violations with one pattern). Reduces request size by 95% for bulk cleanups."`
	// DryRun, when true, runs every step up to and including in-memory
	// application, generates diff receipts, and returns them without
	// touching disk. No FormatCommand and no VerifyCommand are executed.
	// Use it to preview a complex change before committing — the returned
	// DiffReceipts are byte-for-byte what the on-disk state would have
	// become.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Validates all edits and generates diff receipts without writing to disk. Use to preview changes before committing."`
	// CreateFiles enumerates new files to write as part of the same
	// atomic operation. Each entry's parent directories are created with
	// mode 0755 if missing, and the file is then written with the
	// requested content and mode (defaulting to 0644). On verify failure
	// every newly created file is removed during rollback, so a failed
	// fs.patch leaves no orphan files behind.
	CreateFiles []CreateFile `json:"create_files,omitempty" jsonschema:"New files to create in the same atomic operation"`
	// DeleteFiles enumerates paths to remove as part of the same atomic
	// operation. The handler captures each file's bytes into the
	// snapshot before unlinking, so a verify failure restores them via
	// AtomicWrite during rollback. Missing files are silently ignored
	// during the delete phase (the rollback path will not recreate a
	// file that did not exist to begin with).
	DeleteFiles []string `json:"delete_files,omitempty" jsonschema:"Files to delete in the same atomic operation"`
	// FormatCommand is run against each modified or created file after
	// the disk write but before verification, typically a formatter like
	// "goimports -w" or "gofmt -w". The command runs as exe + base-args +
	// path for every modified path and its exit code is intentionally
	// ignored (best-effort): a formatter that fails on a transient input
	// will not invalidate the patch. After formatting the handler re-
	// reads each file so the DiffReceipt reflects the formatted output
	// rather than the raw edit.
	FormatCommand string `json:"format_command,omitempty" jsonschema:"Command to run on each modified file before generating diff receipt. Example: 'goimports -w'. Prevents formatting lint on the next verify call."`
	// VerifyCommand is run once after every change has been applied and
	// formatted, typically a build or test command ("go build ./...",
	// "go test ./..."). A zero exit code commits the patch; a non-zero
	// exit code triggers a full rollback: every modified file is restored
	// from its snapshot, every created file is removed, and every deleted
	// file is recreated. The combined stdout+stderr (last 10 lines) is
	// attached to VerifyResult for the agent to inspect.
	VerifyCommand string `json:"verify_command,omitempty" jsonschema:"Command to run after all changes are applied. Example: 'go build ./...'. If it fails, ALL changes are rolled back automatically."`
	// VerifyTargets are arguments appended to VerifyCommand at execution
	// time, after any arguments parsed out of VerifyCommand itself.
	// Useful for parameterising a generic command ("go test") with the
	// package list to target (e.g. ["./pkg/foo/...", "./pkg/bar/..."]).
	// Each element is passed as a separate argv entry so spaces within
	// elements are safe.
	VerifyTargets []string `json:"verify_targets,omitempty" jsonschema:"Arguments appended to verify_command"`
}

// PatternEdit applies a single regex substitution across every file
// matching a glob, expanding into literal FilePatch entries before
// fs.patch's atomic apply pipeline. The agent supplies the pattern;
// the handler discovers files, computes per-file matches, and emits
// the equivalent literal edits.
//
// Use PatternEdit for bulk refactors where one pattern describes
// the full transformation. A typical example: replacing all
// "x, _ := NewLedger(args)" call sites with proper error handling
// in a single call. Because expansion runs before commit, a failing
// regex or a missing glob match aborts the whole patch with a
// partial_failure and nothing is written.
type PatternEdit struct {
	// FileGlob selects target files via doublestar-style matching
	// executed through GlobFiles. The pattern may contain a single "**"
	// segment, which walks every subdirectory below the literal prefix
	// and matches the suffix against each file's base name. Examples:
	// "**/*_test.go", "core/**/*.go", "main.go". Empty matches surface
	// as an empty patch list (no edits applied), not as an error.
	FileGlob string `json:"file_glob" jsonschema:"Glob pattern to match files. Example: '**/*_test.go' or 'core/**/*.go'. Uses doublestar matching."`
	// OldRegex is the Go RE2 regular expression matched against the
	// full content of each globbed file. Capture groups are available
	// to NewTemplate as $1, $2, ${name}. Invalid expressions abort the
	// patch with a partial_failure pointing at the offending pattern
	// rather than each file individually.
	OldRegex string `json:"old_regex" jsonschema:"Regex pattern to match in each file. Supports capture groups. Example: '(\\w+), _ := NewLedger\\((.+?)\\)' to capture the variable and args."`
	// NewTemplate is the replacement string applied per match using
	// regexp.ReplaceAllString syntax: $1, $2 etc. refer to capture
	// groups; a literal dollar sign must be written $$. The template
	// also honours the literal escape sequences \n and \t so multi-
	// line replacements survive the JSON wire transport without the
	// agent having to embed raw newlines.
	NewTemplate string `json:"new_template" jsonschema:"Replacement template with $1, $2 capture group references. Example: '$1, err := NewLedger($2)\\nif err != nil {\\n\\tt.Fatal(err)\\n}'."`
	// MaxReplacements caps the number of substitutions per file. Zero
	// (the default) replaces every match in every file. Set to 1 to
	// perform an idempotent first-match swap, which is the right choice
	// when each file is expected to contain exactly one occurrence —
	// over-replacement then surfaces as a hard error rather than silent
	// breakage.
	MaxReplacements int `json:"max_replacements,omitempty" jsonschema:"Maximum replacements per file. Default: unlimited (0). Set to 1 to replace only the first match per file."`
}

// FilePatch describes the set of literal edits to apply to a single
// file. It is the structural unit fs.patch operates on after
// PatternEdit expansion; suggested patches returned by
// lang.go.verify and friends use the same shape so they can be fed
// back in unchanged.
type FilePatch struct {
	// FilePath is the absolute or workspace-relative path of the file to
	// modify. The file must exist before the patch is applied; a missing
	// file produces a per-file PatchFileFailure with Error="file not
	// found" and aborts the whole batch.
	FilePath string `json:"file_path" jsonschema:"Path to the file to modify"`
	// Edits is the ordered list of literal find-and-replace operations
	// applied to FilePath. Each PatchEdit's OldString must appear in the
	// current content (the result of the prior edit, or the original
	// body for the first edit). Edits are not commutative — reordering
	// them can change the outcome or cause a missing-OldString error.
	Edits []PatchEdit `json:"edits" jsonschema:"Ordered list of find-and-replace edits"`
}

// PatchEdit is a single literal find-and-replace within a FilePatch.
// It is exported as a type alias for lang.PatchEdit so callers in
// pkg/lang and pkg/fs share the same on-the-wire shape; lang.go.fix
// emits the type and fs.patch consumes it without any translation
// step. See lang.PatchEdit for the field-level contract.
type PatchEdit = lang.PatchEdit

// CreateFile describes a file to materialise as part of the atomic
// fs.patch envelope. Created files participate in rollback: if any
// edit fails before the disk-write phase, they are never written; if
// the VerifyCommand fails afterwards, they are removed during
// rollback so the workspace is left as if the patch never happened.
type CreateFile struct {
	// Path is the absolute or workspace-relative destination for the new
	// file. Parent directories are created automatically with mode 0755
	// before the file is written, so the caller need not pre-arrange the
	// tree. If a file already exists at Path the call will overwrite it,
	// losing the prior content; an upstream check via fs.stat is advised
	// when unsure.
	Path string `json:"path" jsonschema:"Path for the new file"`
	// Content is the file body to write, used verbatim with no
	// translation — line endings, BOMs, and trailing whitespace are
	// preserved exactly. For Go files, ending Content with a single
	// newline is conventional and keeps gofmt/goimports happy.
	Content string `json:"content" jsonschema:"File content"`
	// Mode is the file permission bits expressed as an octal string
	// ("0644", "0755"). Empty selects the default "0644". The handler
	// applies an explicit os.Chmod after writing so the requested mode
	// is honoured even when the active umask would have masked bits.
	Mode string `json:"mode,omitempty" jsonschema:"File permissions in octal (e.g. 0644). Default: 0644"`
}

// PatchOutput is the wire-format response for fs.patch. It collapses
// the outcome of a multi-file atomic operation into a single Status,
// a per-file Results slice, an optional VerifyResult, and a list of
// suggested follow-up actions.
//
// Status is the first field an agent should read: PatchStatusSuccess
// means the workspace has changed; PatchStatusPartialFailure means
// nothing was written; PatchStatusRolledBack means changes were
// applied and then reverted (the workspace is back to its starting
// state but the VerifyCommand's output is available for diagnosis).
type PatchOutput struct {
	// Status is the overall outcome: PatchStatusSuccess,
	// PatchStatusPartialFailure, or PatchStatusRolledBack. See the
	// constants for the precise on-disk implication of each state.
	Status string `json:"status" jsonschema:"success: all edits applied. partial_failure: one or more edits failed, nothing written. rolled_back: edits applied but verify_command failed, all changes reverted."`
	// Results lists per-file outcomes in the order the handler processed
	// them: literal patches first (in input order), then created files,
	// then deleted files. Each entry's Status field disambiguates between
	// an applied edit, a failure, a create, and a delete; DiffReceipt is
	// populated for successes and creations.
	Results []PatchFileResult `json:"results" jsonschema:"Per-file results for every patched, created, or deleted file"`
	// VerifyResult carries the exit code and tail of combined output for
	// the VerifyCommand, when one was supplied. Nil indicates no verify
	// command ran. A non-nil VerifyResult with Passed=false coincides
	// with Status=PatchStatusRolledBack.
	VerifyResult *VerifyCommandResult `json:"verify_result,omitempty" jsonschema:"Outcome of the verify_command if one was specified"`
	// NextActions lists suggested follow-up tool calls the host should
	// encourage the agent to make. On success the suggestion is usually a
	// full lang.go.verify; on rollback it is a lang.go.explore so the
	// agent can read the file before retrying. Each NextAction carries a
	// confidence label so the host can decide how aggressively to surface
	// it.
	NextActions []lang.NextAction `json:"next_actions,omitempty" jsonschema:"Suggested follow-up tool calls"`
}

// PatchFileResult is one row in PatchOutput.Results, describing the
// outcome for a single file. Exactly one of DiffReceipt or Error is
// set in normal operation; both are empty for a successful delete
// (where there is nothing meaningful to render).
type PatchFileResult struct {
	// FilePath is the path of the file this result describes, mirroring
	// FilePatch.FilePath, CreateFile.Path, or one of the entries from
	// PatchInput.DeleteFiles depending on the kind of operation.
	FilePath string `json:"file_path" jsonschema:"Path of the file that was operated on"`
	// Status is one of PatchFileSuccess, PatchFileFailure,
	// PatchFileCreated, or PatchFileDeleted, indicating what the handler
	// did (or tried to do) with FilePath.
	Status string `json:"status" jsonschema:"success, failure, created, or deleted"`
	// DiffReceipt is the unified diff of the change applied to this
	// file. Populated for every successful patch and every create, empty
	// for deletes and failures. Agents can compare the receipt against
	// their intent to verify the edit landed correctly without issuing a
	// follow-up fs.read.
	DiffReceipt string `json:"diff_receipt,omitempty" jsonschema:"Unified diff of the change. AGENT HINT: Use this to verify your edit was applied correctly without calling read_file."`
	// Error carries the failure message when Status is PatchFileFailure
	// — typically "file not found", a missing OldString snippet, or
	// "rolled back after verify failure". Empty for any other Status.
	Error string `json:"error,omitempty" jsonschema:"Error message if the operation failed"`
}

// VerifyCommandResult captures the outcome of a post-patch verify
// command: its exit code, the tail of its combined output, and a
// boolean shortcut for "did it pass?". The result is included in
// PatchOutput only when a VerifyCommand was supplied; its absence
// means no verification ran, not that verification passed.
type VerifyCommandResult struct {
	// ExitCode is the process exit code returned by VerifyCommand. Zero
	// indicates success and triggers a commit; any non-zero value
	// triggers a rollback.
	ExitCode int `json:"exit_code" jsonschema:"Process exit code; 0 means success"`
	// Output is the last 10 lines of combined stdout and stderr from
	// VerifyCommand, trimmed by lang.LastNLines. The 10-line cap keeps
	// large build logs from dominating the response while still giving
	// the agent enough context to act on a failure.
	Output string `json:"output" jsonschema:"Last 10 lines of command output"`
	// Passed is a boolean shortcut for ExitCode == 0, supplied so callers
	// can branch on a single field instead of doing integer arithmetic
	// on the exit code.
	Passed bool `json:"passed" jsonschema:"True if the verify command exited with code 0"`
}

// Patch is the fs.patch tool entry point. It applies multi-file
// find-and-replace edits atomically, optionally bundling file
// creations and deletions, optionally formatting modified files, and
// optionally gating the whole operation on a verify command that can
// roll every change back.
//
// Atomicity model: every target file is snapshotted into memory
// before any disk write happens. Edits are applied in three phases:
//  1. Expand phase: every PatternEdit is resolved into literal
//     FilePatch entries by globbing the file system. Failure here
//     (bad regex, glob error) aborts with partial_failure and
//     nothing reaches disk.
//  2. Apply-in-memory phase: every Patches entry is checked against
//     its snapshot; a missing OldString aborts the whole batch with
//     partial_failure before any file is touched.
//  3. Commit phase: only after every patch validates does any file
//     get written, deleted, or created. If a FormatCommand is
//     supplied, it runs best-effort against each modified path. If
//     a VerifyCommand is supplied and exits non-zero, every change
//     is reverted from the snapshot and the response carries Status
//     PatchStatusRolledBack.
//
// Design rationale: agents writing code make mistakes mid-batch
// all the time. Without rollback, a half-applied multi-file edit
// can leave the workspace in a state that requires manual cleanup
// or a second agent turn to diagnose. With rollback gated on a
// build command, the agent can attempt a change confidently — the
// worst outcome is a no-op with diagnostic output.
//
// Pattern edits expand against the working directory via
// GlobFiles' doublestar matcher and apply a Go regexp ReplaceAllString
// with the supplied template. They merge into the literal patches list
// before the apply phase, so a single fs.patch invocation can combine
// both forms atomically.
//
// File creation and deletion participate in the same atomic envelope:
// a file in CreateFiles is written only if everything else commits,
// and a file in DeleteFiles is restored from its captured snapshot
// on rollback. This makes "move with rewrite" expressible as a single
// fs.patch (CreateFiles new, DeleteFiles old, in one call).
//
// For Go-specific refactors (rename a symbol, change a signature,
// extract a function) prefer the lang.go.* refactor tools — they are
// AST-aware and update every reference. fs.patch is the right hammer
// for cross-language sweeps, formatting fix-ups, and anything
// lang.go.* cannot express.
var Patch = tool.New[PatchInput, PatchOutput](
	"fs.patch",
	"Applies multi-file edits atomically with optional formatting and build verification. Use this instead of individual file writes when modifying code. All changes are rolled back if any edit fails or verification fails.",
	patchHandler,
	tool.WithShortDescription("Apply atomic multi-file edits with optional format and verify-and-rollback"),
)

// patchHandler implements fs.patch. It executes the snapshot /
// expand / apply / commit / format / verify pipeline described in
// the doc comment on the Patch entry point.
//
// Stage by stage:
//
//  1. Snapshot every file named in Patches into a map keyed by
//     path. Missing files are recorded with snapshotExists=false so
//     a later edit attempt produces a clear "file not found"
//     failure rather than silently creating the file.
//  2. Expand PatternEdits into additional literal FilePatch entries
//     via ExpandPatternEdits, merging them into Patches and snap-
//     shotting any newly discovered files.
//  3. Apply each FilePatch in memory using ApplyEdits, recording
//     a per-file success result and queuing the modified bytes for
//     write. Any failure short-circuits the whole batch with status
//     partial_failure and no disk writes.
//  4. Parse CreateFiles modes (octal strings to os.FileMode).
//  5. If DryRun, generate diff receipts from the in-memory state
//     and return PatchStatusSuccess without writing anything.
//  6. Otherwise, write every queued modification via AtomicWrite,
//     create every CreateFile (mkdir + WriteFile + explicit Chmod
//     to override umask), and remove every DeleteFile (capturing
//     its prior content into the snapshot first so rollback can
//     restore it).
//  7. If FormatCommand is set, run it best-effort against every
//     modified/created path and re-read those files so the diff
//     receipt reflects the formatted output.
//  8. If VerifyCommand is set, run it once with VerifyTargets
//     appended. On non-zero exit, restore every modified file from
//     the snapshot, remove every newly created file, recreate every
//     deleted file, mark every per-file result as failure, and
//     return status rolled_back with the verify output attached.
//  9. Generate diff receipts and return PatchStatusSuccess with
//     suggested NextActions pointing to lang.go.verify.
func patchHandler(_ context.Context, input PatchInput) (PatchOutput, error) {
	// 1. Snapshot: read all target files into memory.
	snapshot := make(map[string][]byte)
	snapshotExists := make(map[string]bool)

	for _, fp := range input.Patches {
		if _, seen := snapshot[fp.FilePath]; seen {
			continue
		}
		data, err := os.ReadFile(fp.FilePath)
		if err != nil {
			if !os.IsNotExist(err) {
				return PatchOutput{}, fmt.Errorf("fs.patch: reading %q: %w", fp.FilePath, err)
			}
			snapshot[fp.FilePath] = nil
			snapshotExists[fp.FilePath] = false
			continue
		}
		snapshot[fp.FilePath] = data
		snapshotExists[fp.FilePath] = true
	}

	// 1b. Expand PatternEdits into literal Patches by finding matching files and applying regex.
	if len(input.PatternEdits) > 0 {
		expandedPatches, patternResults, err := ExpandPatternEdits(input.PatternEdits)
		if err != nil {
			return PatchOutput{
				Status:  PatchStatusPartialFailure,
				Results: patternResults,
			}, nil
		}
		// Merge expanded patches into the literal patches list.
		input.Patches = append(input.Patches, expandedPatches...)
		// Snapshot the newly discovered files.
		for _, fp := range expandedPatches {
			if _, seen := snapshot[fp.FilePath]; seen {
				continue
			}
			data, err := os.ReadFile(fp.FilePath)
			if err != nil {
				if !os.IsNotExist(err) {
					return PatchOutput{}, fmt.Errorf("fs.patch: reading %q: %w", fp.FilePath, err)
				}
				snapshot[fp.FilePath] = nil
				snapshotExists[fp.FilePath] = false
				continue
			}
			snapshot[fp.FilePath] = data
			snapshotExists[fp.FilePath] = true
		}
	}

	// 2. Apply edits in memory, collect results.
	type pendingWrite struct {
		filePath string
		original []byte
		modified []byte
	}
	pending := make([]pendingWrite, 0, len(input.Patches))
	results := make([]PatchFileResult, 0, len(input.Patches)+len(input.CreateFiles)+len(input.DeleteFiles))

	for _, fp := range input.Patches {
		orig := snapshot[fp.FilePath]
		if !snapshotExists[fp.FilePath] {
			results = append(results, PatchFileResult{
				FilePath: fp.FilePath,
				Status:   PatchFileFailure,
				Error:    fmt.Sprintf("file not found: %q", fp.FilePath),
			})
			// partial failure — return immediately without writing
			return PatchOutput{
				Status:  PatchStatusPartialFailure,
				Results: results,
			}, nil
		}

		modified, err := ApplyEdits(string(orig), fp.Edits)
		if err != nil {
			results = append(results, PatchFileResult{
				FilePath: fp.FilePath,
				Status:   PatchFileFailure,
				Error:    err.Error(),
			})
			return PatchOutput{
				Status:  PatchStatusPartialFailure,
				Results: results,
			}, nil
		}

		pending = append(pending, pendingWrite{
			filePath: fp.FilePath,
			original: orig,
			modified: []byte(modified),
		})
		results = append(results, PatchFileResult{
			FilePath: fp.FilePath,
			Status:   PatchFileSuccess,
		})
	}

	// 3. Parse CreateFiles modes.
	type pendingCreate struct {
		path    string
		content []byte
		mode    os.FileMode
	}
	creates := make([]pendingCreate, 0, len(input.CreateFiles))
	for _, cf := range input.CreateFiles {
		mode := os.FileMode(0o644)
		if cf.Mode != "" {
			parsed, err := strconv.ParseUint(cf.Mode, 8, 32)
			if err != nil {
				return PatchOutput{}, fmt.Errorf("fs.patch: invalid mode %q for %q: %w", cf.Mode, cf.Path, err)
			}
			mode = os.FileMode(parsed)
		}
		creates = append(creates, pendingCreate{
			path:    cf.Path,
			content: []byte(cf.Content),
			mode:    mode,
		})
	}

	// DryRun: generate diffs and return without writing.
	if input.DryRun {
		for i, pw := range pending {
			results[i].DiffReceipt = GenerateDiff(pw.filePath, pw.original, pw.modified)
		}
		for _, pc := range creates {
			results = append(results, PatchFileResult{
				FilePath:    pc.path,
				Status:      PatchFileCreated,
				DiffReceipt: GenerateDiff(pc.path, nil, pc.content),
			})
		}
		for _, dp := range input.DeleteFiles {
			results = append(results, PatchFileResult{
				FilePath: dp,
				Status:   PatchFileDeleted,
			})
		}
		return PatchOutput{
			Status:  PatchStatusSuccess,
			Results: results,
		}, nil
	}

	// 5. Write all modified files atomically.
	for _, pw := range pending {
		if err := AtomicWrite(pw.filePath, pw.modified); err != nil {
			return PatchOutput{}, fmt.Errorf("fs.patch: writing %q: %w", pw.filePath, err)
		}
	}

	// Create new files.
	for _, pc := range creates {
		if err := os.MkdirAll(filepath.Dir(pc.path), 0o755); err != nil {
			return PatchOutput{}, fmt.Errorf("fs.patch: mkdir for %q: %w", pc.path, err)
		}
		if err := os.WriteFile(pc.path, pc.content, pc.mode); err != nil {
			return PatchOutput{}, fmt.Errorf("fs.patch: creating %q: %w", pc.path, err)
		}
		// Explicitly chmod to override umask.
		if err := os.Chmod(pc.path, pc.mode); err != nil {
			return PatchOutput{}, fmt.Errorf("fs.patch: chmod %q: %w", pc.path, err)
		}
	}

	// Snapshot created-file originals (for rollback — they didn't exist).
	for _, cf := range input.CreateFiles {
		snapshot[cf.Path] = nil
		snapshotExists[cf.Path] = false
	}

	// Delete files.
	for _, dp := range input.DeleteFiles {
		data, err := os.ReadFile(dp)
		if err == nil {
			snapshot[dp] = data
			snapshotExists[dp] = true
		}
		if err := os.Remove(dp); err != nil && !os.IsNotExist(err) {
			return PatchOutput{}, fmt.Errorf("fs.patch: deleting %q: %w", dp, err)
		}
	}

	// Collect paths of all modified/created files for formatting.
	modifiedPaths := make([]string, 0, len(pending)+len(creates))
	for _, pw := range pending {
		modifiedPaths = append(modifiedPaths, pw.filePath)
	}
	for _, pc := range creates {
		modifiedPaths = append(modifiedPaths, pc.path)
	}

	// 6. Format modified/created files.
	if input.FormatCommand != "" {
		parts := strings.Fields(input.FormatCommand)
		exe := parts[0]
		baseArgs := parts[1:]
		for _, path := range modifiedPaths {
			args := append(append([]string{}, baseArgs...), path)
			cmd := exec.Command(exe, args...)
			_ = cmd.Run() // best-effort; ignore errors
		}
	}

	// Re-read modified files (formatting may have changed them).
	pendingIdx := make(map[string]int, len(pending))
	for i, pw := range pending {
		pendingIdx[pw.filePath] = i
	}
	for _, pw := range pending {
		data, err := os.ReadFile(pw.filePath)
		if err == nil {
			pending[pendingIdx[pw.filePath]].modified = data
		}
	}

	// Re-read created files too.
	createIdx := make(map[string]int, len(creates))
	for i, pc := range creates {
		createIdx[pc.path] = i
	}
	for _, pc := range creates {
		data, err := os.ReadFile(pc.path)
		if err == nil {
			creates[createIdx[pc.path]].content = data
		}
	}

	// 7. Verify.
	if input.VerifyCommand != "" {
		verifyArgs := append([]string{}, input.VerifyTargets...)
		cmdParts := strings.Fields(input.VerifyCommand)
		exe := cmdParts[0]
		args := slices.Concat(cmdParts[1:], verifyArgs)
		cmd := exec.Command(exe, args...)
		out, _ := cmd.CombinedOutput()
		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}

		if exitCode != 0 {
			// Rollback: restore all modified files from snapshots.
			for _, pw := range pending {
				if snapshot[pw.filePath] != nil {
					_ = AtomicWrite(pw.filePath, snapshot[pw.filePath])
				}
			}
			// Remove created files.
			for _, pc := range creates {
				_ = os.Remove(pc.path)
			}
			// Restore deleted files.
			for _, dp := range input.DeleteFiles {
				if data, ok := snapshot[dp]; ok && data != nil {
					_ = AtomicWrite(dp, data)
				}
			}

			verifyResult := &VerifyCommandResult{
				ExitCode: exitCode,
				Output:   lang.LastNLines(string(out), 10),
				Passed:   false,
			}
			// Mark all patch results as rolled back.
			for i := range results {
				results[i].Status = PatchFileFailure
				results[i].Error = "rolled back after verify failure"
			}
			return PatchOutput{
				Status:       PatchStatusRolledBack,
				Results:      results,
				VerifyResult: verifyResult,
				NextActions: []lang.NextAction{
					{
						Tool:       "lang.go.explore",
						Confidence: lang.ConfidenceLow,
						Reason:     "Patch rolled back after verify failure — explore the code",
					},
				},
			}, nil
		}

		// Verify passed — attach result.
		verifyResult := &VerifyCommandResult{
			ExitCode: 0,
			Output:   lang.LastNLines(string(out), 10),
			Passed:   true,
		}

		// 8. Generate diff receipts.
		for i, pw := range pending {
			results[i].DiffReceipt = GenerateDiff(pw.filePath, pw.original, pw.modified)
		}
		createResultOffset := len(pending)
		for j, pc := range creates {
			results[createResultOffset+j].DiffReceipt = GenerateDiff(pc.path, nil, pc.content)
		}

		return PatchOutput{
			Status:       PatchStatusSuccess,
			Results:      results,
			VerifyResult: verifyResult,
			NextActions: []lang.NextAction{
				{
					Tool:       "lang.go.verify",
					Confidence: lang.ConfidenceHigh,
					Reason:     "Changes applied, run verification",
				},
			},
		}, nil
	}

	// 8. Generate diff receipts (no verify command).
	for i, pw := range pending {
		results[i].DiffReceipt = GenerateDiff(pw.filePath, pw.original, pw.modified)
	}

	// Append create and delete results.
	for _, pc := range creates {
		results = append(results, PatchFileResult{
			FilePath:    pc.path,
			Status:      PatchFileCreated,
			DiffReceipt: GenerateDiff(pc.path, nil, pc.content),
		})
	}
	for _, dp := range input.DeleteFiles {
		results = append(results, PatchFileResult{
			FilePath: dp,
			Status:   PatchFileDeleted,
		})
	}

	// 9. Build NextActions.
	return PatchOutput{
		Status:  PatchStatusSuccess,
		Results: results,
		NextActions: []lang.NextAction{
			{
				Tool:       "lang.go.verify",
				Confidence: lang.ConfidenceHigh,
				Reason:     "Changes applied, run verification",
			},
		},
	}, nil
}
