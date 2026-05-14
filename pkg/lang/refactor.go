// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package lang

// Refactor input types — one per public lang.go.* refactoring tool. Each
// tool has its own narrow schema rather than a single union with an action
// discriminator, so the agent's schema view shows exactly the fields it
// needs to fill in for the chosen operation.
//
// Common fields across all 10 tools (Package, DryRun, AutoVerify, VerifySuites)
// are intentionally repeated rather than embedded so each tool's JSON schema
// is fully self-describing.

// AddParameter describes one parameter to append to a function's
// signature during a [ChangeSignatureInput] refactor.
//
// The parameter is appended at the end of the parameter list — the
// tool does not support insertion at arbitrary positions because
// reordering parameters silently changes the meaning of every existing
// call site. Names are not deduplicated against existing parameters;
// if Name collides with an existing one the underlying gopls refactor
// will fail and the transaction is rolled back.
type AddParameter struct {
	// Name is the Go identifier for the new parameter. Must satisfy the
	// identifier grammar — letters, digits, underscores, not starting with
	// a digit, and not a reserved keyword. The blank identifier `_` is
	// permitted (and useful when the new parameter exists only to satisfy
	// an interface signature).
	Name string `json:"name" jsonschema:"Go identifier for the new parameter."`
	// Type is the parameter's Go type as it would appear in source — e.g.
	// `context.Context`, `*http.Request`, `[]byte`, `chan<- string`. Any
	// imports required by the type are added automatically by goimports
	// after the rewrite. Generic type parameters defined on the enclosing
	// function can be referenced by name.
	Type string `json:"type" jsonschema:"Parameter type as Go source. Example: 'context.Context' or '*http.Request'."`
	// DefaultValue is the Go expression injected as the argument at every
	// existing call site. It must be a valid expression in the calling
	// scope of each caller — most often a zero value (`nil`, `0`, `""`)
	// or a placeholder (`context.TODO()`, `defaultLogger`). The tool does
	// not verify that the expression evaluates without panicking; agents
	// follow up with a full verify run when AutoVerify is set.
	DefaultValue string `json:"default_value" jsonschema:"Value injected at every existing call site. Example: 'context.TODO()' or 'nil'."`
}

// AddReturn describes one return value to append to a function's
// signature during a [ChangeSignatureInput] refactor.
//
// The return is added at the end of the return tuple. At every call
// site a fresh receiver is bound to the new return — see DefaultValue
// for the binding syntax. Direct returns inside the function body are
// NOT auto-rewritten; the agent must edit the function body to produce
// the new return value, typically in a follow-up turn.
type AddReturn struct {
	// Type is the new return value's Go type, written as it would appear
	// in a return list — e.g. `error`, `*Response`, `(int, error)` is NOT
	// allowed (each return is a separate AddReturn entry). Required
	// imports are added by goimports after the rewrite.
	Type string `json:"type" jsonschema:"Return type as Go source. Example: 'error' or '*Response'."`
	// DefaultValue is the assignment expression injected at every call
	// site. Conventionally `_` to discard the new return, or a bare
	// identifier like `err` to capture it for subsequent inspection. The
	// tool inserts the binding as `existingReturns, NEW := call(...)` —
	// the captured name lands in the caller's scope and may collide with
	// an existing local, which the build gate will surface.
	DefaultValue string `json:"default_value" jsonschema:"Assignment expression injected at every call site. Example: '_' to discard or 'err' to capture."`
}

