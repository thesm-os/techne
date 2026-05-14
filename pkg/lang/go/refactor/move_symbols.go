// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"
	"os"
)

// MoveSymbolsAction moves a batch of symbols in a single transaction. Each move
// has the same semantics as [MoveSymbolAction]; batching amortizes the build
// gate over many moves instead of paying for one verification per symbol —
// important when redistributing dozens of symbols (e.g., splitting an oversized
// file or reorganizing tests across files).
//
// Composition within a batch:
//
//   - Multiple moves out of the same source file are applied in input order
//     against the modified bytes from the previous extraction, so symbols don't
//     trip over each other's offsets.
//   - Multiple moves landing in the same target file are concatenated in input
//     order.
//   - A symbol can be moved from one source to any target in the same batch as
//     long as the constraints below are satisfied.
//
// Constraints:
//
//   - Each move's source and target must live in the same package directory
//     (the [MoveSymbolAction] constraint applies per-entry).
//   - A file cannot appear as BOTH a source and a target in the same batch —
//     that would express a circular reorganization where some symbols leave the
//     file while others arrive, which is clearer to read as two sequential
//     batches. The action rejects this up front with a clear error.
//
// The whole batch is atomic: if the build gate fails on commit, every source
// and target rolls back. There is no partial application.
type MoveSymbolsAction struct{}

// Name implements [RefactorStrategy] and returns [ActionMoveSymbols].
func (*MoveSymbolsAction) Name() string { return ActionMoveSymbols }

func init() { RegisterAction(&MoveSymbolsAction{}) }

// Execute is the [RefactorStrategy] entry point. It validates every entry,
// plans the moves, applies all extractions in input order, and stages each
// source and target file exactly once for atomic commit.
//
// Pipeline:
//
//  1. Validate that input.Moves is non-empty and every entry has Symbol and
//     TargetFile.
//  2. Plan each move via [msPlanMove], which resolves source files and rejects
//     cross-package moves.
//  3. Reject batches where any file is both source and target.
//  4. Apply extractions per source file; later moves from the same source
//     operate on the already-modified bytes.
//  5. Group extracted pieces by target file and append them to each target's
//     existing content (or a synthesized package clause for new files).
//  6. Stage every unique source and target through ws.AddChange.
//
// Failure modes:
//
//   - input.Moves empty or any entry missing Symbol/TargetFile — early error.
//   - Cross-package move in any entry — error identifying the offending move
//     index.
//   - A file is both source and target — error identifying it.
//   - A symbol is not found in its source file — error identifying the move
//     index and symbol.
//   - The build gate fails — full rollback (all sources and targets restored).
func (*MoveSymbolsAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if len(input.Moves) == 0 {
		return fmt.Errorf("at least one move is required for move_symbols")
	}
	for i, m := range input.Moves {
		if m.Symbol == "" {
			return fmt.Errorf("moves[%d]: symbol is required", i)
		}
		if m.TargetFile == "" {
			return fmt.Errorf("moves[%d] (%s): target_file is required", i, m.Symbol)
		}
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	type plannedMove struct {
		symbol     string
		sourceFile string
		targetFile string
		pieces     []string // extracted source text(s) — populated during extraction
	}
	planned := make([]plannedMove, 0, len(input.Moves))
	sourceFiles := make(map[string]bool, len(input.Moves))
	targetFiles := make(map[string]bool, len(input.Moves))
	for i, m := range input.Moves {
		plan, planErr := msPlanMove(pkgs, ws.ModRoot(), m.File, m.Symbol, m.TargetFile)
		if planErr != nil {
			return fmt.Errorf("moves[%d] (%s): %w", i, m.Symbol, planErr)
		}
		planned = append(planned, plannedMove{
			symbol:     m.Symbol,
			sourceFile: plan.sourceFile,
			targetFile: plan.targetFile,
		})
		sourceFiles[plan.sourceFile] = true
		targetFiles[plan.targetFile] = true
	}
	for path := range sourceFiles {
		if targetFiles[path] {
			return fmt.Errorf(
				"file %s appears as both a source and a target in this batch — split into separate batches",
				path,
			)
		}
	}

	// Apply extractions in input order so symbols from the same source
	// compose against the latest modified bytes. Track which pieces were
	// produced by each move so they land on the correct target.
	sourceState := make(map[string]msSourceState, len(sourceFiles))
	for i := range planned {
		state, ok := sourceState[planned[i].sourceFile]
		if !ok {
			state, err = msReadSourceState(planned[i].sourceFile)
			if err != nil {
				return err
			}
		}
		before := len(state.extracted)
		state, err = msApplyExtraction(state, []string{planned[i].symbol})
		if err != nil {
			return fmt.Errorf("moves[%d] (%s): %w", i, planned[i].symbol, err)
		}
		planned[i].pieces = append([]string(nil), state.extracted[before:]...)
		sourceState[planned[i].sourceFile] = state
	}

	// Stage each unique source file once. The build gate runs once at
	// transaction commit time.
	for sourceFile, state := range sourceState {
		count := 0
		for _, p := range planned {
			if p.sourceFile == sourceFile {
				count++
			}
		}
		msg := fmt.Sprintf("moved %d symbol(s) out of %s", count, sourceFile)
		if err := ws.AddChange(sourceFile, state.original, state.modified, msg); err != nil {
			return err
		}
	}

	// Group pieces by target file (input order preserved) and stage each
	// target once with all its appendments.
	type targetGroup struct {
		pieces  []string
		pkgName string
		count   int
	}
	byTarget := make(map[string]*targetGroup, len(targetFiles))
	for _, p := range planned {
		g := byTarget[p.targetFile]
		if g == nil {
			g = &targetGroup{}
			byTarget[p.targetFile] = g
		}
		if g.pkgName == "" {
			g.pkgName = sourceState[p.sourceFile].pkgName
		}
		g.pieces = append(g.pieces, p.pieces...)
		g.count++
	}
	for targetFile, g := range byTarget {
		targetOriginal, _ := os.ReadFile(targetFile)
		modified := msComposeTargetContent(targetOriginal, g.pkgName, g.pieces)
		msg := fmt.Sprintf("received %d symbol(s) at %s", g.count, targetFile)
		if err := ws.AddChange(targetFile, targetOriginal, modified, msg); err != nil {
			return err
		}
	}

	return nil
}
