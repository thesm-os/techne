// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/tools/imports"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/fs"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// Status constants for GoPatchResult.
const (
	// GoPatchSuccess marks a per-file patch result whose edit was applied,
	// formatted, and verified by the post-patch build gate. The file on disk
	// reflects the modification and a unified diff is available in
	// GoPatchResult.DiffReceipt.
	GoPatchSuccess = "success"
	// GoPatchFailure marks a per-file patch result whose edit was rejected
	// or rolled back. The file on disk is unchanged — either the edit
	// never made it past the in-memory validation gate, the build failed
	// after writing and the original was restored, or an unrecoverable I/O
	// error occurred. GoPatchResult.Error and GoPatchResult.Forensics carry
	// the diagnostic detail.
	GoPatchFailure = "failure"
)

// GoPatchInput is the input schema for lang.go.patch.
//
// The patch tool always runs goimports in-memory before writing and
// gates every change behind a `go vet` plus `go build` over the
// entire module. Multi-patch submissions (>1 entry in Patches)
// automatically run as an atomic batch: all writes commit only if the
// combined module still builds, otherwise every modified file is
// restored to its pre-patch content. A single patch runs in per-file
// mode, where one failure only rolls back that one file.
//
// The agent does not configure the transaction model — submitting more
// than one patch is the signal for atomic-batch dispatch. This keeps
// the tool surface small while still giving callers transactional
// semantics for refactors that span multiple files.
type GoPatchInput struct {
	// Patches lists the file-level text edits to apply. Each entry pairs a
	// file path with a sequence of (OldString, NewString) substitutions
	// that are applied in order via fs.ApplyEdits.
	//
	// Submitting more than one patch runs the batch atomically — all writes
	// commit only if the combined module still builds, otherwise all files
	// are rolled back to their pre-patch content. Submitting a single patch
	// runs in per-file mode where one file's failure cannot affect the
	// others.
	//
	// For structural changes (rename, change signature, extract, move,
	// change type), prefer the lang.go.* refactor tools — they understand
	// references and will update call sites that a text-patch will miss.
	Patches []fs.FilePatch `json:"patches" jsonschema:"File-level text edits. Submitting >1 patch automatically runs them as an atomic batch — all writes commit only if the combined module still builds, otherwise all are rolled back. Submitting 1 patch runs in per-file mode. AGENT HINT: For structural changes (rename, signature, extract, move), prefer the lang.go.* refactor tools."`
	// DryRun previews the edits by formatting and verifying them in a temp
	// file without writing to the target on disk. Returns the would-be
	// DiffReceipt so the agent can preview what would change.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview edits without writing to disk. The build gate runs against a temp-file swap of the post-change content (atomically restored on exit), so build_status:pass means applying for real is guaranteed to compile."`
	// Force bypasses the pre-patch build gate, which is intended for
	// recovering from a broken workspace after a failed refactor. The
	// post-patch build gate still applies, so a Force=true patch that
	// leaves the module unbuildable still triggers rollback. Set this only
	// when the workspace is known to be broken and a patch is being used
	// to repair it.
	Force bool `json:"force,omitempty" jsonschema:"Bypass the pre-patch build gate. Use ONLY to recover from a broken state after a failed refactor. The post-patch build gate still applies."`
	// AutoVerify runs lint verification after a successful patch. The
	// results are attached to GoPatchOutput.VerifyOutput and
	// VerificationStatus but never trigger rollback — this is diagnostic
	// only. Use to get a quick "is anything visibly broken?" signal
	// without a separate verify call.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint verification after successful patch. Diagnostic only — patches are NOT rolled back on failure."`
	// VerifySuites picks which verification suites run when AutoVerify is
	// true. Defaults to ["lint"] because the auto-verify is bounded to a
	// 10-second budget (autoVerifyTimeout) and tests typically exceed
	// that. Valid values are the lang.Suite* constants.
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Suites to run when auto_verify is true. Default: ['lint']. Options: lint, test, bench, fuzz."`
}

