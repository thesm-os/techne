// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import "go.thesmos.sh/techne/pkg/lang"

// Action constants
const (
	// ActionRename renames a symbol and all of its references project-wide.
	ActionRename = "rename"
	// ActionChangeSignature adds, removes, or modifies parameters and return
	// values on a function or method, updating every call site.
	ActionChangeSignature = "change_signature"
	// ActionImplementInterface generates method stubs on a target struct to
	// satisfy a named interface.
	ActionImplementInterface = "implement_interface"
	// ActionExtractFunction extracts a line range into a new top-level function or
	// method.
	ActionExtractFunction = "extract_function"
	// ActionExtractInterface derives an interface from a struct's exported methods
	// and rewrites uses where applicable.
	ActionExtractInterface = "extract_interface"
	// ActionInline replaces every reference to a constant or simple symbol with
	// its literal value and removes the original declaration.
	ActionInline = "inline"
	// ActionMovePackage relocates an entire package to a new import path and
	// rewrites every importer in the workspace.
	ActionMovePackage = "move_package"
	// ActionExtractVariable hoists an expression into a named local variable.
	ActionExtractVariable = "extract_variable"
	// ActionMoveSymbol moves a single top-level symbol (or method) into a
	// different file within the same package.
	ActionMoveSymbol = "move_symbol"
	// ActionMoveSymbols moves several symbols in one transaction, sharing a single
	// build verification across the batch.
	ActionMoveSymbols = "move_symbols"
	// ActionMoveFile relocates a Go file to a new path, updating the package
	// clause and every importer when the destination package differs.
	ActionMoveFile = "move_file"
	// ActionChangeType replaces a type definition and rewrites usage sites
	// according to a method mapping.
	ActionChangeType = "change_type"
	// ActionDocument writes or refreshes doc comments on a batch of symbols across
	// one or more files.
	ActionDocument = "document"
	// ActionDeleteFile removes a Go file from disk after confirming the workspace
	// still builds without it.
	ActionDeleteFile = "delete_file"
	// ActionAddTests scaffolds Go test functions with optional subtests next to an
	// existing source file.
	ActionAddTests = "add_tests"
)

// Status constants
const (
	// StatusSuccess indicates the operation completed and all changes were
	// committed.
	StatusSuccess = "success"
	// StatusFailure indicates the operation failed and any partial changes were
	// rolled back.
	StatusFailure = "failure"
	// StatusSkipped indicates the operation made no changes because the target was
	// already in the desired state.
	StatusSkipped = "skipped"
)

