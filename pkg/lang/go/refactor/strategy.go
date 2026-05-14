// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"

	"golang.org/x/tools/go/packages"
)

// Transaction is the surface every refactor action depends on to stage edits
// and have them committed atomically against a Go workspace.
//
// The interface exists to decouple action logic from the production workspace
// driver: actions describe what should change (queue a file edit, queue a
// delete, attach an advisory note) and the implementation decides when and how
// it is persisted. The concrete production type is [WorkspaceTransaction];
// tests substitute a fake that records the same calls without running goimports
// or invoking the build gate, which lets the edit-logic of every action be
// exercised without a real Go module on disk.
//
// Lifecycle is staging-then-commit:
//
//  1. The action calls LoadPackages once to obtain type-checked
//     packages.Package values.
//  2. Edits are accumulated by repeated AddChange / AddFileMove /
//     AddDelete calls — none of these touch disk.
//  3. Skipped files are recorded with AddSkipped; advisory messages
//     with AddNote.
//  4. The framework (see [Handle]) calls Commit on the transaction,
//     which writes everything atomically, runs go mod tidy + go build,
//     and rolls every file back to its original snapshot if the build
//     gate fails.
//
// Implementations are NOT goroutine-safe: an action's Execute method is the
// only writer and runs to completion before Commit is invoked.
type Transaction interface {
	// AddChange stages an edit to filePath, snapshotting the original
	// bytes on first encounter so the change can be rolled back, then
	// running goimports in-memory before storing the modified version.
	//
	// Empty or unparseable modified bytes are rejected — otherwise a
	// buggy action could leave the workspace in a state where the build
	// gate cannot meaningfully report what broke. A goimports failure
	// (e.g. ambiguous imports) is non-fatal and the unformatted bytes
	// are stored instead; the build gate is the final arbiter.
	//
	// message is a short, human-readable summary surfaced in the per-
	// file FileResult (e.g. "renamed 3 occurrence(s) of Foo to Bar").
	AddChange(filePath string, original, modified []byte, message string) error

	// AddFileMove stages a file rename: oldPath is queued for deletion
	// at commit time and newPath is created with newContent. The
	// original bytes at oldPath are snapshotted so rollback can restore
	// them if the build gate fails.
	//
	// Used by move_file and move_package; the rename is logically a
	// single operation but is expressed as a (delete + create) pair so
	// it composes with every other staged change in the same
	// transaction.
	AddFileMove(oldPath, newPath string, newContent []byte, message string) error

	// AddDelete queues filePath for removal at commit time. The build
	// gate still runs after deletions are applied, so references to
	// symbols defined in the removed file cause the entire transaction
	// (deletions and modifications alike) to roll back.
	//
	// Used primarily by delete_file. Returns an error if the file is
	// unreadable, since the rollback path needs a snapshot of the
	// original bytes to restore on build failure.
	AddDelete(filePath, message string) error

	// AddSkipped records a no-op result for a file the action chose
	// not to modify (e.g. implement_interface called on a struct that
	// already implements every interface method). The result is
	// surfaced in Output.Results with Status set to [StatusSkipped] so
	// callers can distinguish intentionally untouched from accidentally
	// missed.
	AddSkipped(filePath, message string)

	// AddNote attaches an advisory message to Output.Notes (deduped by
	// the implementation). Notes are for side effects the framework
	// cannot perform itself — e.g., a cross-module move that shifts
	// go.mod requirements and needs the user to run `go mod tidy` in
	// both affected modules. Surface notes to the user verbatim.
	AddNote(message string)

	// LoadPackages returns the workspace's packages loaded with full
	// syntax and type information, suitable for type-checked symbol
	// lookups via [FindSymbolObject] and friends.
	//
	// The production implementation honors go.work, so cross-module
	// workspaces are loaded in a single call. Actions should call this
	// at most once per Execute; the implementation does not cache by
	// default.
	LoadPackages(ctx context.Context) ([]*packages.Package, error)

	// ModRoot returns the resolved module root directory — the
	// directory containing the go.mod nearest to the package targeted
	// by the input. Actions use it to anchor module-relative file
	// paths and as the working directory for shelling out to the Go
	// toolchain (e.g., `go mod tidy`).
	ModRoot() string
}

// RefactorStrategy is the interface every refactoring action implements and
// registers under a stable Name() in the package-level registry.
//
// Name() must return one of the Action* constants from models.go so the string
// routing performed by [Handle] stays consistent with the jsonschema
// documentation exposed to tool callers. Execute receives the fully decoded
// [Input] and a fresh [Transaction]; it must stage every required edit before
// returning. The framework calls Commit on the transaction once Execute returns
// nil — if Execute returns an error, no files are written and no diagnostics
// are reported beyond that error.
type RefactorStrategy interface {
	// Name returns the stable string identifier the action is
	// registered under. The value MUST match one of the Action*
	// constants in models.go, since [Handle] dispatches by exact
	// string match against Input.Action.
	Name() string

	// Execute performs the refactoring against ws, staging edits via
	// [Transaction.AddChange] / [Transaction.AddFileMove] /
	// [Transaction.AddDelete]. It MUST NOT touch disk directly —
	// every file mutation goes through ws so the framework's atomic-
	// commit and rollback semantics apply.
	//
	// Returning a non-nil error skips Commit entirely; staged edits
	// are discarded and no files change. Returning nil hands control
	// to the framework, which writes everything, runs the build gate,
	// and either reports success or rolls back to the snapshotted
	// originals.
	Execute(ctx context.Context, input Input, ws Transaction) error
}

var registry = map[string]RefactorStrategy{}

// RegisterAction adds a strategy to the package-level registry, keyed by its
// Name(). Called once per strategy from init(), so registration order is
// deterministic but unspecified — actions MUST NOT rely on other actions being
// registered when their own init() runs.
//
// A second call with the same Name() silently overwrites the earlier entry.
// This is by design: a test can swap in a fake strategy for the duration of the
// test, but production code should never register two strategies under the same
// key.
func RegisterAction(a RefactorStrategy) {
	registry[a.Name()] = a
}

// Handle is the entry point invoked by the lang.go.refactor tool framework. It
// looks up the registered strategy for input.Action, constructs a fresh
// [WorkspaceTransaction] rooted at input.Package, executes the strategy, and
// commits the resulting transaction.
//
// Failure modes:
//   - input.Action is not registered — returns an error naming the unknown
//     action; no files touched.
//   - The strategy's Execute returns an error — returned to the caller verbatim
//     with Status set to [StatusFailure]; staged edits are discarded without
//     writing.
//   - Commit's build gate fails — every modified or deleted file is rolled back
//     to its snapshot and the Go build diagnostic is surfaced as the error
//     message.
//
// The call is single-shot — there is no incremental commit or partial rollback.
// A new Transaction is created per call so concurrent invocations on disjoint
// packages are independent.
func Handle(ctx context.Context, input Input) (Output, error) {
	strategy, ok := registry[input.Action]
	if !ok {
		return Output{Status: StatusFailure}, fmt.Errorf("unknown refactor action: %q", input.Action)
	}

	ws := NewTransaction(input.Package, input.DryRun)

	if err := strategy.Execute(ctx, input, ws); err != nil {
		return Output{Status: StatusFailure}, err
	}

	return ws.Commit(ctx)
}