// GoPatchOutput is the result of a lang.go.patch operation. The output
// is structured so that an agent can pick the smallest field set for
// its purpose: Summary for "did everything apply", Results for
// per-file diagnostics, VerifyOutput plus VerificationStatus when
// AutoVerify was requested, and NextActions for the recommended
// follow-up call.
type GoPatchOutput struct {
	// Summary holds aggregate counts of applied, failed, and total patches
	// so the agent can determine overall success without iterating Results.
	Summary GoPatchSummary `json:"summary" jsonschema:"Aggregate counts: applied, failed, and total patches processed."`
	// Results lists per-file outcomes in request order. Successes carry a
	// DiffReceipt showing exactly what was written (including goimports
	// formatting); failures carry compiler output, forensics, and an
	// actionable Hint when one can be inferred.
	Results []GoPatchResult `json:"results" jsonschema:"Per-file results in request order. Successes have diff receipts; failures have compiler errors and forensics."`
	// VerifyOutput carries the structured verification report when
	// AutoVerify was requested, regardless of pass or fail. Patches are
	// never rolled back based on this report — it is diagnostic only.
	// Absent when AutoVerify was not requested or no patches applied.
	VerifyOutput *lang.VerifyOutput `json:"verify_output,omitempty" jsonschema:"Verification results when auto_verify was used. Present regardless of pass/fail — patches are NOT rolled back."`
	// VerificationStatus summarises the auto-verify outcome. Possible
	// values are the Verification* constants: TestOK (tests passed on
	// the affected packages), LintOK (lint passed; logic untested),
	// Unverified (no auto-verify performed), Degraded (verification
	// found issues), or Timeout (verification exceeded its 10s budget).
	// Note that the status is scoped to the affected packages, not the
	// entire module — only an explicit lang.go.verify with ./... gives
	// global confidence.
	VerificationStatus string `json:"verification_status" jsonschema:"'test_ok': tests passed on affected packages (scoped, not full module). 'lint_ok': lint passed on affected packages — logic untested. 'unverified': no verification performed. 'degraded': verification found issues. 'timeout': verification timed out — may indicate a build hang."`
	// NextActions lists suggested follow-up tool calls with confidence
	// labels. Deterministic actions (formatter patches) can be executed
	// without review; check confidence before acting on medium/low
	// entries. Typical content includes a lang.go.verify call when all
	// patches applied, or a lang.go.explore call when one or more
	// failed.
	NextActions []lang.NextAction `json:"next_actions,omitempty" jsonschema:"Suggested follow-up tool calls. AGENT HINT: Execute deterministic actions without review; check confidence before acting on medium/low."`
}

// GoPatchSummary holds aggregate counts for a patch run. Applied +
// Failed always equals Total; failures include both pre-write rejects
// (parse errors, unbuildable starting state) and post-write rollbacks
// (build-gate failure).
type GoPatchSummary struct {
	// Applied counts patches that passed parse validation, formatting,
	// and the build gate, and were committed to disk.
	Applied int `json:"applied" jsonschema:"Number of patches that passed formatting and build verification and were committed to disk."`
	// Failed counts patches that did not commit, whether rejected before
	// writing (parse error, broken pre-state) or rolled back after writing
	// (build gate failed).
	Failed int `json:"failed" jsonschema:"Number of patches that failed post-patch verification and were rolled back to original content."`
	// Total is the number of patches processed (Applied + Failed).
	Total int `json:"total" jsonschema:"Total number of patches processed (applied + failed)."`
}

// GoPatchResult is the per-file result of a patch operation. The shape
// is the same for success and failure; consumers branch on Status to
// decide which optional fields are meaningful.
type GoPatchResult struct {
	// PatchIndex is the 0-based position of this patch in the input
	// Patches array. Useful when correlating with batched input that
	// generated multiple patches.
	PatchIndex int `json:"patch_index" jsonschema:"0-based index of this patch in the request array."`
	// FilePath is the absolute or module-relative path of the file that
	// was patched. Mirrors the input Patches entry's FilePath verbatim.
	FilePath string `json:"file_path" jsonschema:"Absolute or module-relative path of the file that was patched."`
	// Status is GoPatchSuccess when the edit was applied, formatted, and
	// verified by the build gate, or GoPatchFailure when it was rolled
	// back or never written. Consumers must check this before reading
	// DiffReceipt or Error — only one is populated per result.
	Status string `json:"status" jsonschema:"'success': edit applied and build verified. 'failure': edit rolled back — file is unchanged."`
	// DiffReceipt is the unified diff of the applied change, including any
	// goimports formatting performed during the write. Lets the agent
	// verify the actual change without a follow-up read_file. Absent on
	// failure.
	DiffReceipt string `json:"diff_receipt,omitempty" jsonschema:"Unified diff of the applied change including goimports formatting. AGENT HINT: Use to verify the edit was correct without calling read_file."`
	// Error is the compiler-output or edit-failure message when Status is
	// GoPatchFailure. For atomic-batch failures, the message on the first
	// result is the canonical compiler output; subsequent results carry a
	// shorter "rolled back (batch failure)" message to avoid duplication.
	// Check Forensics.Hint for an actionable fix suggestion.
	Error string `json:"error,omitempty" jsonschema:"Compiler error or edit failure message. AGENT HINT: Check Forensics.Hint for an actionable fix suggestion."`
	// Forensics carries structured diagnostic detail for failed patches—
	// the last few lines of compiler output and an actionable Hint when one
	// can be inferred. Absent for successful patches and for pre-write
	// failures that have no compiler output yet.
	Forensics *PatchForensics `json:"forensics,omitempty" jsonschema:"Structured diagnostic details for failed patches including compiler output and a remediation hint."`
}

// PatchForensics provides diagnostic detail for a failed patch.
// Populated only when a patch reached the post-patch build gate or
// the in-memory parse check and failed there.
type PatchForensics struct {
	// CompilerOutput holds the final lines of raw output from `go vet`
	// and `go build`. Capped at ~10 lines to avoid flooding the agent's
	// context on cascading errors; the full output is available in
	// logDir/lint.log when verify ran the lint suite.
	CompilerOutput string `json:"compiler_output,omitempty" jsonschema:"Last 10 lines of raw compiler error output from go vet + go build."`
	// Hint is an actionable suggestion derived from the compiler output
	// by inferHint. Use to adjust the edit strategy before retrying —
	// common hints include "variable already exists, use = instead of
	// :=" and "unused import after edit, check goimports".
	Hint string `json:"hint,omitempty" jsonschema:"Actionable suggestion for resolving the failure. AGENT HINT: Use this to adjust your edit strategy before retrying."`
}