// Input is the union of every parameter accepted by the lang.go.refactor tool
// across all registered actions. Only a subset is relevant per Action — the
// jsonschema tags advertise per-field applicability to the LLM agent so unused
// fields can be omitted.
//
// Why one struct instead of one per action: the tool framework decodes a single
// JSON object before [Handle] inspects Action and dispatches to a strategy.
// Sharing the struct keeps schema generation, validation, and CLI/MCP/TUI
// wiring uniform. Strategies pick out only the fields they care about and
// ignore the rest; an action that returns an error for a missing field is
// responsible for the error message.
//
// Field groups (commented inline below) follow the per-action conventions used
// by the strategy registry:
//
//   - General: Action, AutoVerify, VerifySuites, Package, File, Line, Symbol,
//     DryRun, Detail.
//   - Per-action groups: rename (NewName), change_signature
//     (AddParams/AddReturns/RemoveParams), implement_interface
//     (TargetStruct/Interface/StubBody), extract_function
//     (StartLine/EndLine/NewFuncName/Receiver/TargetFile), extract_interface
//     (NewInterfaceName), move_package (SourcePackage/DestPackage),
//     move_symbols (Moves), extract_variable (VariableName/StartCol/EndCol),
//     change_type (NewTypeDefinition/MethodMapping), add_tests (Tests/After),
//     document (Comments/DocumentFiles/Mode/MaxLineLength/NoWrap/
//     StrictPrefix/ListMissing).
type Input struct {
	// Action selects which refactoring to apply. Must be one of the Action*
	// constants.
	Action string `json:"action" jsonschema:"Refactoring action to apply. One of: rename, change_signature, implement_interface, extract_function, extract_interface, extract_variable, inline, move_package, move_symbol, change_type."`
	// AutoVerify runs diagnostic verification after a successful refactor.
	// Failures are reported but do not roll back the change. Default: false.
	AutoVerify bool `json:"auto_verify,omitempty" jsonschema:"Run lint verification after successful refactor. Diagnostic only — changes are NOT rolled back on failure. Default: false."`
	// VerifySuites selects which suites run when AutoVerify is true. Default:
	// ["lint"]. Valid values: lint, test, bench, fuzz.
	VerifySuites []string `json:"verify_suites,omitempty" jsonschema:"Suites to run when auto_verify is true. Default: ['lint']. Options: lint, test, bench, fuzz."`
	// Package is the target package, given as an import path ("core/ledger") or
	// filesystem path ("./pkg/fs", ".").
	Package string `json:"package,omitempty" jsonschema:"Target package import path or filesystem path. Example: './pkg/fs', 'core/ledger', or '.' for current directory. AGENT HINT: Use lang.go.search to confirm the package path first."`
	// File is the module-relative path of the target file. Required for
	// range-based actions such as extract_function and extract_variable, and to
	// disambiguate local symbols.
	File string `json:"file,omitempty" jsonschema:"Target file path relative to module root. Example: 'pkg/core/types.go'. Required for extract_function, extract_variable, and local variable disambiguation."`
	// Line is a 1-based line number used to pin down a local symbol or expression.
	Line int `json:"line,omitempty" jsonschema:"1-based line number. Required to disambiguate local variables and for extract_variable. AGENT HINT: Use line numbers from lang.go.explore location fields."`
	// Symbol is the target identifier. Accepts top-level names ("NewUser"),
	// qualified methods ("Engine.Run"), and field references.
	Symbol string `json:"symbol,omitempty" jsonschema:"Target symbol to act on. Example: 'NewUser', 'Engine.Run', or 'MaxRetries'. AGENT HINT: Use symbol names from lang.go.search or lang.go.explore results."`
	// DryRun reports the diff the refactor would produce without writing anything
	// to disk.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without writing to disk. Use to validate a complex refactor before committing. The build gate runs against the post-change projection via 'go build -overlay', so build_status:pass means applying for real is guaranteed to compile."`
	// Detail controls output verbosity: "summary", "standard" (default), or
	// "full".
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity. 'summary': counts and file paths only. 'standard' (default): per-file diff snippets. 'full': adds extra diagnostic context. On failure, only failed entries are returned regardless of mode."`

	// rename
	NewName string `json:"new_name,omitempty" jsonschema:"For rename: new identifier name. All references project-wide are updated atomically. Example: 'CreateUser' to rename 'NewUser'."`

	// change_signature
	AddParams []AddParameter `json:"add_params,omitempty" jsonschema:"For change_signature: parameters to add to the function signature. All callers are updated with DefaultValue. Example: [{name: 'ctx', type: 'context.Context', default_value: 'context.TODO()'}]."`
	// AddReturns lists return values to append during change_signature. Every call
	// site receives the corresponding DefaultValue assignment.
	AddReturns []AddReturn `json:"add_returns,omitempty" jsonschema:"For change_signature: return values to add. All call sites receive the DefaultValue assignment. Example: [{type: 'error', default_value: '_'}]."`
	// RemoveParams names parameters to drop during change_signature. Matching
	// arguments are stripped from every call site.
	RemoveParams []string `json:"remove_params,omitempty" jsonschema:"For change_signature: parameter names to remove from the definition. Corresponding arguments are also removed from ALL call sites."`
	// RemoveReturns names return types to drop during change_signature,
	// matched left-to-right against the original signature. Signature-only:
	// body returns and call-site assignments are NOT auto-rewritten.
	RemoveReturns []string `json:"remove_returns,omitempty" jsonschema:"For change_signature: return-type names to drop from the definition, matched left-to-right. Example: ['error']. Signature-only — body returns and call-site bindings are NOT auto-rewritten; the build gate will fail until those are fixed in a follow-up edit."`

	// implement_interface
	TargetStruct string `json:"target_struct,omitempty" jsonschema:"For implement_interface: struct to add method stubs to. Example: 'PostgresStore'. AGENT HINT: Use lang.go.search to find the struct first."`
	// Interface names the interface to implement. Accepts local ("Storage"),
	// cross-package ("ports.EventStore"), and stdlib ("io.Reader") forms.
	Interface string `json:"interface,omitempty" jsonschema:"For implement_interface: interface to implement. Supports local ('Storage'), cross-package ('ports.EventStore'), and stdlib ('io.Reader') interfaces."`
	// StubBody is the body written into generated method stubs for
	// implement_interface. Default: panic("not implemented").
	StubBody string `json:"stub_body,omitempty" jsonschema:"For implement_interface: body for generated method stubs. Default: panic(\"not implemented\"). Example: 'return nil, nil'."`

	// extract_function
	StartLine int `json:"start_line,omitempty" jsonschema:"For extract_function: first line of the range to extract (1-based). AGENT HINT: Use line numbers from lang.go.explore location fields."`
	// EndLine is the 1-based inclusive last line of the range to extract for
	// extract_function.
	EndLine int `json:"end_line,omitempty" jsonschema:"For extract_function: last line of the range to extract (1-based, inclusive)."`
	// NewFuncName names the function produced by extract_function.
	NewFuncName string `json:"new_func_name,omitempty" jsonschema:"For extract_function: name for the newly extracted function. Example: 'validatePayload' or 'computeTotal'."`
	// Receiver is the method receiver to attach during extract_function (e.g. "e
	// *Engine"). Empty extracts a free function.
	Receiver string `json:"receiver,omitempty" jsonschema:"For extract_function: method receiver. Example: 'e *Engine'. Leave empty to extract as a free function."`
	// TargetFile is the destination file for extract_function, extract_interface,
	// and move_symbol. Created when it does not yet exist.
	TargetFile string `json:"target_file,omitempty" jsonschema:"For extract_function, extract_interface, move_symbol: destination file. Example: 'pkg/core/helpers.go'. Created if it doesn't exist."`

	// extract_interface
	NewInterfaceName string `json:"new_interface_name,omitempty" jsonschema:"For extract_interface: name for the generated interface. Example: 'UserRepository'. All exported methods of the target struct become interface methods."`

	// move_package
	SourcePackage string `json:"source_package,omitempty" jsonschema:"For move_package: source import path to move from. Example: 'internal/old'. All imports project-wide are rewritten."`
	// DestPackage is the destination import path for move_package. The directory
	// is created and every importer is rewritten.
	DestPackage string `json:"dest_package,omitempty" jsonschema:"For move_package: destination import path to move to. Example: 'internal/new'. The directory is created and all references updated."`

	// move_symbols
	Moves []Move `json:"moves,omitempty" jsonschema:"For move_symbols: list of (symbol, target_file) pairs. All moves stage into one transaction with a single build verification — substantially faster than N sequential move_symbol calls when redistributing many symbols."`

	// extract_variable
	VariableName string `json:"variable_name,omitempty" jsonschema:"For extract_variable: name for the new local variable. Example: 'isEligible' or 'totalCost'."`
	// StartCol is the 1-based start column of the expression to extract for
	// extract_variable.
	StartCol int `json:"start_col,omitempty" jsonschema:"For extract_variable: 1-based start column of the expression to extract. Combined with Line to pinpoint the expression."`
	// EndCol is the 1-based end column of the expression to extract for
	// extract_variable. When zero, the tool auto-detects an interesting expression
	// on Line.
	EndCol int `json:"end_col,omitempty" jsonschema:"For extract_variable: 1-based end column of the expression to extract. If omitted, auto-detects the interesting expression on that line (if-condition, assignment RHS, return value)."`

	// change_type
	NewTypeDefinition string `json:"new_type_definition,omitempty" jsonschema:"For change_type: the new type definition as Go source. Example: 'interface { Evaluate(Snapshot) Result }'. The tool replaces the old type and updates all usage sites."`
	// MethodMapping maps old usage patterns to new method calls during
	// change_type. The special key "__call__" rewrites direct invocations into
	// method calls.
	MethodMapping map[string]string `json:"method_mapping,omitempty" jsonschema:"For change_type: maps old usage patterns to new method calls. Key '__call__' maps direct function invocations to a method name. Example: {'__call__': 'Evaluate'} rewrites edge(args) to edge.Evaluate(args)."`

	// add_tests
	Tests []TestSpec `json:"tests,omitempty" jsonschema:"For add_tests: test function specs to generate."`
	// After names the function to insert generated tests after (the "Test" prefix
	// is added automatically). Empty appends to the end of the file.
	After string `json:"after,omitempty" jsonschema:"For add_tests: insert after this function name ('Test' prefix added if missing). Appends to end when empty."`

	// document
	Comments []DocumentComment `json:"comments,omitempty" jsonschema:"For document: doc comments to set, one per symbol (single-file mode)."`
	// DocumentFiles is the multi-file batch form of the document action; each
	// entry pairs a file path with comment specs.
	DocumentFiles []DocumentFile `json:"document_files,omitempty" jsonschema:"For document: multi-file batch."`
	// Mode controls existing-comment handling for the document action: "replace"
	// (default) or "skip_existing".
	Mode string `json:"mode,omitempty" jsonschema:"For document: 'replace' (default) or 'skip_existing'."`
	// MaxLineLength wraps doc prose at this column width. Zero uses the built-in
	// default (80).
	MaxLineLength int `json:"max_line_length,omitempty" jsonschema:"For document: wrap prose at this column width. 0 = use default (80). Combine with NoWrap=true to disable wrapping."`
	// NoWrap disables line wrapping for the document action.
	NoWrap bool `json:"no_wrap,omitempty" jsonschema:"For document: skip line wrapping entirely."`
	// StrictPrefix requires each doc comment to begin with the symbol name,
	// matching the godoc convention.
	StrictPrefix bool `json:"strict_prefix,omitempty" jsonschema:"For document: require docs to begin with the symbol name."`
	// ListMissing reports exported symbols that currently lack a doc comment
	// instead of writing any.
	ListMissing bool `json:"list_missing,omitempty" jsonschema:"For document: report exported symbols that lack a doc comment."`
}