// RenameInput drives lang.go.rename: a type-checked, project-wide
// identifier rename that updates the symbol's declaration and every
// reference in the workspace atomically.
//
// Use RenameInput in preference to grep + Edit for any non-trivial
// rename: gopls handles method dispatch through interfaces, embedded
// fields, shadowed locals, and import-alias references that text
// search routinely misses. The whole refactor commits through the
// build gate, so a syntactically invalid result rolls back rather than
// leaving the workspace half-renamed.
//
// Local-variable renames within a single function are unambiguous by
// name; package-level symbols and methods are resolved by Package and
// Symbol. When a name is overloaded (a function and a struct share it,
// or multiple files declare the same local), supply File and Line to
// pin the target.
type RenameInput struct {
	// Symbol identifies the declaration to rename. Use the bare name for
	// package-level functions, types, vars, and consts (`NewUser`); use
	// `Receiver.Method` for methods (`Engine.Run`). For local variables
	// supply the bare name and disambiguate with File/Line.
	Symbol string `json:"symbol" jsonschema:"Symbol to rename. Example: 'NewUser' or 'Engine.Run'."`
	// NewName is the replacement identifier. Must satisfy Go's identifier
	// grammar and must not collide with an existing identifier in the
	// same scope — gopls will refuse the rename and roll back. Exported/
	// unexported status is determined by NewName's first letter: changing
	// case changes visibility, which can ripple into external importers.
	NewName string `json:"new_name" jsonschema:"Replacement identifier."`
	// Package scopes the search for the declaration of Symbol. Accepts an
	// import path or workspace-relative directory; defaults to the current
	// working directory. The rename itself spans the whole workspace; this
	// field only resolves the symbol when its name is overloaded across
	// packages.
	Package string `json:"package,omitempty" jsonschema:"Target package import path or relative path. Defaults to the current directory."`
	// File pins the declaration to a specific source file. Combine with
	// Line when renaming a local variable that shadows a same-named one
	// elsewhere in the package, or when multiple declarations share the
	// bare name. Path is resolved relative to the module root.
	File string `json:"file,omitempty" jsonschema:"File path containing the symbol when needed to disambiguate local variables."`
	// Line is the 1-based line number of the declaration when File alone
	// is insufficient to disambiguate (e.g. multiple shadowed locals in
	// the same function). Zero means "use the first matching declaration
	// in File".
	Line int `json:"line,omitempty" jsonschema:"1-based line number when needed to disambiguate local variables."`
	// DryRun computes the full rename and returns the would-be diff
	// without writing to disk. Useful for previewing impact on a
	// wide-blast-radius identifier before committing. The build gate does
	// not run in dry-run mode — the result reports textual changes only.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the rename
	// is applied. Diagnostic only — failures are reported but the rename
	// is NOT rolled back, because by then the workspace is already
	// renamed and a rollback would itself need to be verified. Use
	// DryRun for true preview-then-apply workflows.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after a successful rename. Diagnostic only — changes are NOT rolled back if verification fails."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"] which is cheap and almost always
	// appropriate; add "test" for renames touching widely-used symbols
	// where semantic verification matters.
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only — smallest, best for autonomous batches.
	// "standard" (default) adds per-file diff snippets so the agent can
	// review what changed. "full" adds extra diagnostic context. On any
	// failure mode the response only carries failed entries regardless
	// of Detail.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// ChangeSignatureInput drives lang.go.change_signature: rewrites a
// function or method signature (adding parameters, adding returns,
// removing parameters) and updates every call site in the workspace
// atomically.
//
// Use ChangeSignatureInput in preference to manual edits whenever the
// function has more than one or two callers. The tool delegates to
// gopls's change-signature analyser, which knows about method dispatch,
// interface satisfaction, and embedded field promotion — none of which
// text-based edits can track reliably.
//
// The additions are positional: AddParams appends to the parameter
// list, AddReturns appends to the return tuple, RemoveParams removes
// by name (in the original signature). The tool refuses to apply a
// change that would silently demote a public symbol to package-private
// or break an interface satisfaction relationship — the build gate
// catches these and rolls back.
type ChangeSignatureInput struct {
	// Symbol identifies the function or method whose signature to change.
	// Bare name for package-level functions (`NewUser`); `Receiver.Method`
	// for methods (`Engine.Run`). The receiver name may use either value
	// or pointer form.
	Symbol string `json:"symbol" jsonschema:"Function or method to modify. Example: 'NewUser' or 'Engine.Run'."`
	// AddParams lists parameters to append to the parameter list, in
	// order. Each [AddParameter] carries the new param's name, type, and a
	// DefaultValue that is injected at every existing call site so the
	// refactor stays type-correct.
	AddParams []AddParameter `json:"add_params,omitempty" jsonschema:"Parameters to add. Each carries a default_value that is injected at all call sites."`
	// AddReturns lists return values to append to the return tuple, in
	// order. Each [AddReturn] supplies the type and a DefaultValue used as
	// the binding name at call sites. Note that the function body's
	// existing `return` statements are NOT rewritten — the agent must add
	// the extra return value in a follow-up edit.
	AddReturns []AddReturn `json:"add_returns,omitempty" jsonschema:"Return values to add. Each carries a default_value that is assigned at all call sites."`
	// RemoveParams lists parameter names to drop from the signature.
	// Matching arguments are removed from every call site by position;
	// calls that pass a non-trivial expression for the removed parameter
	// lose that expression's side effects, so the agent should review
	// call sites carefully or run with AutoVerify=true.
	RemoveParams []string `json:"remove_params,omitempty" jsonschema:"Parameter names to remove from the definition; matching arguments are dropped from every call site."`
	// Package scopes the lookup for Symbol's declaration. Accepts an
	// import path or workspace-relative directory; defaults to the current
	// working directory. Call-site rewrites span the whole workspace
	// regardless.
	Package string `json:"package,omitempty" jsonschema:"Target package. Defaults to the current directory."`
	// DryRun computes the full rewrite and returns the would-be diff
	// without writing to disk. The build gate does not run in dry-run mode
	// — the response reports textual changes only.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the change.
	// Diagnostic only — failures are reported but the change is NOT
	// rolled back.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after a successful change."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"]; add "test" for high-risk changes
	// where semantic verification matters.
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed entries
	// are returned regardless of Detail.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// ImplementInterfaceInput drives lang.go.implement_interface: adds
// method stubs to a target struct so its method set satisfies a
// given interface.
//
// The tool computes the difference between the interface's required
// method set and TargetStruct's existing method set, then generates a
// stub for each missing method with the correct signature and a
// user-specified body (or `panic("not implemented")` by default). The
// stubs are written to the file containing TargetStruct.
//
// Use this when scaffolding a new implementation of an established
// interface — a faster, less error-prone path than copying the
// interface definition and translating each method by hand.
type ImplementInterfaceInput struct {
	// TargetStruct is the struct that should be made to satisfy the
	// interface. Bare type name only — qualified names are not supported
	// because the destination of the generated stubs must live in the
	// local package.
	TargetStruct string `json:"target_struct" jsonschema:"Struct that should implement the interface. Example: 'PostgresStore'."`
	// Interface names the interface to implement. Supports three forms:
	// local (`Storage`), cross-package within the workspace
	// (`ports.EventStore`), and standard library (`io.Reader`). For
	// cross-package forms the package must already be imported by the
	// file, or be addable by goimports.
	Interface string `json:"interface" jsonschema:"Interface to implement. Supports local ('Storage'), cross-package ('ports.EventStore'), and stdlib ('io.Reader')."`
	// StubBody is the Go statement(s) emitted as each generated method's
	// body. Defaults to `panic("not implemented")` when empty. Common
	// alternatives: `return nil`, `return errors.New("todo")`. Multi-line
	// bodies are permitted but must parse as valid Go inside a function
	// body.
	StubBody string `json:"stub_body,omitempty" jsonschema:"Body for generated stubs. Default: panic(\"not implemented\")."`
	// Package scopes the lookup for TargetStruct's declaration. Accepts
	// an import path or workspace-relative directory; defaults to the
	// current working directory.
	Package string `json:"package,omitempty" jsonschema:"Package containing the target struct. Defaults to the current directory."`
	// DryRun computes the would-be stubs and returns them without writing
	// to disk. The build gate does not run in dry-run mode.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the stubs
	// are written. Diagnostic only — failures are reported but the stubs
	// are NOT removed.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after stubs are generated."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"]. The lint suite catches missing
	// imports and unused-parameter warnings on the new stubs.
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// ExtractFunctionInput drives lang.go.extract_function: lifts a
// contiguous range of statements out of an existing function into a
// newly declared function (or method) that the original site calls in
// the extracted range's place.
//
// The tool computes the captured-variable closure of the range,
// promotes them to parameters of the new function, and constructs a
// return tuple from whatever the range writes that is still live
// after it. Control flow that exits the range (a `return` or a
// `break` of an outer loop) is rejected — extracting such a range
// would require non-trivial control-flow plumbing that the tool does
// not attempt.
//
// Use for trimming long functions, deduplicating repeated logic, or
// turning anonymous blocks into named helpers.
type ExtractFunctionInput struct {
	// File is the Go source file containing the range to extract,
	// relative to the module root.
	File string `json:"file" jsonschema:"File containing the range to extract."`
	// StartLine is the 1-based line number of the first statement in the
	// range (inclusive). Lines that contain only whitespace or a comment
	// are permitted at the boundaries and are trimmed before the AST is
	// consulted.
	StartLine int `json:"start_line" jsonschema:"First line of the range, 1-based."`
	// EndLine is the 1-based line number of the last statement in the
	// range (inclusive). Must be greater than or equal to StartLine and
	// must land at a statement boundary — partial-statement ranges are
	// rejected with a clear error.
	EndLine int `json:"end_line" jsonschema:"Last line of the range, 1-based and inclusive."`
	// NewFuncName is the Go identifier for the extracted function. Must
	// satisfy the identifier grammar and not collide with another
	// top-level declaration in the destination file. Visibility (exported
	// vs. unexported) is determined by the leading letter.
	NewFuncName string `json:"new_func_name" jsonschema:"Identifier for the extracted function."`
	// Receiver is the optional method-receiver clause, written as it
	// would appear in source — e.g. `e *Engine`. Empty produces a
	// free-standing function; non-empty produces a method on the named
	// receiver type. The receiver type must be defined in the same
	// package as the destination file.
	Receiver string `json:"receiver,omitempty" jsonschema:"Method receiver (e.g. 'e *Engine'). Empty for a free function."`
	// TargetFile is the destination file for the new function. Empty
	// means "same as File". The file is created if it does not exist, in
	// which case the package clause is set to match File's package. The
	// target must reside in the same package directory; cross-package
	// extraction is not supported here (use lang.go.move_symbol after).
	TargetFile string `json:"target_file,omitempty" jsonschema:"Destination file. Created if missing. Defaults to the source file."`
	// Package scopes the lookup for File. Defaults to the current
	// working directory. Used primarily as a sanity check; the absolute
	// File path is the authoritative locator.
	Package string `json:"package,omitempty" jsonschema:"Target package. Defaults to the current directory."`
	// DryRun computes the would-be extraction and returns it without
	// writing to disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the
	// extraction. Diagnostic only — failures are reported but the
	// refactor is NOT rolled back.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after extraction."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// ExtractInterfaceInput drives lang.go.extract_interface: synthesises
// an interface declaration from the exported method set of a target
// struct and inserts it into the workspace.
//
// This is the inverse of [ImplementInterfaceInput] — instead of
// making a struct satisfy an existing interface, it derives an
// interface from an existing struct's API. Useful when introducing a
// mocking seam or refactoring concrete dependencies into pluggable
// ones.
//
// Only exported methods participate; unexported methods are skipped
// so the generated interface remains useable from other packages.
// Doc comments on each method are copied verbatim onto the
// interface, preserving the API contract.
type ExtractInterfaceInput struct {
	// TargetStruct is the struct whose exported method set should become
	// the interface. Bare type name only — qualified names are not
	// supported because the lookup is scoped by Package.
	TargetStruct string `json:"target_struct" jsonschema:"Struct whose methods become the interface."`
	// NewInterfaceName is the identifier for the generated interface
	// type. Must satisfy the identifier grammar and must not collide with
	// an existing declaration in the destination file. Capitalisation
	// determines visibility.
	NewInterfaceName string `json:"new_interface_name" jsonschema:"Name for the generated interface."`
	// TargetFile is the destination file for the new interface
	// declaration. Empty places the interface in the same file as
	// TargetStruct. The destination file must live in the same package
	// directory.
	TargetFile string `json:"target_file,omitempty" jsonschema:"Destination file. Defaults to the struct's file."`
	// Package scopes the lookup for TargetStruct. Accepts an import path
	// or workspace-relative directory; defaults to the current working
	// directory.
	Package string `json:"package,omitempty" jsonschema:"Package containing the struct. Defaults to the current directory."`
	// DryRun computes the would-be interface declaration and returns it
	// without writing to disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after generation.
	// Diagnostic only — failures are reported but the new declaration is
	// NOT removed.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after generation."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// ExtractVariableInput drives lang.go.extract_variable: binds an
// expression to a new local variable declared immediately above the
// expression's containing statement, then replaces every occurrence of
// the expression in the local scope with the new variable.
//
// Use when an expression is repeated, has a non-obvious meaning, or
// should be evaluated exactly once (the most common reason — converting
// a lazy expression that side-effects on each access into a captured
// value).
//
// The column range (StartCol/EndCol) is optional: when omitted the
// tool picks the most likely expression on Line using a simple priority
// order (if-condition, return value, assignment RHS). This is the
// lightweight path for the common case where there is only one obvious
// extractable expression on the line.
type ExtractVariableInput struct {
	// File is the Go source file containing the expression, relative to
	// the module root.
	File string `json:"file" jsonschema:"File containing the expression."`
	// Line is the 1-based line number of the expression to extract. The
	// expression must lie wholly on Line; multi-line expressions are not
	// supported.
	Line int `json:"line" jsonschema:"1-based line of the expression."`
	// VariableName is the Go identifier for the new local. Must satisfy
	// the identifier grammar and not shadow a name already in scope at
	// the insertion point.
	VariableName string `json:"variable_name" jsonschema:"Identifier for the new local."`
	// StartCol is the 1-based start column of the expression on Line.
	// Combine with EndCol to pin a specific expression when the line
	// contains multiple candidate expressions. Zero means "auto-detect"
	// (see EndCol).
	StartCol int `json:"start_col,omitempty" jsonschema:"1-based start column of the expression. Combined with end_col to pinpoint."`
	// EndCol is the 1-based end column of the expression on Line. Zero
	// triggers auto-detection: the tool picks the highest-priority
	// expression on the line (if-condition, return value, assignment
	// right-hand side). Auto-detection is the right default when the
	// range is unambiguous; supply both columns when it isn't.
	EndCol int `json:"end_col,omitempty" jsonschema:"1-based end column of the expression. If omitted, the tool auto-picks the most likely expression on the line (if-condition, return value, assignment RHS)."`
	// Package scopes the lookup for File. Defaults to the current
	// working directory.
	Package string `json:"package,omitempty" jsonschema:"Target package. Defaults to the current directory."`
	// DryRun computes the would-be edits and returns them without
	// writing to disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after extraction.
	// Diagnostic only — failures are reported but the refactor is NOT
	// rolled back.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after extraction."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// InlineConstantInput drives lang.go.inline_constant: replaces every
// use of a named constant with the constant's literal value, then
// deletes the declaration.
//
// The tool is intentionally narrow — it only inlines constants, not
// functions. Function inlining is supported via gopls's own
// inline-call action but is not exposed here because agents
// frequently misuse a generic "inline" label, and the failure modes
// (callable arguments evaluated multiple times after inlining,
// side-effect reordering) deserve explicit per-call deliberation.
//
// String, numeric, boolean, and iota-derived constants all inline
// verbatim. Group declarations (`const ( ... )`) have only the named
// constant removed; the surrounding group is preserved.
type InlineConstantInput struct {
	// Symbol is the name of the constant to inline (e.g. `MaxRetries`).
	// Must resolve to a single const declaration in Package; ambiguity
	// produces a clear error rather than guessing.
	Symbol string `json:"symbol" jsonschema:"Constant to inline. Example: 'MaxRetries'."`
	// Package scopes the lookup for the constant declaration. Accepts an
	// import path or workspace-relative directory; defaults to the
	// current working directory. Inlining rewrites span every package
	// that uses the constant, regardless of where it is declared.
	Package string `json:"package,omitempty" jsonschema:"Package containing the constant. Defaults to the current directory."`
	// DryRun computes the would-be inlinings and returns them without
	// writing to disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after inlining.
	// Diagnostic only — failures are reported but the refactor is NOT
	// rolled back.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after inlining."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// MovePackageInput drives lang.go.move_package: relocates an entire
// package directory to a new import path and rewrites every importer
// in the workspace to reference the new path.
//
// This is the heavy-weight cousin of [MoveFileInput] (which moves a
// single file across packages) and [MoveSymbolInput] (which moves a
// single declaration within a package). Use MovePackage when an entire
// package should logically live elsewhere — e.g. promoting an
// internal helper to a top-level public path, or consolidating two
// sibling packages under a new parent.
//
// The move is atomic: every source file in SourcePackage is rewritten
// and moved, every importer is updated, and the whole batch commits
// or rolls back through the build gate. If DestPackage already exists
// the move is rejected.
type MovePackageInput struct {
	// SourcePackage is the current import path of the package to move.
	// Accepts a module-relative path (e.g. `internal/old`). The directory
	// must contain at least one .go file.
	SourcePackage string `json:"source_package" jsonschema:"Source import path. Example: 'internal/old'."`
	// DestPackage is the destination import path. Must not already
	// exist. The package name (final path segment) becomes the new
	// package clause in every moved file. Renaming the directory
	// implicitly renames the package.
	DestPackage string `json:"dest_package" jsonschema:"Destination import path. Example: 'internal/new'."`
	// DryRun computes the would-be moves and importer rewrites and
	// returns them without touching disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the move.
	// Diagnostic only — failures are reported but the move is NOT
	// reverted.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after the move."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// SymbolDoc is one entry in a [DocumentInput] batch: a single symbol
// paired with the doc-comment text that should land above its
// declaration.
//
// The SymbolDoc shape — rather than a flat map of name to string —
// lets the tool surface per-entry validation errors ("this symbol
// was not found in the file") while still applying the rest of the
// batch. It also keeps the JSON schema printable in a tabular form
// that agents can compose mechanically.
type SymbolDoc struct {
	// Symbol identifies the declaration whose doc comment to set. Forms
	// supported:
	//
	//  - `Helper` for a top-level func, type, var, or const.
	//  - `Cache.Get` for a method on a type.
	//  - `Config.Timeout` for a field on a struct (the doc lands above
	//    the field).
	//  - `Active` for an individual spec inside a multi-spec const/var
	//    block (the doc lands above the spec, not the block).
	//
	// Names are resolved against the AST of the target file; mismatches
	// produce a per-entry error and do not abort the batch.
	Symbol string `json:"symbol" jsonschema:"Symbol whose doc comment to add or replace. Example: 'Helper' for a top-level function, 'Cache.Get' for a method, 'MaxRetries' for a constant."`
	// Doc is the doc-comment text. Plain prose or lines already prefixed
	// with `//` are both accepted; the tool prefixes each line with `// `
	// if it isn't already prefixed and (when configured) wraps prose at
	// the column limit. Per godoc convention the comment should begin with
	// the symbol name (e.g. `Helper formats a string ...`); when
	// DocumentInput.StrictPrefix is true the tool enforces this.
	Doc string `json:"doc" jsonschema:"Doc-comment text. Plain text or // lines — the tool prefixes each line with '// ' if not already prefixed. Per godoc convention, start with the symbol name (e.g., 'Helper formats a string ...')."`
}

// FileDocs is a per-file slice of [SymbolDoc] entries used to drive a
// multi-file [DocumentInput] batch via its Files field.
//
// Each FileDocs entry is independent — failures in one file do not
// block edits to another in the same batch — but the whole batch
// commits or rolls back atomically through the build gate. Useful
// when documenting a related cluster of types spread across several
// files in one turn.
type FileDocs struct {
	// File is the Go source file these comments target, relative to the
	// module root. The file must already exist; the tool does not create
	// files, since the comments only make sense against existing
	// declarations.
	File string `json:"file" jsonschema:"Path of the Go file these doc comments target."`
	// Comments is the symbol-to-doc list to apply to File. Each entry is
	// processed independently within the file; per-symbol failures
	// (missing symbol, validation error) are reported in the response
	// without aborting other entries in the same file.
	Comments []SymbolDoc `json:"comments" jsonschema:"Doc comments to apply to this file."`
}

// DocumentInput drives lang.go.document: rewrites doc comments for one
// or many symbols across one or many files, atomically through the
// build gate.
//
// Two input modes are supported:
//
//   - Single-file mode: supply File + Comments. All comments target
//     that file.
//   - Multi-file batch: supply Files. Each entry pairs a file with its
//     own symbol-to-doc list. The whole batch is one transaction —
//     validation failures in any file roll back every change.
//
// Supported symbol forms (resolved against each file's AST):
//
//   - `Foo`           top-level func, type, var, or const
//   - `Type.Method`   method on a type
//   - `Type.Field`    field on a struct (doc lands above the field)
//   - `Active`        an individual spec inside a multi-spec
//     const/var block (doc lands above the spec,
//     not the block)
//
// Quality controls — StrictPrefix, MaxLineLength, NoWrap — run
// before any edit hits disk; on validation failure every file is left
// untouched. This makes DocumentInput dramatically faster than
// running N [PatchEdit] operations: gopls is invoked once across the
// whole batch and the formatter handles indentation rather than the
// agent guessing line widths.
type DocumentInput struct {
	// File is the target Go source file in single-file mode, relative to
	// the module root. Mutually exclusive with Files — supply either
	// File+Comments or Files, never both.
	File string `json:"file,omitempty" jsonschema:"Target Go file (single-file mode). Either File+Comments or Files must be supplied."`
	// Comments is the symbol-to-doc list applied in single-file mode.
	// Ignored when Files is set.
	Comments []SymbolDoc `json:"comments,omitempty" jsonschema:"Doc comments to apply (single-file mode). Each entry pairs a symbol with its new doc text."`
	// Files is the per-file batch payload. When set it replaces the
	// File+Comments shape, allowing one atomic call to document symbols
	// across many files. Per-symbol and per-file errors are reported in
	// the response; the whole batch commits or rolls back together.
	Files []FileDocs `json:"files,omitempty" jsonschema:"Multi-file batch — replaces File + Comments. Each entry targets one file with its own symbol → doc list. The whole batch commits or rolls back atomically through the build gate."`
	// Mode controls how existing doc comments are handled. `replace` (the
	// default) overwrites whatever doc comment was there; `skip_existing`
	// leaves the existing comment in place if there is one and only adds
	// comments to undocumented symbols. Use `skip_existing` when
	// backfilling docs without disturbing curated existing prose.
	Mode string `json:"mode,omitempty" jsonschema:"How to handle existing doc comments. 'replace' (default): overwrite. 'skip_existing': leave the comment unchanged when one is already present."`
	// MaxLineLength is the column width at which prose paragraphs are
	// wrapped. Defaults to 80 when zero. Lines that already begin with
	// `//` (i.e. the agent pre-formatted them) and indented preformatted
	// lines are never wrapped, so manually formatted code blocks inside
	// docs survive intact. Set NoWrap to disable wrapping entirely.
	MaxLineLength int `json:"max_line_length,omitempty" jsonschema:"Wrap prose paragraphs at this column width. Default: 80. Lines already starting with '//' and indented (preformatted) lines are never wrapped. Set no_wrap=true to disable wrapping entirely."`
	// NoWrap disables line wrapping entirely. Use when the agent has
	// pre-formatted the comment exactly as it should appear on disk —
	// overrides MaxLineLength.
	NoWrap bool `json:"no_wrap,omitempty" jsonschema:"Disable line wrapping. Use when the agent has already formatted the comment exactly as it should appear."`
	// StrictPrefix toggles godoc-convention enforcement: every doc comment
	// must begin with the symbol's name (e.g. `Helper formats a string
	// ...`). Method comments (`Type.Method`) must begin with the bare
	// method name, not the receiver. Default false. Enable for libraries
	// where godoc rendering matters.
	StrictPrefix bool `json:"strict_prefix,omitempty" jsonschema:"Require each doc comment to start with the symbol name (godoc convention). Method docs (Type.Method) must start with the bare method name, not the type. Default: false (off)."`
	// ListMissing flips the tool into a query-only mode: instead of
	// writing any edits, it returns the list of exported symbols in the
	// target file(s) that have no doc comment. Useful as a pre-flight
	// quality gate before generating docs in a follow-up call.
	ListMissing bool `json:"list_missing,omitempty" jsonschema:"Don't apply any edits — instead return the list of exported symbols in the file(s) that have no doc comment. Useful as a quality gate before generating docs."`
	// DryRun computes the would-be edits (and validates them) but does
	// not write to disk. The response carries the diff snippets the agent
	// can review before applying.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview edits without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the
	// rewrite. Diagnostic only — doc comments do not affect compilation,
	// but a stale godoc tool may still surface warnings.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after the rewrite. Diagnostic only — comments don't affect compilation."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// MoveFileInput drives lang.go.move_file: relocates a single Go
// source file (with all its top-level declarations) to a different
// package directory and rewrites every importer in the workspace.
//
// MoveFile fills a niche between [MoveSymbolInput] (same-package only)
// and [MovePackageInput] (whole-package only): use it when a single
// file logically belongs in a sibling or new package. The destination
// directory may already contain other files in a compatible package;
// the moved file's package clause is rewritten to match.
//
// Transactional like other refactors: the move, package-clause
// rewrite, and importer updates commit through the build gate as one
// atomic batch, or roll back if anything fails.
type MoveFileInput struct {
	// File is the source Go file path, relative to the module root
	// (e.g. `pkg/util/strings.go`). The file must already be part of a
	// buildable package.
	File string `json:"file" jsonschema:"Source .go file path. Example: 'pkg/util/strings.go'."`
	// TargetFile is the destination file path. Must NOT already exist;
	// the parent directory may exist (with compatible package files) or
	// be created by the tool. The basename may differ from File's; the
	// tool does not require basename preservation.
	TargetFile string `json:"target_file" jsonschema:"Destination .go file path. Example: 'pkg/strings/strings.go'. Must not already exist; the parent directory may exist with compatible package files."`
	// DryRun computes the would-be move and importer rewrites without
	// touching disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the move.
	// Diagnostic only — failures are reported but the move is NOT
	// reverted.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after the move."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// MoveSymbolInput drives lang.go.move_symbol: relocates a single
// declaration (function, method, type, var, or const) to a different
// file within the same package.
//
// Use this to redistribute symbols across files for readability —
// grouping related types in one file, splitting an overgrown
// `util.go`, or extracting test fixtures into a `*_test.go` sibling.
// The move is local to the package; for cross-package moves use
// [MoveFileInput] (whole file) or refactor by hand.
//
// The symbol's declaration and its associated doc comment migrate
// together. Method moves carry along their receiver type's method
// set accounting so subsequent gopls queries continue to resolve
// correctly.
type MoveSymbolInput struct {
	// Symbol identifies the declaration to move. Bare name for top-level
	// declarations (`NewUser`, `MaxRetries`); `Receiver.Method` for
	// methods. Must resolve to exactly one declaration in Package.
	Symbol string `json:"symbol" jsonschema:"Symbol to move."`
	// TargetFile is the destination file within the same package
	// directory. Created with the correct package clause if missing.
	// Must live in the same package directory as the source — to move
	// across packages use [MoveFileInput] or [MovePackageInput].
	TargetFile string `json:"target_file" jsonschema:"Destination file. Created if missing."`
	// Package scopes the lookup for Symbol. Accepts an import path or
	// workspace-relative directory; defaults to the current working
	// directory.
	Package string `json:"package,omitempty" jsonschema:"Source package. Defaults to the current directory."`
	// DryRun computes the would-be move without touching disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the move.
	// Diagnostic only — failures are reported but the move is NOT
	// reverted.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after the move."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// SymbolMove is one entry in a [MoveSymbolsInput] batch: a single
// symbol paired with the file it should move to.
//
// The batch form (rather than N sequential [MoveSymbolInput] calls)
// lets gopls compute the whole rearrangement once and apply it as a
// single build-gated transaction — substantially faster on large
// rearrangements where every individual move would otherwise
// re-typecheck the package.
type SymbolMove struct {
	// Symbol identifies the declaration to move. Same form as
	// MoveSymbolInput.Symbol: bare name or `Receiver.Method`.
	Symbol string `json:"symbol" jsonschema:"Symbol to move. Same form as move_symbol's symbol — a top-level identifier or 'Type.Method'."`
	// TargetFile is the destination file in the same package directory
	// as the symbol's source. Created if missing. Multiple SymbolMove
	// entries may target the same TargetFile within a batch — the moves
	// are appended in input order.
	TargetFile string `json:"target_file" jsonschema:"Destination file. Must live in the same package directory as the source. Created if missing."`
	// File is an optional source-file hint. When empty the tool locates
	// the symbol via the workspace's type info, which is usually fine.
	// Supply File when the same name is declared in multiple files of
	// the package (e.g. build-tag-gated variants) to pin the right one.
	File string `json:"file,omitempty" jsonschema:"Optional source file hint. If empty, the source file is located via the workspace's type info."`
}

// DeleteFileInput drives lang.go.delete_file: removes a single Go
// source file from the workspace through the standard atomic refactor
// transaction.
//
// Unlike a raw `rm`, the deletion is gated on a successful build of
// the workspace afterwards: if the file still has references
// elsewhere the deletion is rolled back and the response surfaces the
// dangling references as build errors. This avoids leaving the
// workspace in a half-broken state from a careless delete.
//
// Use for tearing down obsolete code, removing a file that has been
// fully superseded by another, or cleaning up after a manual
// refactor.
type DeleteFileInput struct {
	// File is the source Go file path to delete, relative to the module
	// root (e.g. `pkg/util/strings.go`). The file must exist; deleting a
	// nonexistent file produces a clear error.
	File string `json:"file" jsonschema:"Source .go file path. Example: 'pkg/util/strings.go'."`
	// Package serves as the workspace anchor for symbol resolution.
	// Defaults to the current working directory. Used to locate the
	// workspace's go.mod / go.work for the build-gate run.
	Package string `json:"package,omitempty" jsonschema:"Workspace anchor; defaults to the current directory."`
	// DryRun computes whether the deletion would succeed (build still
	// passes) and returns the result without actually removing the
	// file.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the
	// deletion. Diagnostic only — failures are reported but the file is
	// NOT restored.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after the deletion."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only. 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// MoveSymbolsInput drives lang.go.move_symbols: relocates a batch of
// symbols in a single atomic transaction.
//
// Substantially faster than running N [MoveSymbolInput] calls when
// redistributing many symbols (file split, test reorganisation,
// breaking a god-object into focused files): the build gate runs
// once for the whole batch instead of per individual move, and gopls
// re-types the package once at the start.
//
// Within a batch:
//
//   - Multiple moves from the same source file compose: each
//     subsequent extraction sees the source file already modified by
//     the prior moves.
//   - Multiple moves landing on the same target file are appended in
//     input order (so the agent can control the final layout).
//   - A file cannot be both a source and a target in one batch — that
//     would require two-phase application; split into sequential
//     batches when you need it.
type MoveSymbolsInput struct {
	// Moves is the list of (symbol, target file) pairs to apply
	// atomically. Order matters within a target file (later entries land
	// below earlier ones); order does not matter across target files.
	Moves []SymbolMove `json:"moves" jsonschema:"List of (symbol, target_file) pairs to apply atomically."`
	// Package scopes the lookup for every Symbol in Moves. Accepts an
	// import path or workspace-relative directory; defaults to the
	// current working directory. All symbols in the batch must live in
	// this package.
	Package string `json:"package,omitempty" jsonschema:"Source package. Defaults to the current directory."`
	// DryRun computes the would-be rearrangement without touching disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the
	// rearrangement. Diagnostic only — failures are reported but the
	// moves are NOT reverted.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after the moves."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}

// TestSpec describes one Go test function to scaffold via
// [AddTestsInput]. The generator emits a `func TestName(t *testing.T)`
// stub plus, for each subtest, a `t.Run("name", func(t *testing.T)
// { t.Parallel(); t.Skip("TODO") })` block.
//
// The `t.Parallel()` and `t.Skip()` defaults are deliberate: parallel
// makes failure modes that depend on test isolation surface
// immediately, and t.Skip prevents the placeholder from being
// counted as a passing test until the agent fills in real assertions.
type TestSpec struct {
	// Name is the test function's identifier. The `Test` prefix is
	// automatically prepended if missing — `ParseArgs` becomes
	// `TestParseArgs`. Names must satisfy Go's identifier grammar and
	// should match the conventional `TestSubjectAction` form for grep-
	// and IDE-discoverability.
	Name string `json:"name" jsonschema:"Test function name. 'Test' prefix is added if missing (e.g. 'ParseArgs' becomes 'TestParseArgs')."`
	// Subtests is the list of subtest names to generate inside the
	// function. Each becomes one `t.Run` call with a `t.Parallel()` and
	// `t.Skip("TODO")` stub body. Empty produces a single-level test
	// function without any t.Run blocks. Names may contain spaces and
	// are emitted as string literals — `t.Run` accepts arbitrary names.
	Subtests []string `json:"subtests,omitempty" jsonschema:"Subtest names to generate within this function. Each becomes a t.Run call with t.Parallel() and t.Skip() stub body."`
}

// AddTestsInput drives lang.go.add_tests: scaffolds one or more Go
// test functions (with optional t.Run subtests) into a target test
// file.
//
// The tool exists because hand-typing test scaffolding is repetitive
// and error-prone: it is easy to forget `t.Parallel()`, mix up the
// receiver, or generate a syntactically invalid `_test.go` file. The
// scaffolder produces idiomatic stubs that compile cleanly and skip
// their bodies until the agent fills them in.
//
// Use for bootstrapping a test file for a new package, adding a
// batch of related test cases at once, or generating subtest tables
// for an established function. For one-off tests it is still simpler
// to write the code directly with Edit.
type AddTestsInput struct {
	// File is the destination test file path, relative to the module
	// root (e.g. `pkg/foo/foo_test.go`). Created with the correct
	// package clause if missing; if it already exists new functions are
	// appended at end-of-file or inserted immediately after After.
	File string `json:"file" jsonschema:"Destination test file path (e.g. 'pkg/foo/foo_test.go'). Created if missing; if it already exists new functions are appended or inserted after 'after'."`
	// Tests is the list of test functions to generate. At least one
	// entry is required — the tool refuses an empty batch rather than
	// running silently.
	Tests []TestSpec `json:"tests" jsonschema:"Test functions to generate. At least one required."`
	// After names an existing test function in File; new tests are
	// inserted immediately below it. The `Test` prefix is auto-added.
	// Empty appends new tests at end-of-file, the default. Use After to
	// keep related tests grouped in source-file order.
	After string `json:"after,omitempty" jsonschema:"Insert after this existing test function name ('Test' prefix added if missing). Defaults to appending at the end of the file."`
	// Package serves as the workspace anchor for build-gate verification.
	// Defaults to the current working directory.
	Package string `json:"package,omitempty" jsonschema:"Workspace anchor. Defaults to the current directory."`
	// DryRun computes the would-be generated text and returns it without
	// writing to disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after generation.
	// Diagnostic only — failures are reported but the new functions are
	// NOT removed.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after generation."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only. 'standard' (default): per-file diff snippets."`
}

// ChangeTypeInput drives lang.go.change_type: replaces a named type's
// definition with a new one and rewrites usage sites to remain valid.
//
// The canonical use case is converting a function-typed value into an
// interface with named methods (or vice versa) — a shape-shifting
// refactor that mechanical text edits cannot perform correctly
// because call expressions need to be reshaped, not just renamed.
// The MethodMapping field carries the call-site rewrite rules.
//
// The rewrite is atomic and gated on a successful build of the
// workspace. Usage sites the tool cannot mechanically map (e.g.
// expressions assigning the old type to a third-party callback API)
// are reported as build errors and roll back the transaction.
type ChangeTypeInput struct {
	// Symbol is the name of the type to redefine (e.g. `Edge`). Must
	// resolve to a single type declaration in Package.
	Symbol string `json:"symbol" jsonschema:"Type symbol whose definition will be replaced. Example: 'Edge'."`
	// NewTypeDefinition is the new RHS of the `type Symbol =` statement,
	// as Go source — e.g. `interface { Evaluate(Snapshot) Result }` or
	// `struct { Name string; Op func(int) int }`. Imports the new
	// definition introduces are added by goimports after the rewrite.
	NewTypeDefinition string `json:"new_type_definition" jsonschema:"New type definition as Go source. Example: 'interface { Evaluate(Snapshot) Result }'."`
	// MethodMapping rewrites usage sites. Keys are old usage patterns
	// and values are the new method calls to substitute. The special
	// key `__call__` maps direct invocations of a function-typed value
	// — `edge(args)` becomes `edge.<value>(args)`. Other keys map
	// specific old method names to new ones. Empty MethodMapping means
	// the new and old call shapes are compatible (e.g. struct → struct
	// with same field set).
	MethodMapping map[string]string `json:"method_mapping,omitempty" jsonschema:"Maps old usage patterns to new method calls. Key '__call__' maps direct invocations: edge(args) becomes edge.<value>(args)."`
	// Package scopes the lookup for Symbol's declaration. Accepts an
	// import path or workspace-relative directory; defaults to the
	// current working directory. Usage-site rewrites span the whole
	// workspace regardless.
	Package string `json:"package,omitempty" jsonschema:"Package containing the type. Defaults to the current directory."`
	// DryRun computes the would-be rewrites without touching disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk."`
	// AutoVerify triggers a follow-up verification run after the change.
	// Diagnostic only — failures are reported but the change is NOT
	// rolled back.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint after the change."`
	// VerifySuites lists which verification suites to run when AutoVerify
	// is true. Defaults to ["lint"].
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Verification suites to run when auto_verify is true. Default: ['lint']."`
	// Detail controls output verbosity. "summary" returns counts and
	// file paths only; "standard" (default) adds per-file diff snippets;
	// "full" adds extra diagnostic context. On failure only failed
	// entries are returned.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only (smallest, ideal for autonomous batches). 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`
}