// GoPatch is the lang.go.patch tool. It applies file-level text edits
// with goimports formatting, in-memory parse validation, and a `go
// vet` plus `go build` build gate; on failure, edited files are
// rolled back atomically.
//
// Prefer this over Edit for any Go change that needs a build guarantee
// or affects multiple files. Edit is faster for trivial single-line
// tweaks (typos, comment fixes, log messages) where the cost of a
// build verification round-trip dominates the cost of being wrong.
// Use lang.go.patch when correctness matters — a corrupted source
// file in a multi-file refactor will cost more than the verification
// would have.
//
// For structural changes (rename, change signature, extract function,
// move symbol, change type), use the lang.go.* refactor tools instead.
// They produce patches as a side-effect but also rewrite all references
// in the workspace, which lang.go.patch alone does not do.
//
// Multi-patch submissions automatically run as an atomic batch: all
// writes commit only if the combined module still builds, otherwise
// every file is restored. Single patches run in per-file mode where one
// failure only rolls back that file. The agent does not configure
// this — submitting more than one patch is the signal.
//
// For cross-package safety, edits to files in the same package are
// serialized via a kernel flock on the package directory.
var GoPatch = tool.New[GoPatchInput, GoPatchOutput](
	"lang.go.patch",
	"PREFER OVER Edit for any Go change that needs a build-gate or affects multiple files. Each patch is goimports-formatted, parse-checked before write, and go-build verified after; on failure, files roll back atomically. Multi-patch submissions auto-run as one transaction. Edit is faster for trivial single-line tweaks (typos, comments, log messages); use lang.go.patch when correctness matters or when a refactor produced patches you need to apply. For structural changes (rename, signature, type), use the refactor tools instead — they update references project-wide.",
	goPatchHandler,
	tool.WithShortDescription("Apply Go patches with goimports, parse check, and build-gated rollback"),
)

// goPatchHandler implements the lang.go.patch RPC. Dispatches on the
// number of submitted patches: >1 runs processAtomicBatch (one
// module-wide build gate, all-or-nothing rollback), single-patch runs
// through processPatch with its own per-file transaction.
//
// The handler builds NextActions before returning: a lang.go.verify
// follow-up at high confidence when any patches applied, or a
// lang.go.explore at low confidence when patches failed. The
// NextAction.Input.Targets is populated with the unique parent
// directories of patched files so a re-verify is scoped to only the
// affected packages, not the whole module.
func goPatchHandler(ctx context.Context, input GoPatchInput) (GoPatchOutput, error) {
	// Multi-patch submissions automatically use atomic batch mode: all writes
	// commit only if the combined module still builds. Single patches run
	// in per-file mode where one failure only rolls back that file.
	if len(input.Patches) > 1 {
		return processAtomicBatch(ctx, input)
	}

	// ── Per-file transaction mode (single-patch case) ─────────────
	results := make([]GoPatchResult, 0, len(input.Patches))
	applied := 0
	failed := 0

	for i, patch := range input.Patches {
		result := processPatch(ctx, i, patch, input.DryRun, input.Force)
		results = append(results, result)
		if result.Status == GoPatchSuccess {
			applied++
		} else {
			failed++
		}
	}

	// Build NextActions.
	var nextActions []lang.NextAction
	if failed > 0 {
		nextActions = append(nextActions, lang.NextAction{
			Tool:       "lang.go.explore",
			Confidence: lang.ConfidenceLow,
			Reason:     fmt.Sprintf("%d patches failed — explore the affected symbols for context", failed),
		})
	} else if applied > 0 {
		// Collect unique package directories from patched files.
		pkgDirSet := make(map[string]bool)
		for _, r := range results {
			if r.Status == GoPatchSuccess && r.FilePath != "" {
				pkgDirSet[filepath.Dir(r.FilePath)] = true
			}
		}
		targets := make([]string, 0, len(pkgDirSet))
		for dir := range pkgDirSet {
			targets = append(targets, dir)
		}
		if len(targets) == 0 {
			targets = []string{"./..."}
		}
		nextActions = append(nextActions, lang.NextAction{
			Tool:       "lang.go.verify",
			Confidence: lang.ConfidenceHigh,
			Reason:     "All patches applied — run full verification",
			Input: lang.VerifyInput{
				Targets: targets,
			},
		})
	}

	out := GoPatchOutput{
		Summary: GoPatchSummary{
			Applied: applied,
			Failed:  failed,
			Total:   len(results),
		},
		Results:     results,
		NextActions: nextActions,
	}
	applyAutoVerify(ctx, &out, input.AutoVerify, input.VerifySuites, input.DryRun)
	return out, nil
}