// DocumentComment is a single (symbol, doc) pair for the document action.
// Symbol identifies which declaration's doc comment to replace; Doc is the bare
// prose text that the framework wraps and prefixes with godoc-style `// `
// markers.
//
// Symbol accepts the same forms as docFindTarget: a top-level identifier
// ("NewUser"), a method ("Engine.Run"), a struct field ("Config.Timeout"), or a
// single value inside a multi-spec const/var block. Doc must NOT include
// leading `//` markers — the formatter adds them. Leading whitespace on a line
// marks it as a godoc preformatted block and is preserved verbatim.
type DocumentComment struct {
	// Symbol is the identifier to document, optionally qualified as "Type.Field"
	// or "Type.Method".
	Symbol string `json:"symbol"`
	// Doc is the comment body to set, without leading slashes or a trailing
	// newline.
	Doc string `json:"doc"`
}

// DocumentFile is one file in a multi-file document batch — a module-relative
// file path paired with the per-symbol Comments to apply within it. Multi-file
// batches commit atomically through a single build-gate run, which is
// materially faster than N single-file calls when refreshing docs across a
// package.
type DocumentFile struct {
	// File is the module-relative path of the file whose comments are being set.
	File string `json:"file"`
	// Comments lists the symbol/doc pairs to apply within File.
	Comments []DocumentComment `json:"comments"`
}

// Move is one entry in a move_symbols batch: a symbol identifier and the
// destination file it should land in. Optional File pins the source when the
// symbol name appears in multiple files; left empty, the workspace's type info
// locates the source automatically.
//
// Symbol uses the same form as move_symbol's symbol field — a top-level
// identifier ("ProcessOrder") or a method spelled "Type.Method". TargetFile
// must live in the same package directory as the source; cross-package moves
// are not supported by move_symbol and should be expressed as move_package or
// move_file instead.
type Move struct {
	// Symbol is the identifier to move, in the same form accepted by move_symbol
	// (top-level name or "Type.Method").
	Symbol string `json:"symbol" jsonschema:"Symbol to move. Same form as move_symbol's symbol — a top-level identifier or 'Type.Method'."`
	// TargetFile is the destination file within the same package directory.
	// Created when missing.
	TargetFile string `json:"target_file" jsonschema:"Destination file. Must live in the same package directory as the source. Created if missing."`
	// File optionally pins the source file. When empty the workspace's type
	// information locates the symbol.
	File string `json:"file,omitempty" jsonschema:"Optional source file hint. If empty, the source file is located via the workspace's type info."`
}

// TestSpec describes one test function to scaffold for the add_tests action.
// Name accepts either an already-prefixed identifier ("TestHandler",
// "BenchmarkParse", "FuzzDecode") or a bare name ("Handler") to which the
// "Test" prefix is added automatically.
//
// Subtests, when non-empty, become `t.Run(...)` calls inside the generated
// function — each pre-wired with t.Parallel() and t.Skip("not implemented") so
// the file compiles and runs immediately. An empty Subtests slice emits a flat
// test body with the same Parallel/Skip pair. This matches the project's test
// conventions: every test is parallel by default and explicitly skipped until
// implementation lands.
type TestSpec struct {
	// Name is the test function name. The "Test" prefix is added automatically
	// when missing.
	Name string `json:"name"`
	// Subtests lists subtest names to generate inside the function, each wired
	// with t.Parallel() and t.Skip placeholders.
	Subtests []string `json:"subtests,omitempty"`
}