// processAtomicBatch applies ALL patches to disk first, then runs ONE
// module-wide `go vet` plus `go build` on the combined result. If the
// combined build fails, ALL files are rolled back atomically. This is
// the "database transaction" model for cross-file refactors — a
// cross-package change that requires every file to land together is
// either fully applied or fully rejected, never half-applied.
//
// Four phases:
//  1. Prepare every modification in memory: read original bytes,
//     apply edits via fs.ApplyEdits, parse-check, then goimports.
//     Files that fail any in-memory step are recorded as failures
//     immediately and excluded from the on-disk phases.
//  2. Write every prepared file to disk via fs.AtomicWrite. A write
//     failure here rolls back everything written so far and returns
//     immediately — the disk is back to its starting state.
//  3. Format every written file on disk and run ONE module-wide
//     `go vet` plus `go build`. On failure, rollback ALL files. The
//     compiler output is attached to the first result; subsequent
//     entries carry a short "rolled back (batch failure)" marker so
//     the agent does not see the same compiler output N times.
//  4. Success: generate unified DiffReceipts from the re-read
//     formatted content and return.
//
// DryRun short-circuits after phase 1 with would-be DiffReceipts. The
// combined build always runs from the first patched file's module
// root so go.work setups are honored.
func processAtomicBatch(ctx context.Context, input GoPatchInput) (GoPatchOutput, error) {
	// Phase 1: Prepare all changes in memory.
	type pendingFile struct {
		filePath string
		original []byte
		modified []byte
		index    int
		message  string
	}
	var pending []pendingFile
	var results []GoPatchResult

	for i, patch := range input.Patches {
		if !strings.HasSuffix(patch.FilePath, ".go") {
			results = append(results, GoPatchResult{
				PatchIndex: i,
				FilePath:   patch.FilePath,
				Status:     GoPatchFailure,
				Error:      fmt.Sprintf("not a Go file: %q", patch.FilePath),
			})
			continue
		}

		original, err := os.ReadFile(patch.FilePath)
		if err != nil {
			results = append(results, GoPatchResult{
				PatchIndex: i,
				FilePath:   patch.FilePath,
				Status:     GoPatchFailure,
				Error:      fmt.Sprintf("cannot read file: %v", err),
			})
			continue
		}

		modifiedStr, err := fs.ApplyEdits(string(original), patch.Edits)
		if err != nil {
			results = append(results, GoPatchResult{
				PatchIndex: i,
				FilePath:   patch.FilePath,
				Status:     GoPatchFailure,
				Error:      err.Error(),
			})
			continue
		}

		modified := []byte(modifiedStr)

		// In-memory parse check.
		if parseErr := parseCheckGo(patch.FilePath, modified); parseErr != nil {
			results = append(results, GoPatchResult{
				PatchIndex: i,
				FilePath:   patch.FilePath,
				Status:     GoPatchFailure,
				Error:      fmt.Sprintf("edit produces invalid Go syntax: %v", parseErr),
				Forensics: &PatchForensics{
					Hint: "The edited content does not parse as valid Go. Check old_string/new_string.",
				},
			})
			continue
		}

		// In-memory goimports.
		if formatted, fmtErr := importsProcess(patch.FilePath, modified); fmtErr == nil {
			modified = formatted
		}

		pending = append(pending, pendingFile{
			filePath: patch.FilePath,
			original: original,
			modified: modified,
			index:    i,
			message:  fmt.Sprintf("edited %d site(s)", len(patch.Edits)),
		})
	}

	if len(pending) == 0 {
		return GoPatchOutput{
			Summary: GoPatchSummary{Total: len(results), Failed: len(results)},
			Results: results,
		}, nil
	}

	// DryRun: generate diffs but don't write.
	if input.DryRun {
		for _, p := range pending {
			results = append(results, GoPatchResult{
				PatchIndex:  p.index,
				FilePath:    p.filePath,
				Status:      GoPatchSuccess,
				DiffReceipt: fs.GenerateDiff(p.filePath, p.original, p.modified),
			})
		}
		applied := len(pending)
		return GoPatchOutput{
			Summary: GoPatchSummary{Applied: applied, Total: applied + len(results) - applied},
			Results: results,
		}, nil
	}

	// Phase 2: Write ALL files to disk.
	var written []string
	for _, p := range pending {
		if err := fs.AtomicWrite(p.filePath, p.modified); err != nil {
			// Rollback everything written so far.
			for _, w := range written {
				for _, p2 := range pending {
					if p2.filePath == w {
						_ = fs.AtomicWrite(p2.filePath, p2.original)
						break
					}
				}
			}
			results = append(results, GoPatchResult{
				PatchIndex: p.index,
				FilePath:   p.filePath,
				Status:     GoPatchFailure,
				Error:      fmt.Sprintf("atomic write failed: %v", err),
			})
			return GoPatchOutput{
				Summary: GoPatchSummary{Failed: len(results), Total: len(results)},
				Results: results,
			}, nil
		}
		written = append(written, p.filePath)
	}

	// Format all written files on disk.
	for _, p := range pending {
		formatFile(ctx, p.filePath)
	}

	// Phase 3: ONE module-wide verification on the combined result.
	// Use the first file to find the module root.
	verifyOutput, verifyErr := vetAndBuild(ctx, pending[0].filePath)
	if verifyErr != nil {
		// Rollback ALL files.
		for _, p := range pending {
			_ = fs.AtomicWrite(p.filePath, p.original)
		}
		// Report each file as rolled back — but DON'T duplicate the compiler
		// output on every entry. Report it once on the first result, mark the
		// rest as "rolled back (batch failure)" for clarity.
		compilerOut := lang.LastNLines(verifyOutput, 15)
		hint := inferHint(verifyOutput)
		for i, p := range pending {
			r := GoPatchResult{
				PatchIndex: p.index,
				FilePath:   p.filePath,
				Status:     GoPatchFailure,
			}
			if i == 0 {
				r.Error = fmt.Sprintf("atomic batch build failed — all %d files rolled back", len(pending))
				r.Forensics = &PatchForensics{
					CompilerOutput: compilerOut,
					Hint:           hint,
				}
			} else {
				r.Error = "rolled back (batch failure — see first result for compiler output)"
			}
			results = append(results, r)
		}
		return GoPatchOutput{
			Summary: GoPatchSummary{Failed: len(pending), Total: len(pending) + len(results) - len(pending)},
			Results: results,
			NextActions: []lang.NextAction{
				{
					Tool:            "lang.go.explore",
					Confidence:      lang.ConfidenceLow,
					Reason:          "Atomic batch failed — explore the affected symbols",
					RiskDescription: "The combined changeset broke the build. Review the compiler output in the first result to identify which edit caused the failure.",
				},
			},
		}, nil
	}

	// Phase 4: Success — all files stay. Generate diffs.
	for _, p := range pending {
		// Re-read formatted content for the diff.
		formatted, _ := os.ReadFile(p.filePath)
		if formatted == nil {
			formatted = p.modified
		}
		results = append(results, GoPatchResult{
			PatchIndex:  p.index,
			FilePath:    p.filePath,
			Status:      GoPatchSuccess,
			DiffReceipt: fs.GenerateDiff(p.filePath, p.original, formatted),
		})
	}

	applied := len(pending)
	failed := len(results) - applied // pre-phase failures (parse errors etc)
	out := GoPatchOutput{
		Summary: GoPatchSummary{Applied: applied, Failed: failed, Total: len(results)},
		Results: results,
		NextActions: []lang.NextAction{{
			Tool:       "lang.go.verify",
			Confidence: lang.ConfidenceHigh,
			Reason:     fmt.Sprintf("All %d files applied atomically — run full verification", applied),
		}},
	}
	applyAutoVerify(ctx, &out, input.AutoVerify, input.VerifySuites, input.DryRun)
	return out, nil
}