// AddParameter describes one parameter to add during change_signature. Name is
// the Go identifier inserted into the function definition; Type is the
// parameter type written as valid Go source; DefaultValue is the expression
// injected literally at every existing call site so the call continues to
// compile.
//
// All three fields are required. The tool does NOT validate that Type or
// DefaultValue parse — invalid input is caught by the build gate on commit,
// with rollback. This is intentional: a strict pre-parse would have to mirror
// the full Go grammar, and the build gate is the source of truth anyway.
type AddParameter struct {
	// Name is the Go identifier for the new parameter.
	Name string `json:"name" jsonschema:"Go identifier for the new parameter. Example: 'ctx' or 'opts'."`
	// Type is the parameter type written as valid Go source (e.g.
	// "context.Context").
	Type string `json:"type" jsonschema:"Parameter type as valid Go source. Example: 'context.Context' or '*http.Request'."`
	// DefaultValue is the expression inserted at every existing call site.
	DefaultValue string `json:"default_value" jsonschema:"Value injected literally at all existing call sites. Example: 'context.TODO()' or 'nil'."`
}

// AddReturn describes one return value to add during change_signature. Type is
// the new return type written as valid Go source; DefaultValue is the
// assignment expression injected at every existing call site to capture (or
// discard) the new value.
//
// Using "_" as DefaultValue discards the new return at all call sites — useful
// when the caller doesn't yet care about the value (e.g., adding an error
// return that's intentionally ignored until the next refactor). Using an
// identifier name (e.g., "err") captures it into a freshly-declared variable.
// The build gate catches mismatches like "using a captured err but never
// inspecting it" via the workspace's lint suite.
type AddReturn struct {
	// Type is the return type written as valid Go source.
	Type string `json:"type" jsonschema:"Return type as valid Go source. Example: 'error' or '*Response'."`
	// DefaultValue is the assignment expression used at every call site (e.g. "_"
	// to discard or "err" to capture).
	DefaultValue string `json:"default_value" jsonschema:"Assignment expression injected at all call sites. Example: '_' for blank discard or 'err' to capture."`
}