// processPatch handles a single fs.FilePatch as an independent
// transaction. The path threads through five gates, any of which can
// reject the patch and short-circuit with a populated GoPatchResult:
//  1. .go extension check (catches accidental non-Go targets).
//  2. Read original bytes for rollback snapshot.
//  3. Pre-patch build gate: refuse to modify a file whose package
//     does not already build, unless Force=true (recovery mode).
//  4. In-memory parse validation after fs.ApplyEdits — catches ~80%
//     of failures (syntax errors, mismatched brackets) without any
//     disk I/O.
//  5. In-memory goimports.
//
// If DryRun, the result is generated via processDryRun and the
// function returns. Otherwise the function acquires a package
// directory flock to serialize concurrent edits in the same package,
// writes via fs.AtomicWrite, formats on disk (goimports may add or
// remove imports the in-memory pass missed), and re-runs the build
// gate against the on-disk module. Build-gate failure triggers an
// immediate rollback that is verified post-write — a failed rollback
// is logged to stderr as CRITICAL because at that point the agent's
// source is in an undefined state.
func processPatch(ctx context.Context, idx int, patch fs.FilePatch, dryRun, force bool) GoPatchResult {
	base := GoPatchResult{
		PatchIndex: idx,
		FilePath:   patch.FilePath,
	}

	// Validate it looks like a Go file.
	if !strings.HasSuffix(patch.FilePath, ".go") {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("not a Go file: %q", patch.FilePath)
		return base
	}

	// Snapshot: read original content.
	original, err := os.ReadFile(patch.FilePath)
	if err != nil {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("cannot read file: %v", err)
		return base
	}

	// Pre-patch gate: verify the file builds and vets BEFORE we touch it.
	// We don't patch broken files — unless Force is set (recovery mode).
	if !force {
		if preOutput, preErr := vetAndBuild(ctx, patch.FilePath); preErr != nil {
			base.Status = GoPatchFailure
			base.Error = "file already broken before patching — refusing to modify (use force=true to override for recovery)"
			base.Forensics = &PatchForensics{
				CompilerOutput: lang.LastNLines(preOutput, 10),
				Hint:           "Fix existing errors before applying new patches, or set force=true to bypass this gate for recovery from a failed refactor.",
			}
			return base
		}
	}

	// Apply edits sequentially using the shared helper.
	modifiedStr, err := fs.ApplyEdits(string(original), patch.Edits)
	if err != nil {
		base.Status = GoPatchFailure
		base.Error = err.Error()
		return base
	}

	modified := []byte(modifiedStr)

	// ── In-memory validation gate ──────────────────────────────────
	// Parse the modified content BEFORE touching disk. This catches ~80%
	// of failures (syntax errors, missing brackets, bad edits) without
	// any file I/O. Only syntactically valid Go reaches the disk-write path.
	if parseErr := parseCheckGo(patch.FilePath, modified); parseErr != nil {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("edit produces invalid Go syntax (file NOT modified): %v", parseErr)
		base.Forensics = &PatchForensics{
			Hint: "The edited content does not parse as valid Go. Check old_string/new_string for mismatched brackets, quotes, or statements.",
		}
		return base
	}

	// ── In-memory format ──────────────────────────────────────────
	// Run goimports in memory before writing. This fixes imports and
	// formatting without touching disk. If goimports fails, fall back
	// to the raw modified bytes (the disk-write path will format again).
	if formatted, fmtErr := importsProcess(patch.FilePath, modified); fmtErr == nil {
		modified = formatted
	}

	if dryRun {
		return processDryRun(ctx, idx, patch, original, modified)
	}

	// ── Package lock ──────────────────────────────────────────────
	// Prevent concurrent edits to files in the same package directory.
	pkgDir := filepath.Dir(patch.FilePath)
	unlock, lockErr := acquirePackageLock(pkgDir)
	if lockErr != nil {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("cannot acquire package lock: %v", lockErr)
		return base
	}
	defer unlock()

	// ── Disk write ────────────────────────────────────────────────
	// Write modified content via atomic temp+rename. The original bytes
	// are held in memory for instant rollback.
	if writeErr := fs.AtomicWrite(patch.FilePath, modified); writeErr != nil {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("cannot write file: %v", writeErr)
		return base
	}

	// From this point, any failure path MUST rollback.
	rollback := func() {
		_ = fs.AtomicWrite(patch.FilePath, original)
		// Verify the rollback actually restored the file.
		restored, readErr := os.ReadFile(patch.FilePath)
		if readErr != nil || !bytes.Equal(restored, original) {
			// This should never happen, but if it does the agent needs to know.
			fmt.Fprintf(os.Stderr, "CRITICAL: rollback verification failed for %s\n", patch.FilePath)
		}
	}

	// Format on disk (goimports may add/remove imports that in-memory pass missed).
	formatFile(ctx, patch.FilePath)

	// Re-read after disk formatting.
	formatted, err := os.ReadFile(patch.FilePath)
	if err != nil {
		rollback()
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("cannot read file after formatting: %v", err)
		return base
	}

	// Post-patch gate: go vet + go build. Catches type errors that
	// parse validation can't detect.
	if postOutput, postErr := vetAndBuild(ctx, patch.FilePath); postErr != nil {
		rollback()
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("post-patch verification failed: %v", postErr)
		base.Forensics = &PatchForensics{
			CompilerOutput: lang.LastNLines(postOutput, 10),
			Hint:           inferHint(postOutput),
		}
		return base
	}

	// Success — file stays as written+formatted. No rollback needed.
	base.Status = GoPatchSuccess
	base.DiffReceipt = fs.GenerateDiff(patch.FilePath, original, formatted)
	return base
}

// processDryRun runs the formatting and build gates on a temporary
// file rather than the target, then returns a DiffReceipt for the
// would-be change. The temp file lives in the same directory as the
// target so `go build` can resolve the surrounding package; for the
// build verification step the function temporarily replaces the
// original file with the formatted candidate, runs the gates, then
// unconditionally restores the original.
//
// The atomic replace-then-restore is critical: if the verification
// step crashes or is interrupted, the test infrastructure must still
// leave the file in its original state — hence the unconditional
// fs.AtomicWrite restore before returning.
func processDryRun(ctx context.Context, idx int, patch fs.FilePatch, original, modified []byte) GoPatchResult {
	base := GoPatchResult{
		PatchIndex: idx,
		FilePath:   patch.FilePath,
	}

	// Write to a temp file in the same directory so go build can resolve the package.
	dir := filepath.Dir(patch.FilePath)
	tmp, err := os.CreateTemp(dir, ".gopatch-dry-*.go")
	if err != nil {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("dry run: cannot create temp file: %v", err)
		return base
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, writeErr := tmp.Write(modified); writeErr != nil {
		tmp.Close()
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("dry run: cannot write temp file: %v", writeErr)
		return base
	}
	tmp.Close()

	// Format the temp file.
	formatFile(ctx, tmpPath)

	formatted, err := os.ReadFile(tmpPath)
	if err != nil {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("dry run: cannot read formatted temp file: %v", err)
		return base
	}

	// For dry run build verification we temporarily replace the original file.
	if err := fs.AtomicWrite(patch.FilePath, formatted); err != nil {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("dry run: cannot stage file for build: %v", err)
		return base
	}
	verifyOutput, verifyErr := vetAndBuild(ctx, patch.FilePath)
	// Always restore original.
	_ = fs.AtomicWrite(patch.FilePath, original)

	if verifyErr != nil {
		base.Status = GoPatchFailure
		base.Error = fmt.Sprintf("vet/build would fail: %v", verifyErr)
		base.Forensics = &PatchForensics{
			CompilerOutput: lang.LastNLines(verifyOutput, 10),
			Hint:           inferHint(verifyOutput),
		}
		return base
	}

	base.Status = GoPatchSuccess
	base.DiffReceipt = fs.GenerateDiff(patch.FilePath, original, formatted)
	return base
}

// formatFile runs goimports -w on the given file, falling back to
// gofmt -w when goimports is not on PATH. Errors are intentionally
// swallowed: this is a best-effort post-write polish that runs after
// the parse check has already confirmed valid Go, so any formatter
// error is non-fatal and would only make the diagnostic noisier.
func formatFile(ctx context.Context, path string) {
	if _, err := exec.LookPath("goimports"); err == nil {
		cmd := exec.CommandContext(ctx, "goimports", "-w", path)
		_ = cmd.Run()
		return
	}
	cmd := exec.CommandContext(ctx, "gofmt", "-w", path)
	_ = cmd.Run()
}

// vetAndBuild runs `go vet ./...` then `go build ./...` on the entire
// module containing filePath, returning the empty string and nil on
// success, or the combined output of the failing step with the
// exec.ExitError on failure.
//
// Runs the full module rather than the single package on the theory
// that correctness > speed: a silently broken module costs five-plus
// turns to diagnose, while a failed patch with clear diagnostics costs
// one retry turn.
//
// Workspace-aware: when the file lives inside a go.work setup, the
// function Discovers the workspace and expands the patterns to one
// per use directive. Without this, `./...` from the workspace root
// matches nothing and a cross-module break could go undetected.
func vetAndBuild(ctx context.Context, filePath string) (string, error) {
	// Resolve the module root for the changed file.
	dir := filepath.Dir(filePath)
	modRoot := dir
	for {
		if _, err := os.Stat(filepath.Join(modRoot, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(modRoot)
		if parent == modRoot {
			modRoot = dir // fallback to file's directory
			break
		}
		modRoot = parent
	}

	// Workspace-aware build patterns: in go.work mode, "./..." from a
	// workspace root does not match anything — every module's pattern
	// must be spelled out. Without this, a multi-module batch could
	// commit a build break in one module while building cleanly in
	// another. Discover() walks UP from modRoot to find go.work, so
	// it returns the workspace even when modRoot is a use directive.
	w, wsErr := workspace.Discover(modRoot)
	runDir := modRoot
	patterns := []string{"./..."}
	if wsErr == nil && w.IsGoWork() {
		runDir = w.Root()
		patterns = patterns[:0]
		for _, m := range w.Modules() {
			rel, relErr := filepath.Rel(w.Root(), m.Dir)
			if relErr != nil || rel == "." {
				patterns = append(patterns, "./...")
				continue
			}
			patterns = append(patterns, "./"+filepath.ToSlash(rel)+"/...")
		}
	}

	vetArgs := append([]string{"vet"}, patterns...)
	vetCmd := exec.CommandContext(ctx, "go", vetArgs...)
	vetCmd.Dir = runDir
	if vetOut, err := vetCmd.CombinedOutput(); err != nil {
		return string(vetOut), err
	}

	buildArgs := append([]string{"build"}, patterns...)
	buildCmd := exec.CommandContext(ctx, "go", buildArgs...)
	buildCmd.Dir = runDir
	if buildOut, err := buildCmd.CombinedOutput(); err != nil {
		return string(buildOut), err
	}

	return "", nil
}

// parseCheckGo validates that src is parseable Go without writing to
// disk. Returns nil if valid, or a parser error describing what is
// wrong (often pointing at the offending line and column). Used as
// the in-memory gate before fs.AtomicWrite to catch ~80% of bad
// patches (mismatched brackets, missing semicolons) before any disk
// activity.
//
// Returns an error for empty input so an accidental empty-string
// edit is rejected rather than producing an empty Go file.
func parseCheckGo(filename string, src []byte) error {
	if len(src) == 0 {
		return fmt.Errorf("content is empty")
	}
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, filename, src, parser.AllErrors)
	return err
}

// importsProcess runs goimports in memory via golang.org/x/tools/
// imports.Process without touching disk. Returns the formatted source
// or an error. Used as the pre-write polish so that imports are
// finalized before the build gate — a missing import that the disk
// goimports would have added is also added in-memory, avoiding a
// false build failure on the post-patch gate.
func importsProcess(filename string, src []byte) ([]byte, error) {
	return imports.Process(filename, src, nil)
}

// inferHint produces an actionable one-liner suggestion derived from
// the compiler output by pattern-matching on common error fragments:
// "undefined:" → check imports, "already declared" → use = instead of
// :=, "cannot use"/"type mismatch" → check expected type,
// "imported and not used" → goimports issue.
//
// Returns a generic "Build failed. Review the compiler output."
// fallback for unrecognized errors. The hint is surfaced through
// PatchForensics.Hint so an agent retrying a failed patch knows the
// likely fix without parsing the compiler output itself.
func inferHint(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "undefined:"):
		return "Variable or function not defined. Check naming or imports."
	case strings.Contains(lower, "already declared") || strings.Contains(lower, "redeclared"):
		return "Variable already exists in this scope. Use '=' instead of ':='."
	case strings.Contains(lower, "cannot use") || strings.Contains(lower, "type mismatch"):
		return "Type mismatch. Check the expected type."
	case strings.Contains(lower, "imported and not used"):
		return "Unused import after edit. The formatter should have handled this — check goimports."
	default:
		return "Build failed. Review the compiler output."
	}
}