// Output is the structured result of a lang.go.refactor operation, returned by
// [Handle] regardless of whether the underlying strategy succeeded or rolled
// back.
//
// Field interplay:
//
//   - Status / BuildStatus: Status is the overall outcome ([StatusSuccess] or
//     [StatusFailure]); BuildStatus is whether `go build` succeeded under the
//     workspace. A failure with BuildStatus "fail" means the refactor was
//     rolled back; a failure with BuildStatus "pass" means an action returned
//     an error before staging any change.
//   - Results: one entry per file the action touched (or attempted to). On a
//     successful refactor each entry's Status is [StatusSuccess] (or
//     [StatusSkipped] for files intentionally left alone). On failure, only the
//     file that triggered the build break carries an Error; the rest were
//     rolled back transparently.
//   - VerifyOutput / VerificationStatus: present only when the caller requested
//     auto-verify. They are diagnostic — failures here do NOT roll back the
//     refactor.
//   - NextActions: pre-computed follow-up tool calls (typically a
//     lang.go.verify pointer). High-confidence entries are safe to execute
//     autonomously.
//   - Notes: advisory messages the framework cannot perform itself (e.g.,
//     asking the user to run `go mod tidy` after a cross-module move). Surface
//     verbatim.
type Output struct {
	// Status is the overall outcome: StatusSuccess when every modified file was
	// committed, StatusFailure when any change was rolled back.
	Status string `json:"status" jsonschema:"Overall outcome: 'success' if all files modified cleanly, 'failure' if any file was rolled back."`
	// FilesModified counts files that were modified and passed verification.
	FilesModified int `json:"files_modified" jsonschema:"Number of files successfully modified and verified."`
	// FilesFailed counts files whose changes were rolled back.
	FilesFailed int `json:"files_failed" jsonschema:"Number of files that failed verification and were rolled back to original state."`
	// BuildStatus is "pass" when the module builds after the refactor, "fail" when
	// the refactor was rolled back.
	BuildStatus string `json:"build_status" jsonschema:"'pass': entire module builds after refactor. 'fail': refactor was rolled back — all files restored to original state."`
	// Results holds per-file diff receipts and status entries.
	Results []FileResult `json:"results" jsonschema:"Per-file results with diff receipts. AGENT HINT: Check each result's Status. If all success, call lang.go.verify to confirm. If any failure, the refactor was rolled back — read the Error field."`
	// VerifyOutput carries verification results when AutoVerify was requested.
	VerifyOutput *lang.VerifyOutput `json:"verify_output,omitempty" jsonschema:"Verification results when auto_verify was used. Present regardless of pass/fail."`
	// VerificationStatus summarises the verify run: "verified", "lint_ok",
	// "unverified", or "degraded".
	VerificationStatus string `json:"verification_status,omitempty" jsonschema:"'verified': test suite passed. 'lint_ok': lint passed, logic untested. 'unverified': no verification. 'degraded': issues found."`
	// NextActions lists suggested follow-up tool calls with confidence labels.
	NextActions []lang.NextAction `json:"next_actions,omitempty" jsonschema:"Pre-computed follow-up tool calls. AGENT HINT: Execute deterministic/high-confidence actions without review. Review medium/low confidence before acting."`
	// Notes carries advisory messages about side effects the refactor cannot
	// perform itself (for example, asking the user to run "go mod tidy"). Surface
	// these verbatim.
	Notes []string `json:"notes,omitempty" jsonschema:"Advisory messages for the user about side effects the refactor cannot perform itself. Example: a cross-module move asks the user to run 'go mod tidy' in the affected modules. Surface these to the user verbatim."`
}

// FileResult carries the per-file outcome of a refactor operation. Surfaced
// inside Output.Results.
//
// Status is one of [StatusSuccess], [StatusFailure], or [StatusSkipped]. On a
// successful refactor every modified file is [StatusSuccess] (rolled-back files
// would have prevented Status from being [StatusSuccess] in the parent Output).
// DiffSnippet is a unified diff of the key changes — produced from the
// in-memory before/after, so it reflects the post-goimports content. Error is
// populated only when Status is [StatusFailure].
type FileResult struct {
	// FilePath is the module-relative path of the affected file.
	FilePath string `json:"file_path" jsonschema:"Path of the modified file relative to the module root."`
	// Status is the per-file outcome: StatusSuccess, StatusFailure, or
	// StatusSkipped.
	Status string `json:"status" jsonschema:"Outcome for this file: 'success', 'failure' (rolled back), or 'skipped' (no changes needed)."`
	// DiffSnippet shows the key changed lines in unified diff format.
	DiffSnippet string `json:"diff_snippet,omitempty" jsonschema:"Key changed lines in unified diff format. AGENT HINT: Review to verify the AST rewrite was correct before proceeding."`
	// Message is a human-readable summary of what changed in the file.
	Message string `json:"message,omitempty" jsonschema:"Human-readable summary of what changed in this file."`
	// Error carries diagnostic detail when Status is StatusFailure.
	Error string `json:"error,omitempty" jsonschema:"Diagnostic details for failed files. AGENT HINT: If the error mentions 'undefined', the refactor missed a reference — report to the user."`
}