// Verification status constants for auto_verify output.
// Verification status constants for auto_verify output.
// These are "Scoped Confidence" signals — they reflect what was checked,
// not full module correctness. Only an explicit lang.go.verify with ./...
// provides global confidence.
const (
	// VerificationTestOK indicates tests passed on the affected packages
	// in a Scoped Confidence sense. Logic elsewhere in the module is
	// untested; only an explicit lang.go.verify with ./... gives global
	// confidence.
	VerificationTestOK = "test_ok" // Tests passed on affected packages — logic untested elsewhere.
	// VerificationLintOK indicates lint passed on the affected packages.
	// No test behaviour was exercised; treat this as a syntactic and
	// stylistic green light, not a logic guarantee.
	VerificationLintOK = "lint_ok" // Lint passed on affected packages — logic untested.
	// VerificationUnverified indicates no auto-verification ran after the
	// patch. The default when AutoVerify is false or no patches applied.
	VerificationUnverified = "unverified" // No verification performed.
	// VerificationDegraded indicates verification ran and surfaced issues.
	// The patch was applied (and was NOT rolled back); inspect
	// VerifyOutput for the specific failures.
	VerificationDegraded = "degraded" // Verification ran and found issues.
	// VerificationTimeout indicates verification exceeded its budget,
	// which likely signals a hanging build, a recursive type error, or
	// an init() that does not return. The patch was applied; run a
	// manual lang.go.verify to investigate.
	VerificationTimeout = "timeout" // Verification timed out — build may be hanging.
)

// autoVerifyTimeout is the maximum duration for diagnostic
// auto-verify. Picked so that the common case (lint a few packages)
// finishes well within budget but a hung build is bounded — if
// lint or test takes longer than this, something is structurally
// wrong and the agent should hear about it via VerificationTimeout
// rather than waiting indefinitely.
const autoVerifyTimeout = 10 * time.Second

// applyAutoVerify runs diagnostic verification after a successful patch
// when autoVerify is true. Patches are NEVER rolled back based on the
// result — this is diagnostic only.
//
// The verification is scoped to the patched files' parent directories
// rather than the full module, both because that is the right scope
// for a quick sanity check and because the timeout budget is fixed.
// A context timeout of autoVerifyTimeout protects against hung builds;
// on timeout, VerificationStatus is set to VerificationTimeout and a
// NextAction prompting the agent to run manual verification is
// prepended (not appended) so it appears at the top of the action
// list.
//
// Skipped silently on DryRun, when no patches applied, or when
// autoVerify is false.
func applyAutoVerify(ctx context.Context, out *GoPatchOutput, autoVerify bool, verifySuites []string, dryRun bool) {
	if !autoVerify || out.Summary.Applied == 0 || dryRun {
		out.VerificationStatus = VerificationUnverified
		return
	}

	suites := verifySuites
	if len(suites) == 0 {
		suites = []string{lang.SuiteLint}
	}

	pkgDirSet := make(map[string]bool)
	for _, r := range out.Results {
		if r.Status == GoPatchSuccess && r.FilePath != "" {
			pkgDirSet[filepath.Dir(r.FilePath)] = true
		}
	}
	targets := make([]string, 0, len(pkgDirSet))
	for dir := range pkgDirSet {
		targets = append(targets, dir)
	}
	if len(targets) == 0 {
		targets = []string{"./..."}
	}

	verifyCtx, cancel := context.WithTimeout(ctx, autoVerifyTimeout)
	defer cancel()

	verifyOut, err := verifyHandler(verifyCtx, lang.VerifyInput{Suites: suites, Targets: targets})
	if err != nil {
		if verifyCtx.Err() == context.DeadlineExceeded {
			out.VerificationStatus = VerificationTimeout
			out.NextActions = append([]lang.NextAction{
				{
					Tool:            "lang.go.verify",
					Confidence:      lang.ConfidenceMedium,
					Reason:          "Auto-verify timed out — run manual verification to check for regressions",
					RiskDescription: "The build or lint took longer than 10s, which may indicate a recursive type error or hanging init(). Run full verification manually.",
					Input:           lang.VerifyInput{Targets: targets},
				},
			}, out.NextActions...)
			return
		}
		out.VerificationStatus = VerificationUnverified
		return
	}

	out.VerifyOutput = &verifyOut

	switch verifyOut.OverallStatus {
	case lang.StatusPass:
		out.VerificationStatus = VerificationTestOK
	case lang.StatusLintOK:
		out.VerificationStatus = VerificationLintOK
	default:
		out.VerificationStatus = VerificationDegraded
		out.NextActions = append([]lang.NextAction{{
			Tool:            "lang.go.explore",
			Confidence:      lang.ConfidenceLow,
			Reason:          "Verification found issues after patch — investigate root cause",
			RiskDescription: "The patch was applied but verification failed. Inspect verify_output for details.",
		}}, out.NextActions...)
	}
}
