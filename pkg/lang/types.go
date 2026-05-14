// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package lang provides shared types and the Engine for language toolchain tools.
package lang

// Confidence levels for NextAction — how safe it is to execute blindly.
const (
	// ConfidenceDeterministic marks a [NextAction] as mechanically correct
	// and safe to execute without any human or agent review. Reserved for
	// actions whose result is independent of context (gofmt, goimports,
	// rearranging imports). Agents may chain these in batches.
	ConfidenceDeterministic = "deterministic" // Mechanically correct (formatting, import cleanup)
	// ConfidenceHigh marks a [NextAction] as very likely correct given the
	// upstream diagnostic — e.g. a golangci-lint auto-fix patch or a
	// structural-analysis-derived rewrite. Agents usually execute these
	// without further investigation but should review on the rare
	// failure.
	ConfidenceHigh = "high" // Very likely correct (lint auto-fixes, structural analysis)
	// ConfidenceMedium marks a [NextAction] whose suggestion was inferred
	// from heuristics that have known false-positive rates. Review before
	// executing; the NextAction.RiskDescription field explains the
	// specific failure modes to look for.
	ConfidenceMedium = "medium" // Review before executing (inferred fixes)
	// ConfidenceLow marks a [NextAction] tied to a complex failure (a
	// failing test, an architectural smell) where the suggested fix is
	// one plausible interpretation among several. Agents should explore
	// the problem first and confirm the diagnosis before applying.
	ConfidenceLow = "low" // Explore first, understand the problem (complex failures)
)

// Explore modes — control verbosity of symbol exploration.
const (
	// ModeDocs selects the cheapest explore output: symbol names plus
	// their doc comments, no signatures or bodies. Roughly 50 tokens per
	// symbol. Use for high-level API inventories where prose docs are
	// the most useful per byte.
	ModeDocs = "docs" // Names + docblocks only (cheapest)
	// ModeSkeleton returns signatures and struct/interface fields without
	// function bodies — roughly 150 tokens per symbol. The default for
	// whole-package exploration: enough to understand the API surface,
	// far cheaper than the full source.
	ModeSkeleton = "skeleton" // Signatures + fields, no bodies
	// ModeCode returns the full source of each symbol, suitable for use
	// verbatim as the OldString of a patch edit. Highest token cost
	// (roughly 500+ tokens per symbol). The default when specific symbols
	// are requested by name.
	ModeCode = "code" // Full source implementation
	// ModeOverview is the lightest mode: returns package_doc + symbol
	// order + a one-line summary per exported symbol. Roughly 500 tokens
	// for a typical 20-symbol package, an order of magnitude cheaper
	// than [ModeDocs]. Use as the first call when scanning a package
	// before deciding which symbols to drill into with skeleton or code
	// mode — it skips Location, Signature, Docblock, Implementation,
	// Methods, Fields, Receiver, and ReproCommand entirely.
	ModeOverview = "overview" // One-line summary per symbol (cheapest)
)

// Symbol kinds returned by explore and search tools.
const (
	// KindAll is the wildcard symbol-kind filter — used in [SearchInput]
	// and [ExploreInput] to disable kind filtering entirely. Equivalent
	// to omitting Kind.
	KindAll = "all"
	// KindFunc filters search and explore results to top-level functions
	// (no receiver).
	KindFunc = "func"
	// KindMethod filters search and explore results to methods —
	// functions with a receiver. Use [KindFunc] for receiver-less
	// functions.
	KindMethod = "method"
	// KindStruct filters search and explore results to `type X struct`
	// declarations. Excludes other named types (use [KindType] for those).
	KindStruct = "struct"
	// KindInterface filters search and explore results to `type X
	// interface` declarations. Useful when looking for abstraction
	// boundaries to extend or implement.
	KindInterface = "interface"
	// KindType filters search and explore results to named type
	// declarations that are neither struct nor interface — aliases
	// (`type Foo = Bar`), defined types (`type Foo Bar`), and named
	// function types.
	KindType = "type"
	// KindConst filters search and explore results to constant
	// declarations, including individual specs inside grouped const
	// blocks.
	KindConst = "const"
	// KindVar filters search and explore results to package-level
	// variable declarations. Function-local vars are not searchable
	// via these tools.
	KindVar = "var"
)

// Suite names for the verify tool.
const (
	// SuiteLint identifies the static-analysis quality gate (golangci-lint
	// and related analysers). The lowest-cost suite and the one that
	// reliably produces auto-applicable [SuggestedPatch] entries.
	SuiteLint = "lint"
	// SuiteTest identifies the unit- and integration-test quality gate
	// run via `go test`. Slower than SuiteLint and rarely produces
	// auto-fix patches, but the only gate that catches semantic bugs.
	SuiteTest = "test"
	// SuiteBench identifies the benchmark suite run via `go test -bench`.
	// Reports [Metric] entries with ns/op, allocs/op, bytes/op, and
	// optionally heap-escape analysis.
	SuiteBench = "bench"
	// SuiteFuzz identifies the fuzz suite run via `go test -fuzz` for a
	// bounded duration. Useful for surface-level fuzzing of parsers and
	// decoders; not a substitute for a long-running fuzz farm.
	SuiteFuzz = "fuzz"
)

// Status values for verification results.
const (
	// StatusPass indicates every gate that ran completed successfully
	// with no diagnostic findings. The strongest signal that the change
	// is ready to merge.
	StatusPass = "pass"
	// StatusLintOK indicates the lint suite passed but no test suite
	// ran, so behaviour is unverified. A weaker outcome than StatusPass:
	// the code compiles and is stylistically clean but may still be
	// broken at runtime.
	StatusLintOK = "lint_ok" // Lint passed but logic is untested — not a full "pass".
	// StatusFail indicates one or more checks reported actionable
	// issues. Distinct from StatusError (which is an infrastructure
	// failure) — StatusFail means the tooling ran successfully and found
	// problems in the code.
	StatusFail = "fail"
	// StatusDegraded indicates a partial failure: some suites passed and
	// others failed, but the run completed. Used as the rolled-up
	// VerifyOutput.OverallStatus when any individual suite reports
	// StatusFail or StatusError.
	StatusDegraded = "degraded"
	// StatusError indicates an infrastructure or tool-invocation
	// failure that prevented the run from completing — the linter binary
	// is missing, the workspace failed to build at all, or the suite
	// crashed before producing output. Distinct from StatusFail.
	StatusError = "error"
)

// Issue severity levels.
const (
	// SeverityError marks an [Issue] as a hard failure that should
	// block merging. Linters that classify into severity levels typically
	// use this for compile-blocking issues.
	SeverityError = "error"
	// SeverityWarning marks an [Issue] as a non-blocking problem worth
	// fixing — style violations, opportunities for improvement that do
	// not break correctness.
	SeverityWarning = "warning"
	// SeverityInfo marks an [Issue] as informational — surfaced for
	// awareness, not action. Typically used for analyser hints that
	// require human judgement before acting.
	SeverityInfo = "info"
)

// Blast radius risk levels.
const (
	// RiskLow indicates a change with limited blast radius — few
	// callers, no public API impact, no behaviour change. Safe to ship
	// without extensive review.
	RiskLow = "low"
	// RiskMedium indicates a change touching several packages or a
	// moderately-used symbol. Standard review process applies.
	RiskMedium = "medium"
	// RiskHigh indicates a change affecting many callers or a widely-
	// used public API. Warrants a focused review and broader test
	// coverage.
	RiskHigh = "high"
	// RiskCritical indicates a change touching core abstractions whose
	// breakage cascades across most of the codebase (the database
	// adapter, the auth layer, the public service interface). Requires
	// cross-team review and a verified rollback plan.
	RiskCritical = "critical"
)

// Dependency relationship kinds.
const (
	// RelDirect denotes a first-degree dependency between symbols — one
	// directly calls or references the other. The simplest and most
	// common dependency kind reported by [DepReference].
	RelDirect = "direct"
	// RelTransitive denotes a dependency reached through one or more
	// intermediaries in the call or reference graph. Surfaced when
	// DepsInput.Depth is greater than 1.
	RelTransitive = "transitive"
	// RelInterface denotes a dependency mediated by an interface: the
	// relationship holds because the source dispatches through an
	// interface that the target satisfies. Statically invisible to a
	// grep-based search.
	RelInterface = "interface"
	// RelDirectCaller denotes a function that calls the target symbol
	// directly. Reported by [CallersInput]; the most common deps
	// relationship.
	RelDirectCaller = "direct_caller"
	// RelTransitiveCaller denotes a function that reaches the target
	// symbol through one or more intermediate callers in the call graph.
	RelTransitiveCaller = "transitive_caller"
	// RelTypeUser denotes a symbol that uses the target type in its
	// signature, fields, or local declarations. Reported when the deps
	// query targets a type rather than a function.
	RelTypeUser = "type_user"
	// RelImplementor denotes a concrete type whose method set satisfies
	// the target interface. Reported by [ImplementationsInput].
	RelImplementor = "implementor"
)

// Dependency include types for DepsInput.
const (
	// IncludeImplementations selects implementation tracing in a
	// [DepsInput]: concrete types satisfying the target interface.
	IncludeImplementations = "implementations"
	// IncludeCallers selects call-site tracing in a [DepsInput]:
	// functions and methods that invoke the target symbol.
	IncludeCallers = "callers"
	// IncludeReferences selects reference tracing in a [DepsInput]:
	// every site that reads, writes, or names the target — including
	// non-call uses.
	IncludeReferences = "references"
	// IncludeInvocations selects invocation tracing in a [DepsInput]:
	// call sites that invoke a function-typed value bound to the target.
	// Distinct from [IncludeCallers], which matches on function name.
	IncludeInvocations = "invocations"
)

// NextAction is a structured follow-up tool-call suggestion that a
// lang.* tool attaches to its response so the agent can chain the
// logical next step without composing a new request from scratch.
//
// The pattern is load-bearing for token efficiency: every tool that
// returns a [VerifyOutput], [ExploreOutput], [SearchOutput], or
// [DepsResult] may include up to [MaxNextActions] suggestions ranked
// by diagnostic value. Suggestions carry their own pre-filled Input
// payload so high-confidence cases can be relayed verbatim; medium
// and low-confidence cases include a RiskDescription so the agent
// knows what to verify before executing.
//
// NextAction is purely advisory — agents are free to ignore it. It
// exists because LLMs frequently choose suboptimal follow-up tools
// (grep instead of references, Edit instead of patch), and embedding
// the correct next step in the tool response halves the time-to-fix
// for multi-turn workflows.
type NextAction struct {
	// Tool is the dotted tool name to invoke next, e.g. `lang.go.explore`
	// or `fs.patch`. The string is matched against the framework's tool
	// registry; an unrecognised name produces an error when the agent
	// attempts to dispatch.
	Tool string `json:"tool" jsonschema:"Dotted tool name, e.g. fs.patch or lang.go.explore"`
	// Reason is a short human-readable explanation of why this follow-up
	// is being suggested. Used as the rationale field on chained tool
	// calls and surfaced in the TUI/CLI presenters.
	Reason string `json:"reason" jsonschema:"Why this action is suggested"`
	// Input is a pre-filled payload that satisfies the suggested tool's
	// input schema. For [ConfidenceDeterministic] and [ConfidenceHigh]
	// actions the agent should pass it through unchanged; for lower
	// confidences the agent may want to inspect or modify fields first.
	// Typed as `any` because the concrete type varies per Tool.
	Input any `json:"input,omitempty" jsonschema:"Pre-filled input for the suggested tool call. AGENT HINT: Pass this directly as the tool's input — no modification needed for deterministic/high confidence actions."`
	// Confidence is one of [ConfidenceDeterministic], [ConfidenceHigh],
	// [ConfidenceMedium], or [ConfidenceLow] and drives whether agents
	// should run the action immediately or review it first. Tools that
	// emit multiple NextActions are sorted by [CapNextActions] so the
	// most useful suggestions land at the top of the returned slice.
	Confidence string `json:"confidence" jsonschema:"Execution guidance. 'deterministic': safe to execute blindly (formatting). 'high': very likely correct (lint fixes, structural analysis). 'medium': review before executing. 'low': explore first, understand the problem."`
	// RiskDescription is a human-readable description of what could go
	// wrong if the action is executed without review. Populated only for
	// [ConfidenceMedium] and [ConfidenceLow] suggestions — for higher
	// confidences it is left empty to save tokens.
	RiskDescription string `json:"risk_description,omitempty" jsonschema:"What could go wrong if executed without review. Only present for medium/low confidence actions."`
}

// ExploreInput is the agent-facing request for lang.go.explore — the
// workhorse symbol-inspection tool. Explore returns per-symbol
// metadata for a Go package, with output verbosity controlled by
// the Mode field and a token budget enforced by MaxOutputTokens.
//
// Explore replaces the agent's instinct to `Read` whole files. A
// [ModeSkeleton] explore of a single package returns ~150 tokens per
// symbol versus ~10x more for a full file Read. With Symbols set, the
// agent can fetch just the implementations it needs to patch — far
// cheaper than reading the whole file and far more reliable, because
// the returned Implementation matches the file's AST exactly and
// works as a verbatim OldString for [PatchEdit].
//
// When Symbols is empty the tool inventories every exported symbol
// in Package (or every symbol when IncludePrivate is true) and
// defaults Mode to [ModeSkeleton]. When Symbols is set Mode defaults
// to [ModeCode] so the agent gets the implementations it asked for.
type ExploreInput struct {
	// Package is the Go import path or workspace-relative directory to
	// explore. Accepts `core/ledger`, `./pkg/fs`, or an absolute module
	// path. Matches the Package value returned by [SearchResult] —
	// pass it through unchanged to avoid lookup ambiguity.
	Package string `json:"package" jsonschema:"Go import path relative to module root. Example: 'core/ledger' or './pkg/fs'. AGENT HINT: Use the package field from lang.go.search results directly."`
	// Symbols restricts the explore to specific named declarations.
	// When empty every exported symbol in Package is returned. Method
	// names are written `Receiver.Method` (e.g. `Engine.Execute`).
	// Unknown names produce per-symbol errors but do not abort the
	// call.
	Symbols []string `json:"symbols,omitempty" jsonschema:"Specific identifiers to extract. If empty, returns all exported symbols. AGENT HINT: Pass symbol names from lang.go.search results directly to this field."`
	// Mode controls output verbosity: [ModeDocs] (cheapest, names plus
	// docblocks), [ModeSkeleton] (signatures plus fields, no bodies),
	// or [ModeCode] (full source). Defaults to [ModeCode] when Symbols
	// is set and to [ModeSkeleton] otherwise — the choice that
	// minimises tokens for the typical use case in each mode.
	Mode string `json:"mode,omitempty" jsonschema:"Controls output verbosity and token cost. 'docs': names+docblocks only (~50 tokens/symbol). 'skeleton': signatures+fields (~150 tokens/symbol). 'code': full source (~500+ tokens/symbol). Default: 'code' when symbols specified, 'skeleton' for whole-package discovery."`
	// IncludePrivate adds unexported (lowercase-leading) symbols to the
	// output. Default false. Enable when the agent is editing inside
	// the package itself, not when navigating its public API.
	IncludePrivate bool `json:"include_private,omitempty" jsonschema:"Include unexported symbols"`
	// Kind restricts results to one symbol kind — see [KindFunc],
	// [KindStruct], [KindInterface], etc. Combine with NamePrefix or
	// NameSuffix to inventory subsets like "all funcs starting with
	// Test". Empty means no kind filter.
	Kind string `json:"kind,omitempty" jsonschema:"Filter results by symbol kind: func, method, struct, interface, type, const, var, all. Combine with name_prefix to inventory subsets ('all funcs starting with Test')."`
	// NamePrefix keeps only symbols whose name begins with this string.
	// Useful for listing tests (`Test`), constructors (`New`), or any
	// naming-convention cluster.
	NamePrefix string `json:"name_prefix,omitempty" jsonschema:"Filter to symbols whose name starts with this string. Example: 'Test' to list test functions."`
	// NameSuffix keeps only symbols whose name ends with this string.
	// Useful for finding types by suffix convention (`Action`,
	// `Error`, `Input`).
	NameSuffix string `json:"name_suffix,omitempty" jsonschema:"Filter to symbols whose name ends with this string. Example: 'Action' to list action types."`
	// MaxOutputTokens is the upper bound on response size in tokens.
	// When exceeded [ApplyTokenBudget] truncates Implementation bodies
	// first (preserving signatures and docblocks) and sets Truncated on
	// the response. Check the Truncated flag and re-call with explicit
	// Symbols to retrieve omitted bodies.
	MaxOutputTokens int `json:"max_output_tokens,omitempty" jsonschema:"Limits result size. When exceeded, implementation bodies are truncated first, preserving signatures and docblocks. Check the 'truncated' field in output to decide if a follow-up call with specific symbols is needed."`
	// StripComments removes inline comments from returned
	// Implementations. Saves roughly 20–30% of tokens when the agent
	// needs the logic but not the prose; the symbol's own docblock is
	// still returned separately in the Docblock field of each
	// [SymbolMetadata].
	StripComments bool `json:"strip_comments,omitempty" jsonschema:"Remove comments from returned implementations. Saves ~20-30% tokens when analyzing logic, not documentation. Doc comments on the symbol itself are preserved in the docblock field."`
	// Minify collapses blank lines and normalises indentation in
	// returned Implementations. Combines with StripComments for the
	// smallest possible payload. Whitespace-sensitive patch matching
	// will no longer work against the returned source — use only when
	// reading, not editing.
	Minify bool `json:"minify,omitempty" jsonschema:"Collapse blank lines and normalize indentation in returned implementations. Combine with strip_comments for maximum token savings."`
	// ProductionOnly excludes test code from the response: `*_test.go`
	// files are skipped, packages whose name ends in `_test` are
	// dropped, the synthetic test-augmented variants from
	// packages.Load are collapsed into the production package, and
	// symbols whose name matches Test/Benchmark/Fuzz/Example are
	// filtered out. Default false (returns everything). Set true when
	// an agent is scanning a package's production surface and the
	// interleaved tests would just inflate the payload.
	ProductionOnly bool `json:"production_only,omitempty" jsonschema:"Exclude *_test.go files, packages named *_test, and Test/Benchmark/Fuzz/Example symbols. Default: false. Set true to get the production-only API surface without test scaffolding noise."`
}

// ExploreOutput is the structured response from lang.go.explore. It
// combines package-level metadata (Files, Imports, PackageDoc) with
// a per-symbol map and a declaration-order index, so callers can
// either look symbols up by name or iterate them in author-intended
// order.
//
// The shape is designed for agent consumption: SymbolOrder preserves
// the reading sequence the human author chose (typically
// top-of-file types are the most important), Truncated signals when
// the token budget cut the response, and NextActions points to the
// cheap follow-up call (typically a targeted re-fetch in [ModeCode])
// that would recover the dropped detail.
type ExploreOutput struct {
	// Package is the import path that was explored, echoed from the
	// request's Package field for self-describing responses.
	Package string `json:"package" jsonschema:"Package path that was explored"`
	// WorkspaceVersion is the [WorkspaceVersion] fingerprint of the
	// module source at query time. Compare across turns — if it has
	// changed, prior results may reflect stale source. Format:
	// `mtime:<unix-seconds>`.
	WorkspaceVersion string `json:"workspace_version,omitempty" jsonschema:"Fingerprint of the module source at query time. Compare across turns — if it changed, prior results may be stale."`
	// PackageDoc is the doc comment attached to the package clause (the
	// first non-import comment in a file declaring `package foo`).
	// Empty when the package is undocumented.
	PackageDoc string `json:"package_doc,omitempty" jsonschema:"Package-level doc comment"`
	// Files lists the Go source files comprising the explored package,
	// relative to the module root. Useful for the agent to learn the
	// package's physical layout before patching.
	Files []string `json:"files" jsonschema:"Files in the package"`
	// Imports lists the packages imported by the explored package. When
	// the explore was narrowed to specific Symbols this is further
	// narrowed to imports referenced by those symbols, saving tokens
	// when the package is large but the slice of interest is small.
	Imports []string `json:"imports" jsonschema:"Packages imported by this package. When specific symbols are requested, only imports referenced by those symbols are returned to save tokens."`
	// Symbols is a map keyed by identifier name (or `Receiver.Method`
	// for methods) carrying per-symbol metadata. Iterate in SymbolOrder
	// for deterministic ordering; look up by name to drill into a
	// specific declaration.
	Symbols map[string]SymbolMetadata `json:"symbols" jsonschema:"Map of symbol name to metadata. AGENT HINT: Use symbol names as keys to extract specific metadata for patching or analysis."`
	// SymbolOrder is the symbol names in declaration order across the
	// package's files. Preserves the author's intended reading sequence,
	// which typically clusters related types together — the highest-
	// value iteration order for an agent reviewing unfamiliar code.
	SymbolOrder []string `json:"symbol_order" jsonschema:"Declaration order — preserves the author's intended reading sequence. Use this order when reviewing unfamiliar code."`
	// Truncated reports whether the response was cut to fit the
	// request's MaxOutputTokens budget. When true, follow the
	// NextActions hint to re-fetch the dropped Implementations with a
	// targeted call.
	Truncated bool `json:"truncated,omitempty" jsonschema:"True if output was cut to fit token budget. If true, re-call with specific symbol names in the 'symbols' field to get full implementations."`
	// NextActions carries up to [MaxNextActions] follow-up tool-call
	// suggestions — typically a targeted re-fetch in [ModeCode] for
	// truncated symbols, or a [CallersInput] / [ReferencesInput] on an
	// interesting type.
	NextActions []NextAction `json:"next_actions,omitempty" jsonschema:"Suggested follow-up tool calls"`
}

// SymbolMetadata describes a single Go declaration as returned by
// lang.go.explore. It carries enough information to identify the
// symbol (Kind, Location), display its signature, and either inline
// its source ([ModeCode]) or summarise its shape ([ModeSkeleton]).
//
// The Implementation field is the load-bearing one for refactoring:
// it is the exact source text gopls saw at query time, suitable for
// use as the OldString of a [PatchEdit] without any whitespace
// guesswork. Pass it through unchanged when the agent's next step
// is a patch.
type SymbolMetadata struct {
	// Kind is the declaration kind: one of [KindFunc], [KindMethod],
	// [KindStruct], [KindInterface], [KindType], [KindConst], or
	// [KindVar]. Drives which other fields are populated
	// (Fields/Methods for structs and interfaces, Receiver for methods,
	// etc.).
	Kind string `json:"kind" jsonschema:"func, struct, interface, type, const, var, method"`
	// Location is the declaration site in `file:line` form, relative to
	// the module root. Pass through to [VerifyInput] via the Focus
	// field to scope tests, or use as a starting point for a
	// [PatchEdit].
	Location string `json:"location" jsonschema:"file:line format. AGENT HINT: Use this to construct targeted verify --focus patterns."`
	// Signature is the declaration's single-line signature without a
	// body — useful for API mapping when the agent does not need the
	// full implementation. Populated for funcs, methods, and named
	// types; empty for vars and consts.
	Signature string `json:"signature,omitempty" jsonschema:"Function/method signature without body. Use for API mapping without loading full implementations."`
	// Docblock is the doc comment attached to the symbol with leading
	// `//` markers stripped. Empty when the symbol is undocumented.
	// Preserved across all explore modes — comments are the highest-
	// value per byte content for API discovery.
	Docblock string `json:"docblock,omitempty" jsonschema:"Doc comment"`
	// Implementation is the full source of the declaration including
	// receiver, signature, and body. Returned only in [ModeCode] or
	// when targeted Symbols are requested.
	//
	// Matches the file's AST exactly: pass through as the OldString of
	// an [PatchEdit] for guaranteed whitespace alignment. May be
	// emptied by [ApplyTokenBudget] when the response would exceed the
	// caller's MaxOutputTokens budget; check the Truncated flag on the
	// enclosing [ExploreOutput].
	Implementation string `json:"implementation,omitempty" jsonschema:"Full source code of the symbol. AGENT HINT: Use this value with fs.patch old_string for exact matching when preparing edits."`
	// Receiver is the method receiver clause (e.g. `*Engine`) for
	// methods. Empty for non-method symbols. Used by the agent when
	// locating a method to patch.
	Receiver string `json:"receiver,omitempty" jsonschema:"Receiver type for methods"`
	// Fields lists struct fields, populated in [ModeSkeleton] and
	// [ModeCode] only. Empty for non-struct symbols. Each [FieldInfo]
	// carries the field name, type, struct tag, and doc comment.
	Fields []FieldInfo `json:"fields,omitempty" jsonschema:"Struct fields (skeleton+code modes)"`
	// Methods lists method names declared on a struct receiver or
	// required by an interface. Empty for non-type symbols. For
	// interfaces, this is the method set the interface defines; for
	// structs, it is the methods declared in the package (not
	// transitively inherited).
	Methods []string `json:"methods,omitempty" jsonschema:"Method names for structs/interfaces"`
	// ReproCommand is a copy-paste-ready shell command that runs just
	// this target. Populated for `Test*`, `Benchmark*`, and `Fuzz*`
	// functions — e.g. `go test -run ^TestParseArgs$
	// ./pkg/parser/...` — so the agent can reproduce a failure or
	// verify a fix without composing the command from scratch.
	ReproCommand string `json:"repro_command,omitempty" jsonschema:"For Test*/Benchmark*/Fuzz* functions: the exact go test command to run this specific test or benchmark. Copy-paste ready."`
	// OneLineSummary is a single-line description of the symbol —
	// `name(args) returns(...) — first sentence of doc` for funcs,
	// `TypeName struct/interface — first sentence` for types, capped
	// at ~120 characters. Populated only in [ModeOverview] (and by
	// [WorkspaceOutput] when its Detail is full), kept empty in every
	// other mode so the field never inflates payloads when richer
	// fields are already present.
	OneLineSummary string `json:"one_line_summary,omitempty" jsonschema:"Single-line symbol summary used by overview mode. Format: 'name(args) — first sentence of doc'."`
}

// FieldInfo describes one struct field as returned by
// lang.go.explore in [ModeSkeleton] and [ModeCode]. Returned
// inside the Fields slice of a struct [SymbolMetadata].
//
// The shape mirrors the four useful pieces of a Go field
// declaration: the name, the type, the struct tag, and the doc
// comment. Embedded fields are reported with their type name as
// Name (Go's implicit-naming convention).
type FieldInfo struct {
	// Name is the field's declared identifier. For embedded fields
	// this is the embedded type's last path segment, matching how Go
	// resolves implicit field names.
	Name string `json:"name" jsonschema:"Field identifier name"`
	// Type is the field's Go type as written in source — slice, map,
	// pointer, channel, or named-type forms are all preserved verbatim.
	// The value is a literal source fragment, not a canonical form, so
	// type aliases and qualified names appear exactly as in the source
	// file.
	Type string `json:"type" jsonschema:"Go type of the field"`
	// Tag is the raw struct tag string with the surrounding backticks
	// stripped (e.g. `json:"name,omitempty"`). Empty when the field has
	// no tag. Tag content is not interpreted — the agent parses it
	// according to whichever package uses it.
	Tag string `json:"tag,omitempty" jsonschema:"Struct tag string if present"`
	// Doc is the field's doc comment with leading `//` markers stripped.
	// Empty when undocumented. For struct fields, godoc convention is
	// that the doc comment immediately precedes the field declaration
	// (not at end-of-line).
	Doc string `json:"doc,omitempty" jsonschema:"Field doc comment if present"`
}

// Detail levels for verify and dependency-tracing tools.
const (
	// DetailSummary requests the smallest possible verify or deps
	// response: aggregate status plus issue counts, with no per-issue
	// details. Typically ~80% smaller than [DetailStandard]. Use for the
	// quick "did anything break?" check at the end of a refactor.
	DetailSummary = "summary"
	// DetailStandard is the default detail level: per-issue file:line
	// entries, code snippets, and suggested patches. The sweet spot for
	// fix-workflow turns where the agent needs enough context to act on
	// a finding.
	DetailStandard = "standard"
	// DetailFull adds AST forensics and the affected function's body to
	// every issue. Substantially more expensive than [DetailStandard] —
	// reserve for deep debugging or when the issue's surrounding code is
	// load-bearing for understanding the diagnostic.
	DetailFull = "full"
)

// DetailFlags expands a Detail-level string ([DetailSummary],
// [DetailStandard], or [DetailFull]) into the per-feature toggles
// that verify runners and report assemblers consume internally.
//
// The indirection exists because the same Detail value drives several
// independent output choices (snippets, hints, forensics,
// suggested patches, the issue list itself). Resolving once with
// [ResolveDetail] and passing flags around avoids duplicating that
// mapping at every call site.
//
// DetailFlags is plain data — no methods, no shared state — and is
// freely copyable across goroutines.
type DetailFlags struct {
	// Snippets toggles whether per-issue code snippets are included in
	// the response. True at [DetailStandard] and [DetailFull]; false at
	// [DetailSummary].
	Snippets bool // include code snippets in issue output
	// Hints toggles whether per-issue actionable hints (advice aimed at
	// agents) are included. True at [DetailStandard] and [DetailFull];
	// false at [DetailSummary].
	Hints bool // include actionable hints
	// Forensics toggles whether AST-derived implementations of the
	// affected function or struct are attached to each issue. True only
	// at [DetailFull] — the field is the main reason DetailFull is
	// expensive.
	Forensics bool // attach AST implementations of failing symbols
	// SuggestedPatches toggles whether [SuggestedPatch] entries are
	// emitted alongside each issue. True at [DetailStandard] and
	// [DetailFull]; false at [DetailSummary]. Patches are valuable for
	// autonomous fix workflows but add response size.
	SuggestedPatches bool // emit fs.patch-shaped fix patches
	// Issues toggles whether the per-issue list is included at all.
	// False only at [DetailSummary] — the load-bearing flag that produces
	// the ~80% size reduction. When false, only counts and status
	// remain so the agent can decide whether to drill in.
	Issues bool // include the per-issue list at all (false in summary mode)
}

// ResolveDetail returns the [DetailFlags] for a given Detail-level
// string. Unrecognised values (including the empty string) are
// treated as [DetailStandard] — the same default a user-facing tool
// applies when the agent omits the field.
//
// The mapping:
//
//   - [DetailSummary] → Issues=false (everything else false).
//   - [DetailStandard] → Snippets, Hints, SuggestedPatches, Issues all
//     true; Forensics false.
//   - [DetailFull] → all flags true.
//
// ResolveDetail is pure and allocation-free; safe to call on the hot
// path of every assembler.
func ResolveDetail(detail string) DetailFlags {
	switch detail {
	case DetailSummary:
		return DetailFlags{Issues: false}
	case DetailFull:
		return DetailFlags{Snippets: true, Hints: true, Forensics: true, SuggestedPatches: true, Issues: true}
	default: // "" or DetailStandard
		return DetailFlags{Snippets: true, Hints: true, SuggestedPatches: true, Issues: true}
	}
}

// SearchExploreInput drives lang.go.search_explore: a fused
// search-then-explore tool that saves one round-trip when the agent
// needs to find a symbol and then immediately read its source.
//
// Functionally equivalent to running [SearchInput] followed by an
// [ExploreInput] on the top match — and that is exactly what the
// tool does internally — but exposed as a single call so the agent
// does not pay two LLM turns for the common discover-then-read
// pattern. Use SearchExplore when the next action is "open the file";
// use [SearchInput] alone when the next action is "compare a few
// candidates" or "see how many hits there are".
//
// The Pick field lets the agent drill into a non-top match without
// re-running search.
type SearchExploreInput struct {
	// Query is the symbol-name fuzzy match or natural-language
	// description of what to find. Same shape as SearchInput.Query: name
	// queries (`foldPatch`, `HTTPRq`) hit gopls's fuzzy matcher while
	// prose queries (`how backpressure is handled`) hit the doc-content
	// scorer.
	Query string `json:"query" jsonschema:"Symbol name (fuzzy) or natural-language description, same shape as lang.go.search."`
	// Kind restricts matches to one symbol kind. See [KindFunc],
	// [KindStruct], etc. Empty means no kind filter.
	Kind string `json:"kind,omitempty" jsonschema:"Filter by symbol kind: func, struct, interface, type, const, var, method, all."`
	// IncludePrivate adds unexported symbols to the search candidates.
	// Default false — exported symbols are the typical search target.
	// Enable when refactoring or debugging inside a package.
	IncludePrivate bool `json:"include_private,omitempty" jsonschema:"Include unexported symbols. Default: exported only."`
	// MaxResults caps the number of ranked search hits returned in the
	// Results slice. Default 10. The Selected hit is always returned
	// fully regardless of MaxResults.
	MaxResults int `json:"max_results,omitempty" jsonschema:"Cap of search results returned. Default: 10."`
	// Pick is the zero-based index of the ranked result to explore.
	// Default 0 (the top match). Use 1, 2, … to drill into another
	// match without re-running search — cheaper than two separate
	// calls when the agent realises the second hit was what it really
	// wanted.
	Pick int `json:"pick,omitempty" jsonschema:"Which ranked search result to explore. Default: 0 (top). Use 1, 2, … to drill into a different match without a separate search call."`
	// Mode is the [ExploreInput] Mode applied to the picked result.
	// [ModeSkeleton] is the default; use [ModeCode] to inline
	// implementations for direct patching or [ModeDocs] for the cheapest
	// output.
	Mode string `json:"mode,omitempty" jsonschema:"Explore mode for the picked result: 'docs', 'skeleton' (default), or 'code'."`
}

// SearchExploreOutput is the fused response from
// lang.go.search_explore. It carries the full ranked search results
// (the Results slice) so the agent can re-pick a different match by
// re-calling with a new Pick value — without repeating the search
// step.
//
// The Selected and Symbols fields hold the explored result: Selected
// is the picked [SearchResult] and Symbols mirrors the shape returned
// by standalone lang.go.explore.
type SearchExploreOutput struct {
	// Results is the ranked list of search hits, same shape as the
	// Results slice on [SearchOutput]. Capped at the request's
	// MaxResults. Use to re-pick a different match without re-running
	// the search.
	Results []SearchResult `json:"results" jsonschema:"All search hits in ranked order — same shape as lang.go.search.results. Use this list to pick a different match (set Pick=N and re-call) if the top hit wasn't what you wanted."`
	// TotalMatches is the total number of matches the search produced
	// before the MaxResults cap was applied. When TotalMatches >
	// len(Results) the search was truncated.
	TotalMatches int `json:"total_matches" jsonschema:"Total search matches before the MaxResults cap."`
	// Truncated reports whether Results was capped at MaxResults and
	// additional matches were dropped from the response.
	Truncated bool `json:"truncated,omitempty" jsonschema:"True if Results was capped at MaxResults."`
	// Selected is the [SearchResult] at index Pick — the entry whose
	// symbol metadata was explored. Nil when the search produced zero
	// matches (in which case Symbols is also empty).
	Selected *SearchResult `json:"selected,omitempty" jsonschema:"The result that was explored (Results[Pick]). Nil when there were no matches."`
	// Symbols is the explored metadata for Selected, in the same shape
	// as the Symbols map on [ExploreOutput]. Empty when no match was
	// found.
	Symbols map[string]SymbolMetadata `json:"symbols,omitempty" jsonschema:"Explored symbol metadata — same shape as lang.go.explore.symbols. Empty when no match was found."`
}

// FixInput drives lang.go.fix — the verify-fix-verify workflow tool
// that folds three calls (verify, patch, verify-again) into one. Under
// the hood it routes the patch step through lang.go.patch so the
// build-gate and atomic-rollback machinery applies; the FixInput shape
// is a thin convenience over running those tools sequentially.
//
// Use FixInput when the agent has confirmed an issue exists and wants
// an autonomous best-effort fix attempt without manually orchestrating
// the sub-tools. Lint is the only suite that reliably produces
// auto-applicable patches; for test or bench fixes the agent will
// almost certainly need to apply the diagnosis manually.
type FixInput struct {
	// Targets is the list of Go packages to lint and fix. Accepts the
	// same forms as `go test`: import paths, workspace-relative
	// directories, or `./...` for the whole module. Defaults to
	// ["./..."].
	Targets []string `json:"targets,omitempty" jsonschema:"Go packages to lint and fix. Default: ['./...']."`
	// Suites is the list of verify suites to run during both the
	// initial-verify and final-verify phases. Default ["lint"] — the
	// only suite that reliably produces auto-applicable patches. Add
	// "test" or others if the agent specifically expects the suite to
	// emit useful patches.
	Suites []string `json:"suites,omitempty" jsonschema:"Suites to run. Default: ['lint']. Lint is the suite that produces actionable suggested patches; test/bench/fuzz rarely produce auto-fixable issues."`
	// CompareTo is an optional git ref. When set, Targets is narrowed
	// to packages affected by the diff between the working tree and
	// CompareTo (e.g. `main`). Useful for CI-style fixes that only touch
	// recently-changed code.
	CompareTo string `json:"compare_to,omitempty" jsonschema:"Git ref for diff-aware narrowing — only fix issues in packages affected by the diff."`
	// MaxIssuesPerType caps issues collected per suite during the
	// initial verify phase. Defaults to 5. The cap limits the number of
	// patches the fix step will attempt — agents that want all patches
	// applied should raise this.
	MaxIssuesPerType int `json:"max_issues_per_type,omitempty" jsonschema:"Cap issues per suite during initial verify. Default: 5."`
	// FailFast stops the verify phases at the first suite failure
	// instead of running every suite. Use to shave latency when only
	// the first failure matters.
	FailFast bool `json:"fail_fast,omitempty" jsonschema:"Stop on first suite failure during the verify phases."`
	// DryRun computes the patch set but does not apply it or run the
	// final verify. Useful for previewing the changes that would be
	// applied. The InitialVerify and PatchOutput fields of the response
	// are populated, but FinalVerify is omitted.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Run verify and compute the patches, but do not apply or re-verify. Useful for previewing the fix set."`
}

// FixOutput records each phase of the lang.go.fix workflow so the
// agent can audit the chain: what was wrong before, which patches
// landed (or rolled back), and what the final state looks like.
//
// The pattern lets agents reason about partial successes —
// InitialVerify may show 10 issues, PatchOutput may report 7 applied
// and 3 rolled back, and FinalVerify may show the remaining 3 plus
// any new issues the patches inadvertently introduced. Every field
// is populated except FinalVerify (skipped on DryRun=true or when
// zero patches applied).
type FixOutput struct {
	// InitialVerify is the result of the first verify phase — the
	// issues and suggested patches that existed before any fix was
	// applied. Use to confirm the diagnosis before reading the patch
	// outcome.
	InitialVerify *VerifyOutput `json:"initial_verify,omitempty" jsonschema:"Result of the first verify phase — issues and suggested patches before any fix is applied."`
	// PatchOutput is the raw response from lang.go.patch for the
	// applied patch batch. Per-patch errors, the diff each patch
	// produced, and rollback details all surface here. Typed as `any`
	// because the patch tool's response shape is owned by the
	// filesystem package, not this one.
	PatchOutput any `json:"patch_output,omitempty" jsonschema:"Result of applying suggested patches (lang.go.patch output). Rollback details surfaced here on failure."`
	// Applied is the count of patches that were successfully committed
	// to disk and passed the build gate.
	Applied int `json:"applied" jsonschema:"Number of patches successfully committed to disk."`
	// Failed is the count of patches that failed the build gate and
	// were rolled back. Applied + Failed equals the number of patches
	// attempted.
	Failed int `json:"failed" jsonschema:"Number of patches that failed verification and were rolled back."`
	// FinalVerify is the result of the verify phase that runs after
	// patches are applied. Empty when DryRun was true or when no
	// patches landed. Compare against InitialVerify to confirm the
	// fixed issues are gone and no new ones were introduced.
	FinalVerify *VerifyOutput `json:"final_verify,omitempty" jsonschema:"Result of the post-fix verify. Empty when DryRun=true or when no patches were applied."`
}

// VerifyInput is the agent-facing request for lang.go.verify — the
// workspace-quality gate. Verify runs one or more suites
// ([SuiteLint], [SuiteTest], [SuiteBench], [SuiteFuzz]) against
// Targets and returns a structured [VerifyOutput] with per-suite
// results, deduplicated issue clusters, and ready-to-apply patches.
//
// Use VerifyInput as the canonical replacement for chaining `Bash:
// golangci-lint run` + `Bash: go test`. The structured output saves
// tokens versus parsing raw command output, and the suggested
// patches feed directly into [PatchEdit] or lang.go.fix.
//
// For diff-aware verification — running only the packages touched by
// a recent change — set CompareTo to a git ref. The tool delegates to
// the blast-radius analyser to compute the affected target set.
type VerifyInput struct {
	// Suites is the list of quality gates to run. Default ["lint",
	// "test"]. Valid entries are [SuiteLint], [SuiteTest], [SuiteBench],
	// and [SuiteFuzz]. Unknown names are silently skipped to preserve
	// forward compatibility.
	Suites []string `json:"suites,omitempty" jsonschema:"Quality gates to run. Default: ['lint', 'test']. Options: lint, test, bench, fuzz."`
	// Targets is the list of Go packages to verify. Accepts import
	// paths, workspace-relative directories, or `./...` patterns.
	// Defaults to ["./..."]. Narrowing to a subset materially reduces
	// wall time on large workspaces.
	Targets []string `json:"targets,omitempty" jsonschema:"Go packages to verify. Default: ['./...']."`
	// Focus is an optional regex applied to test and benchmark names.
	// Examples: `^TestGrep`, `BenchmarkPatch`. Forwarded to `go test
	// -run` and `go test -bench` respectively. Empty means no name
	// filter.
	Focus string `json:"focus,omitempty" jsonschema:"Regex filter for specific tests/benchmarks. Example: '^TestGrep' or 'BenchmarkPatch'."`
	// MaxIssuesPerType caps the number of issues collected per suite,
	// to bound the context cost of the response. Defaults to 5.
	// Clustering by [clusterIssues] runs after this cap, so widely-
	// repeated diagnostics may still surface in the Clusters slice.
	MaxIssuesPerType int `json:"max_issues_per_type,omitempty" jsonschema:"Cap issues per suite to control context cost. Default: 5."`
	// FailFast stops the suite loop on the first suite that reports
	// [StatusFail] or [StatusError] instead of running every requested
	// suite. Use to shave latency when only the first failure matters.
	FailFast bool `json:"fail_fast,omitempty" jsonschema:"Stop on first suite failure."`
	// CompareTo is an optional git ref for diff-aware verification.
	// When set, Targets is narrowed to packages affected by the diff
	// between HEAD and CompareTo (e.g. `main`). The resolved target set
	// lands in the response's ResolvedTargets field.
	CompareTo string `json:"compare_to,omitempty" jsonschema:"Git ref for diff-aware linting — narrows targets to packages affected by the diff."`
	// Detail controls response verbosity. [DetailSummary] keeps the
	// response small (status + counts, no per-issue list);
	// [DetailStandard] (the default) adds file:line entries, code
	// snippets, and suggested patches; [DetailFull] adds AST forensics
	// and the affected function's body to every issue.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity: 'summary' (status + counts only, ~80% smaller), 'standard' (default — issues with file:line, snippets, suggested patches), 'full' (adds AST forensics + the affected function's body inline). Use 'summary' for fast checks, 'standard' for fix workflows, 'full' for deep debugging."`
}

// VerifyOutput is the structured response from lang.go.verify. It
// carries per-suite results keyed by suite name, an aggregate status
// rolled up across all suites, and a flat slice of ready-to-apply
// patches culled from the suites that produced them.
//
// Agents should read OverallStatus first to decide whether to drill
// into the per-suite Reports map. SuggestedPatches lets a follow-up
// lang.go.patch call apply auto-fixes without re-running the
// linters. BloatPrevention points to the on-disk log directory where
// the full raw output lives — fetch it only when the structured
// response is insufficient.
//
// VerifyOutput is the public output type of the lang.<lang>.verify
// family; both the Go and other language verify tools share this
// shape.
type VerifyOutput struct {
	// OverallStatus is the rolled-up result across every suite that
	// ran. One of [StatusPass], [StatusLintOK], [StatusDegraded],
	// [StatusFail], or [StatusError]. Inspect Reports for the per-suite
	// breakdown when OverallStatus is anything but StatusPass.
	OverallStatus string `json:"overall_status" jsonschema:"pass: all gates passed. lint_ok: lint passed but logic untested. degraded: one or more suites failed. error: infrastructure failure."`
	// ResolvedTargets is the actual list of Go packages that were
	// verified, after the diff-narrowing pass applied. Populated only
	// when the request's CompareTo field was set; empty otherwise. Use
	// to confirm the diff-aware narrowing landed on the expected set.
	ResolvedTargets []string `json:"resolved_targets,omitempty" jsonschema:"Actual packages verified after diff-narrowing. Only present when compare_to was used."`
	// Reports is the per-suite results keyed by suite name ("lint",
	// "test", "bench", "fuzz"). Iterate this map for a per-suite
	// breakdown of pass/fail status, issue lists, and metrics.
	Reports map[string]SuiteReport `json:"reports" jsonschema:"Per-suite results keyed by suite name"`
	// SuggestedPatches is the flat list of [SuggestedPatch] entries
	// collected across all suites. Pass directly to lang.go.patch or
	// lang.go.fix to apply atomically — the patches are already in
	// fs.patch format.
	SuggestedPatches []SuggestedPatch `json:"suggested_patches,omitempty" jsonschema:"Auto-fix patches in fs.patch format. AGENT HINT: Pass these directly to fs.patch or enable auto_fix to apply automatically."`
	// BloatPrevention points to the on-disk log directory containing
	// the raw tool output for the run. The structured response is
	// trimmed to fit a reasonable token budget; agents that need the
	// full output (timing data, stack traces, verbose logs) should
	// retrieve it via the LogID.
	BloatPrevention BloatPrevention `json:"bloat_prevention" jsonschema:"Full raw logs are written to disk at this path. Only structured results are returned to save context. Use log_id to retrieve details if needed."`
	// Truncated reports whether the response was cut to fit the token
	// budget. When true, inspect the BloatPrevention logs for the
	// omitted content — typically additional issues beyond the
	// MaxIssuesPerType cap.
	Truncated bool `json:"truncated,omitempty" jsonschema:"True if output was cut to fit token budget"`
	// Warnings is a list of diagnostic warnings about the verification
	// run itself — skipped packages (e.g. broken go.mod), tool-version
	// mismatches, suites that failed to start. Distinct from per-suite
	// issues, which describe problems with the code.
	Warnings []string `json:"warnings,omitempty" jsonschema:"Diagnostic warnings about the verification run."`
	// NextActions carries up to [MaxNextActions] follow-up tool-call
	// suggestions — typically lang.go.fix when patches are available,
	// or lang.go.explore on a failing test's symbol when DetailFull is
	// in effect.
	NextActions []NextAction `json:"next_actions,omitempty" jsonschema:"Suggested follow-up tool calls"`
}

// SuiteReport carries the results from a single verification suite
// (lint, test, bench, fuzz) — its pass/fail status, the issues it
// found, optional benchmark metrics, and (for test suites)
// pass/fail/skip counts and coverage data.
//
// Issues that share the same (Linter, Message) pair are deduplicated
// by [clusterIssues] after the response is assembled: three or more
// matching issues collapse into a single [IssueCluster] entry in
// Clusters; varied issues stay in Issues. This keeps repeated lint
// findings from blowing out the agent's context.
type SuiteReport struct {
	// Status is the suite's individual pass/fail result: [StatusPass],
	// [StatusFail], [StatusDegraded], or [StatusError]. Distinct from
	// [VerifyOutput] OverallStatus, which is the rolled-up across all
	// suites.
	Status string `json:"status" jsonschema:"pass, fail, degraded, or error"`
	// Summary is a one-line human-readable description of the suite's
	// outcome — e.g. "3 lint issues across 2 files" or "all tests
	// passed". Suitable for CLI/TUI presentation.
	Summary string `json:"summary" jsonschema:"One-line human summary"`
	// Issues is the list of unique diagnostic issues found by this
	// suite (i.e. those that did not collapse into a Cluster). Empty
	// on a clean pass. Capped at the request's MaxIssuesPerType before
	// clustering runs.
	Issues []Issue `json:"issues,omitempty" jsonschema:"Diagnostic issues found by this suite"`
	// Clusters is the list of deduplicated issue groups: when three or
	// more issues share the same (Linter, Message) pair they collapse
	// into one [IssueCluster] here. Each cluster carries up to three
	// representative Examples plus the total Count.
	Clusters []IssueCluster `json:"clusters,omitempty" jsonschema:"Deduplicated issue groups. When many issues share the same pattern, they are collapsed into clusters to save context."`
	// Metrics is the per-benchmark measurement list, populated only when
	// [SuiteBench] ran. Each [Metric] carries ns/op, allocs/op, and
	// bytes/op plus optional heap-escape analysis.
	Metrics []Metric `json:"metrics,omitempty" jsonschema:"Benchmark results"`
	// TestCounts holds aggregate pass/fail/skip counts for [SuiteTest]
	// runs. Nil for non-test suites.
	TestCounts *TestCounts `json:"test_counts,omitempty" jsonschema:"Pass/fail/skip counts for test suites"`
	// Coverage holds code-coverage results captured during a test run.
	// Nil when coverage was not enabled or the suite did not produce
	// coverage data.
	Coverage *CoverageInfo `json:"coverage,omitempty" jsonschema:"Code coverage results if available"`
}

// IssueCluster groups multiple [Issue] entries that share the same
// (Linter, Message) pair into a single response element. The
// deduplication exists because lint output frequently flags the same
// defect at dozens of sites (e.g. `err113` on every fmt.Errorf in
// the codebase); returning them individually would inflate the
// agent's context without adding new information.
//
// The cluster records the common error Pattern, the total Count, up
// to three representative Examples, and optionally a template
// [SuggestedPatch] that may need site-specific adaptation for each
// occurrence. Clusters appear in the Clusters slice of
// [SuiteReport] alongside the unique-issue Issues slice.
type IssueCluster struct {
	// Pattern is the common error-message text shared by every issue
	// in this cluster. Identical to each Example issue's Message field.
	Pattern string `json:"pattern" jsonschema:"The common error pattern across all issues in this cluster."`
	// Linter is the name of the linter or analyser that produced the
	// clustered issues (e.g. `errcheck`, `staticcheck`).
	Linter string `json:"linter,omitempty" jsonschema:"The linter that found these issues."`
	// Count is the total number of issues that matched the Pattern
	// across the run. May be much larger than len(Examples) — examples
	// are a sample, Count is the truth.
	Count int `json:"count" jsonschema:"Total number of occurrences of this pattern."`
	// Examples is up to three representative issues from the cluster.
	// Use the first example's Location and Snippet to understand the
	// pattern in context; assume the remainder are structurally
	// similar.
	Examples []Issue `json:"examples" jsonschema:"Representative examples (max 3). AGENT HINT: Use the first example's location to understand the pattern."`
	// SuggestedPatch is an optional template patch that addresses the
	// cluster's defect. May require site-specific adaptation before
	// applying to every occurrence — the patch's OldString often
	// matches only the first example. Nil when no auto-fix is available
	// for the pattern.
	SuggestedPatch *SuggestedPatch `json:"suggested_patch,omitempty" jsonschema:"A single patch template that fixes this pattern. AGENT HINT: This patch may need to be adapted for each occurrence."`
}

// Issue is one diagnostic finding from a lint, test, or other
// verification suite. Carries enough information for the agent to
// locate the problem (File, Line, Column), understand it (Message,
// Snippet, optionally SymbolSource), and act on it (Hint,
// ReproCommand, an attached [SuggestedPatch]).
//
// The per-issue snippet is suitable for verbatim use as the
// OldString of a [PatchEdit] — the field's whitespace is preserved
// from the source file. Repeated issues sharing (Linter, Message)
// are deduplicated by [clusterIssues] into [IssueCluster] groups.
type Issue struct {
	// File is the source file path containing the issue, relative to
	// the module root.
	File string `json:"file" jsonschema:"Source file path containing the issue"`
	// Line is the 1-based line number where the issue starts.
	Line int `json:"line" jsonschema:"Line number of the issue (1-based)"`
	// Column is the 1-based column number where the issue starts.
	// Zero when the linter does not report a column (some tools work at
	// line granularity only).
	Column int `json:"column,omitempty" jsonschema:"Column number of the issue (1-based)"`
	// Severity classifies the issue: [SeverityError] (hard failure,
	// blocks merge), [SeverityWarning] (should fix but does not block),
	// or [SeverityInfo] (informational).
	Severity string `json:"severity,omitempty" jsonschema:"error, warning, or info"`
	// Linter is the name of the linter or tool that produced this
	// issue (e.g. `errcheck`, `staticcheck`, `go test`). Used by
	// [clusterIssues] as part of the deduplication key.
	Linter string `json:"linter,omitempty" jsonschema:"Which linter/tool found this"`
	// Message is the human-readable description of the issue, as
	// emitted by the linter or test runner. The text is the same
	// string [clusterIssues] uses as the second part of the
	// deduplication key, so it should be stable across runs.
	Message string `json:"message" jsonschema:"Human-readable description of the issue"`
	// Snippet is the minified source context around the issue line.
	// Matches the file's content exactly: suitable for use verbatim as
	// the OldString of a [PatchEdit]. Empty when [DetailSummary] mode
	// strips snippets.
	Snippet string `json:"snippet,omitempty" jsonschema:"Minified source context around the issue. AGENT HINT: Use this value verbatim as old_string in fs.patch to ensure 100% matching accuracy."`
	// Hint is an actionable suggestion aimed at agents: "add the
	// missing return value", "check this nil dereference". Empty for
	// linters that do not produce hints. Trimmed in [DetailSummary]
	// mode.
	Hint string `json:"hint,omitempty" jsonschema:"Actionable hint for agents"`
	// ReproCommand is a copy-paste-ready shell command that reproduces
	// this specific issue — typically a `go test -run ...` invocation
	// scoped to the failing test. Empty for lint findings, which do not
	// need special invocation.
	ReproCommand string `json:"repro_command,omitempty" jsonschema:"Exact shell command to reproduce this specific issue. Copy-paste ready."`
	// SymbolName is the name of the symbol this issue affects (the
	// function containing the failing assertion, the struct whose
	// field has the lint problem). Pass to lang.go.explore for the
	// full implementation.
	SymbolName string `json:"symbol_name,omitempty" jsonschema:"The symbol this issue affects. AGENT HINT: Pass to lang.go.explore symbols field for full context."`
	// SymbolSource is the full source of the affected function or
	// struct, included only when [DetailFull] is requested. Use as
	// context for understanding the bug or as the OldString source for
	// a [PatchEdit] that rewrites the symbol entirely.
	SymbolSource string `json:"symbol_source,omitempty" jsonschema:"Full source of the affected function/struct when include_forensics is enabled. AGENT HINT: Use this as context for understanding the bug, and as old_string source for fs.patch edits."`
}

// SuggestedPatch is an auto-fix patch in lang.go.patch (and
// fs.patch) format. Produced by linters that emit machine-applicable
// fixes (e.g. golangci-lint's `--fix` patches) and surfaced through
// verify and fix workflows.
//
// The patch is self-contained: FilePath identifies the file, Edits
// carries an ordered list of find-and-replace operations, and Reason
// / Source explain provenance. Pass the whole struct through to
// lang.go.patch unchanged — matches are literal and whitespace must
// align exactly, which is why the Snippet field on each [Issue] is
// the preferred source for OldString values.
type SuggestedPatch struct {
	// FilePath is the path of the file the patch applies to, relative
	// to the module root.
	FilePath string `json:"file_path" jsonschema:"Path to the file this patch applies to"`
	// Edits is the ordered list of [PatchEdit] operations to apply to
	// FilePath. Order matters within the file: each subsequent edit
	// sees the file in its post-previous-edit state.
	Edits []PatchEdit `json:"edits" jsonschema:"Ordered find-and-replace edits. AGENT HINT: Pass this entire object directly to fs.patch patches field. Matches are literal — whitespace and comments must match exactly."`
	// Reason is a short human-readable explanation of why this patch
	// is suggested. Surface in TUI/CLI presenters; agents may include
	// in commit messages.
	Reason string `json:"reason" jsonschema:"Why this patch is suggested"`
	// Source is the name of the linter or tool that generated the
	// patch (e.g. `errcheck`, `gofumpt`). Used for grouping and audit
	// trails.
	Source string `json:"source" jsonschema:"Which linter/tool generated it"`
}

// PatchEdit is one find-and-replace operation within a
// [SuggestedPatch]. The operation is a literal text replacement —
// OldString must match the file content character-for-character,
// including whitespace, line endings, and surrounding context.
//
// When possible the OldString should be sourced from a verbatim
// return value of lang.go.explore or the Snippet field of an
// [Issue], rather than hand-typed: gopls's exact whitespace handling
// differs subtly from what hand-written text usually produces.
//
// ReplaceAll switches between first-match and all-occurrences
// behaviour. Use first-match (the default) for surgical edits where
// the target appears once; use ReplaceAll only when the same
// pattern appears at multiple sites that should all change
// identically.
type PatchEdit struct {
	// OldString is the exact text to find in the file. Matches are
	// LITERAL — whitespace, line endings, and surrounding context must
	// match precisely. The recommended source is a value returned by
	// lang.go.explore (e.g. SymbolMetadata.Implementation) or an
	// [Issue] Snippet; hand-typed values frequently fail to align.
	OldString string `json:"old_string" jsonschema:"Exact text to find in the file. Matches are LITERAL — whitespace must match exactly as returned by lang.go.explore."`
	// NewString is the replacement text. Must carry the same surrounding
	// whitespace and indentation as OldString — the tool does not
	// auto-reflow indentation. An empty NewString deletes the OldString
	// match from the file.
	NewString string `json:"new_string" jsonschema:"Replacement text. Must include the same surrounding whitespace/indentation as old_string."`
	// ReplaceAll switches between first-match (the default) and
	// all-occurrences replacement. Default false. Set true when the
	// same OldString appears at multiple sites that should all be
	// rewritten identically — e.g. when applying a project-wide cleanup
	// clustered into one patch.
	ReplaceAll bool `json:"replace_all,omitempty" jsonschema:"Replace ALL occurrences of old_string in the file, not just the first. Use when the same pattern appears multiple times (e.g. duplicate function calls). Default: false (replace first only)."`
}

// TestCounts summarises pass/fail/skip aggregates from a [SuiteTest]
// run. Surfaced via the TestCounts field on [SuiteReport] and useful
// for the agent's at-a-glance "how broken is the workspace?" check.
//
// The four fields satisfy Total = Passed + Failed + Skipped on any
// successful run. When the underlying test runner crashed and the
// counts could not be parsed, Total is the only reliably non-zero
// field and the rest may be zero.
type TestCounts struct {
	// Total is the total number of test cases the runner attempted —
	// the sum of Passed, Failed, and Skipped on a successful run.
	Total int `json:"total" jsonschema:"Total number of test cases run"`
	// Passed is the number of test cases that finished successfully.
	Passed int `json:"passed" jsonschema:"Number of tests that passed"`
	// Failed is the number of test cases that reported a failure. Even
	// a single failure rolls the suite's Status up to [StatusFail].
	Failed int `json:"failed" jsonschema:"Number of tests that failed"`
	// Skipped is the number of test cases that called t.Skip or were
	// excluded by a build constraint. Skipped tests do not affect the
	// suite's pass/fail outcome but are surfaced so the agent can
	// verify the skip set is intentional.
	Skipped int `json:"skipped" jsonschema:"Number of tests that were skipped"`
}

// CoverageInfo carries code-coverage results captured during a
// [SuiteTest] run. Populated only when coverage was enabled on the
// `go test` invocation; nil otherwise.
//
// The data is suitable for spot-checking coverage of recently-
// changed code (compare UncoveredLines to a diff) or for surfacing
// the lowest-coverage hot spots without forcing the agent to parse
// a raw coverage profile.
type CoverageInfo struct {
	// Percentage is the percentage of statements covered, in the range
	// 0–100. Computed by `go test -cover` over the suite's target
	// packages.
	Percentage float64 `json:"percentage" jsonschema:"Percentage of statements covered (0-100)"`
	// UncoveredLines lists contiguous blocks of source lines that the
	// test run never executed. Empty when coverage is 100% or when
	// block-level data was not captured. Suitable for targeting
	// follow-up test generation.
	UncoveredLines []UncoveredRange `json:"uncovered_lines,omitempty" jsonschema:"Contiguous blocks of uncovered lines"`
}

// UncoveredRange describes a contiguous block of source lines that
// were never executed during a [SuiteTest] run with coverage
// enabled. Returned inside the UncoveredLines slice of
// [CoverageInfo].
//
// The range is half-inclusive at the agent's preference: StartLine
// and EndLine are both 1-based and inclusive, so a single uncovered
// line has StartLine == EndLine.
type UncoveredRange struct {
	// File is the source file path containing the uncovered block,
	// relative to the module root.
	File string `json:"file" jsonschema:"Source file path containing the uncovered block"`
	// StartLine is the 1-based line number of the first uncovered line
	// in the block.
	StartLine int `json:"start_line" jsonschema:"First uncovered line number (1-based)"`
	// EndLine is the 1-based line number of the last uncovered line in
	// the block, inclusive. Equals StartLine for single-line ranges.
	EndLine int `json:"end_line" jsonschema:"Last uncovered line number (inclusive)"`
}

// Metric is one benchmark measurement collected during a
// [SuiteBench] run. Carries the standard `testing.B` outputs
// (ns/op, allocs/op, bytes/op) plus optional regression detection
// and heap-escape analysis.
//
// The Regression field is populated only when a configured threshold
// is breached, so its presence is itself a signal. The Escapes
// field names which local variables in the benchmarked function
// escape to the heap — useful for chasing down allocation
// regressions.
type Metric struct {
	// Name is the benchmark function name (e.g. `BenchmarkPatch`).
	// Matches the function declaration in the test file.
	Name string `json:"name" jsonschema:"Benchmark function name"`
	// NsPerOp is the nanoseconds-per-operation figure averaged across
	// the benchmark run. Lower is faster.
	NsPerOp float64 `json:"ns_per_op" jsonschema:"Nanoseconds per operation"`
	// AllocsPerOp is the heap-allocations-per-operation count. Lower is
	// better; zero is the gold standard for hot-path functions.
	AllocsPerOp int `json:"allocs_per_op" jsonschema:"Heap allocations per operation"`
	// BytesPerOp is the bytes-allocated-per-operation count. Useful for
	// spotting size regressions even when AllocsPerOp stays constant.
	BytesPerOp int `json:"bytes_per_op" jsonschema:"Bytes allocated per operation"`
	// Regression is populated when this Metric breached a configured
	// threshold (typically a ns/op or allocs/op budget). Nil when the
	// benchmark stayed within budget. Presence alone is a signal.
	Regression *Regression `json:"regression,omitempty" jsonschema:"Regression details if a threshold was exceeded"`
	// Escapes is the heap-escape analysis for the benchmarked
	// function, naming which local variables escape and why. Populated
	// when the bench runner ran `-gcflags=-m`. Empty otherwise.
	Escapes []EscapeInfo `json:"escapes,omitempty" jsonschema:"Heap escape analysis for this benchmark's function — shows which variables escape and why"`
}

// EscapeInfo describes a single variable or expression that escapes
// to the heap in a benchmarked function. Returned inside the Escapes
// slice of [Metric] when escape analysis was enabled.
//
// The Cause field names the compiler-reported reason (interface
// boundary, closure capture, slice that grows past its initial
// capacity). The Hint field offers an actionable suggestion when
// the pattern is one the analyser recognises.
type EscapeInfo struct {
	// Line is the 1-based source line where the escape occurs.
	Line int `json:"line" jsonschema:"Line number where the escape occurs (1-based)"`
	// Variable is the variable or expression that escapes to the heap.
	// Matches the identifier or expression as it appears in source.
	Variable string `json:"variable" jsonschema:"Variable or expression that escapes"`
	// Cause is the compiler-reported reason for the escape — e.g.
	// "passed to interface boundary", "captured by closure", "slice
	// literal too large for stack". Lifted directly from
	// `-gcflags=-m` output.
	Cause string `json:"cause" jsonschema:"Why it escapes, e.g. passed to interface boundary, captured by closure"`
	// Hint is an actionable fix suggestion — what to change so the
	// value stays on the stack. Empty when the analyser does not
	// recognise the pattern.
	Hint string `json:"hint,omitempty" jsonschema:"Actionable fix suggestion"`
}

// Regression describes a benchmark metric that exceeded its
// configured threshold. Returned inside the Regression field of
// [Metric] only when the breach occurred; nil otherwise.
//
// The shape carries the offending field name, the observed value,
// and the threshold that was breached so the agent can compose a
// coherent regression report without needing to look up the
// baseline separately.
type Regression struct {
	// Field is the [Metric] field that regressed — `ns_per_op`,
	// `allocs_per_op`, or `bytes_per_op`. The name uses snake_case to
	// match the JSON field tag.
	Field string `json:"field" jsonschema:"Metric field that regressed (e.g. ns_per_op)"`
	// Value is the observed metric value that exceeded Threshold.
	Value float64 `json:"value" jsonschema:"Observed value that exceeded the threshold"`
	// Threshold is the configured upper bound that was breached. Read
	// from the workspace's benchmark-threshold configuration.
	Threshold float64 `json:"threshold" jsonschema:"Configured threshold that was breached"`
}

// BloatPrevention points to the on-disk directory where the full
// raw logs for a verify or fix run were written. The structured
// response is trimmed to fit a reasonable token budget; agents
// that need the raw output (timing data, stack traces, verbose
// debug logs) retrieve it via the LogID.
//
// The pattern keeps the structured response lean — small enough to
// fit easily in an LLM's effective context — without losing
// debuggability for the rare case where verbose output is
// actually needed.
type BloatPrevention struct {
	// LogID is the directory name under /tmp/techne-reports/ where
	// the full raw logs for the run were written. Formed as `et-<8
	// hex>` by [generateLogID].
	LogID string `json:"log_id" jsonschema:"Directory name under /tmp/techne-reports/"`
	// Hint is a copy-paste-ready instruction explaining how to read
	// the truncated logs from disk. Includes the full path so the
	// agent can dispatch a follow-up `cat` or fs.read without
	// composing it from LogID.
	Hint string `json:"hint" jsonschema:"Instruction for reading truncated logs from disk"`
}

// SearchInput is the agent-facing request for lang.go.search — a
// dual-backend symbol finder that combines gopls's fuzzy
// name-matching with an in-process doc-content scorer. The result
// is a tool that handles both "find this symbol I half-remember"
// (`HTTPRq` matches `HTTPRequest`) and "find code about X" ("how
// backpressure is handled") in a single call.
//
// Use SearchInput in preference to grep when the agent does not
// know the exact symbol name, or when prose intent rather than
// verbatim text is the right query. Use grep only when you know the
// exact regex to search for in source or comments.
//
// For the discover-then-explore pattern, lang.go.search_explore
// (see [SearchExploreInput]) saves a turn by inlining the
// follow-up explore call.
type SearchInput struct {
	// Query is the symbol-name fuzzy match or natural-language prose.
	// Examples: `foldPatch`, `HTTPRq`, `how backpressure is handled`.
	// Matching combines a fuzzy symbol-name search (via gopls) with a
	// doc-content scorer; both backends run in parallel and their hits
	// are deduplicated by (Symbol, Package).
	Query string `json:"query" jsonschema:"Symbol name (fuzzy-matched) or natural-language description. Examples: 'foldPatch', 'HTTPRq', 'how backpressure is handled'. Matching combines a fuzzy symbol-name search (gopls) with a doc-content scorer."`
	// Kind restricts matches to one symbol kind. See [KindFunc],
	// [KindStruct], etc. Default [KindAll] disables kind filtering.
	Kind string `json:"kind,omitempty" jsonschema:"Filter by symbol kind: func, struct, interface, type, const, var, method, all. Default: all."`
	// IncludePrivate adds unexported symbols to the candidate pool.
	// Default false — exported symbols are the typical search target.
	IncludePrivate bool `json:"include_private,omitempty" jsonschema:"Include unexported symbols. Default: exported only."`
	// MaxResults caps the number of ranked results returned. Default
	// 20. The full match count before truncation lands in
	// [SearchOutput] TotalMatches.
	MaxResults int `json:"max_results,omitempty" jsonschema:"Cap results. Default: 20."`
	// AutoExplore enriches the response with full [SymbolMetadata] for
	// the top match when the search returns exactly one result. Saves a
	// follow-up lang.go.explore call in the unambiguous case. Has no
	// effect when more than one match is returned.
	AutoExplore bool `json:"auto_explore,omitempty" jsonschema:"When true and the search returns exactly one result, returns full SymbolMetadata inline so a follow-up explore call is unnecessary."`
}

// SearchOutput is the structured response from lang.go.search. It
// carries a ranked list of [SearchResult] entries, the total match
// count before truncation, and the workspace fingerprint at query
// time so agents can detect when prior search results are stale.
//
// Results are sorted by descending Score (a value in 0.0–1.0 where
// 1.0 indicates a perfect match across both symbol name and
// docblock). Multiple scoring backends — gopls's fuzzy matcher and
// the in-process doc scorer — run in parallel and their hits are
// deduplicated by (Symbol, Package) before ranking, with the higher
// score winning each tie.
//
// TotalMatches reflects the count BEFORE the MaxResults cap, so
// callers can detect when more matches were available than returned.
// Scoring is deterministic given a frozen workspace state — but
// workspace state includes file mtimes, so a single edit may shift
// the ranking of close ties.
type SearchOutput struct {
	// Results is the ranked list of matching symbols, sorted by
	// descending Score. Capped at the request's MaxResults.
	Results []SearchResult `json:"results" jsonschema:"Matching symbols up to the MaxResults cap"`
	// TotalMatches is the total number of matches before the MaxResults
	// cap was applied. When TotalMatches > len(Results) the search was
	// truncated.
	TotalMatches int `json:"total_matches" jsonschema:"Total before MaxResults cap"`
	// Truncated reports whether Results was capped at MaxResults and
	// additional matches were dropped from the response.
	Truncated bool `json:"truncated,omitempty" jsonschema:"True if results were capped by MaxResults"`
	// WorkspaceVersion is the [WorkspaceVersion] fingerprint of the
	// module source at query time. Compare across turns — if it has
	// changed, prior search results may reflect stale source.
	WorkspaceVersion string `json:"workspace_version,omitempty" jsonschema:"Fingerprint of the module source at query time. Compare across turns — if it changed, prior results may be stale."`
	// NextActions carries up to [MaxNextActions] follow-up tool-call
	// suggestions — typically a lang.go.explore on the top match.
	NextActions []NextAction `json:"next_actions,omitempty" jsonschema:"Suggested follow-up tool calls"`
}

// SearchResult is one ranked hit from a lang.go.search response.
// Carries enough identifying information (Symbol, Package, Location)
// to chain into a follow-up explore call, plus scoring metadata
// (Score, MatchedOn) so the agent can judge whether the hit is the
// intended target.
//
// When the request's AutoExplore flag was true and the search
// returned exactly one hit, the Metadata field is populated with
// full [SymbolMetadata] so no follow-up call is needed.
type SearchResult struct {
	// Symbol is the identifier name of the matched declaration. Bare
	// name for top-level symbols, `Receiver.Method` for methods.
	Symbol string `json:"symbol" jsonschema:"Identifier name of the matched symbol"`
	// Kind is the symbol's declaration kind: one of [KindFunc],
	// [KindMethod], [KindStruct], [KindInterface], [KindType],
	// [KindConst], or [KindVar].
	Kind string `json:"kind" jsonschema:"func, struct, interface, type, const, var, or method"`
	// Package is the import path of the package containing the
	// symbol. Pass directly to [ExploreInput] for a follow-up explore
	// call.
	Package string `json:"package" jsonschema:"Import path of the package containing this symbol. AGENT HINT: Pass directly to lang.go.explore package field."`
	// Location is the declaration site in `file:line` form, relative
	// to the module root.
	Location string `json:"location" jsonschema:"file:line"`
	// Signature is the symbol's one-line declaration signature (for
	// functions, methods, and named types). Empty for symbols without
	// a conventional signature (vars, consts).
	Signature string `json:"signature,omitempty" jsonschema:"One-line signature"`
	// Docblock is the first line of the symbol's doc comment, with
	// `//` markers stripped. Empty when the symbol is undocumented. The
	// first line alone is shown to keep the search response compact;
	// run a follow-up lang.go.explore for the full doc.
	Docblock string `json:"docblock,omitempty" jsonschema:"First line of doc comment"`
	// Metadata is the full [SymbolMetadata] for this hit, populated
	// only when the request's AutoExplore flag was true and the search
	// returned exactly one result. When present, no follow-up explore
	// call is needed.
	Metadata *SymbolMetadata `json:"metadata,omitempty" jsonschema:"Full symbol metadata, populated when auto_explore found exactly one match. If present, no follow-up explore call is needed."`
	// ReproCommand is a copy-paste-ready shell command that runs just
	// this target. Populated for `Test*`, `Benchmark*`, and `Fuzz*`
	// functions; empty for other symbol kinds.
	ReproCommand string `json:"repro_command,omitempty" jsonschema:"For Test*/Benchmark*/Fuzz* functions: exact go test command. Copy-paste ready."`
	// Score is the relevance score in the range 0.0–1.0, combining
	// the fuzzy name match and doc-content overlap signals. Higher is
	// better. Used by [SearchOutput] to rank Results.
	Score float64 `json:"score,omitempty" jsonschema:"Relevance score (0-1). Higher is better. Combines fuzzy name match and doc-content overlap."`
	// MatchedOn is a comma-separated list of the matchers that
	// contributed to this hit. Possible tokens: `symbol_name`,
	// `docblock`, `package_context`. Useful for understanding why a
	// symbol scored as it did.
	MatchedOn string `json:"matched_on,omitempty" jsonschema:"Comma-separated list of what matched: symbol_name, docblock, package_context."`
}

// BlastRadiusInput is the internal request shape used by
// lang.go.verify when the CompareTo field on a [VerifyInput] is
// set: it identifies which files and symbols changed so the
// verifier can narrow its target set to the affected packages.
//
// Not exposed as a public tool input — agents drive blast-radius
// narrowing implicitly through CompareTo. Documented for the rare
// case where a downstream consumer needs to call the analyser
// directly.
type BlastRadiusInput struct {
	// Files is the list of files that were or will be modified,
	// relative to the module root. Drives the dependency-graph walk
	// that populates AffectedPackages on the output.
	Files []string `json:"files" jsonschema:"Files that were or will be modified."`
	// Symbols is an optional list of specific changed symbols within
	// Files. When set, the test-discovery pass narrows further to tests
	// that exercise those symbols rather than every test in the
	// affected packages.
	Symbols []string `json:"symbols,omitempty" jsonschema:"Specific changed symbols, used to filter test discovery."`
	// IncludeTransitive expands the affected-package set to include
	// packages that transitively depend on the modified files. Default
	// false — typically the direct dependents are enough.
	IncludeTransitive bool `json:"include_transitive,omitempty" jsonschema:"Include transitively dependent packages."`
	// MaxDepth caps the dependency-graph walk depth when
	// IncludeTransitive is true. Default 3. Limits worst-case walk
	// time on workspaces with deep import graphs.
	MaxDepth int `json:"max_depth,omitempty" jsonschema:"How deep to trace transitive deps. Default: 3."`
}

// BlastRadiusOutput describes the set of packages, tests, and risk
// level affected by a change. Returned internally by the blast-
// radius analyser and consumed by lang.go.verify to narrow the
// target set in diff-aware mode.
//
// The SuggestedVerifyInput field is a pre-built [VerifyInput] that
// the agent can pass directly to lang.go.verify — saves the agent
// from hand-assembling Targets from AffectedPackages.
type BlastRadiusOutput struct {
	// AffectedPackages is the list of packages reachable from the
	// changed files according to the dependency graph. Used as the
	// resolved target set when lang.go.verify runs in diff-aware mode.
	AffectedPackages []string `json:"affected_packages"`
	// CriticalTests is the list of tests considered most important to
	// run for the affected packages. Heuristically chosen — typically
	// the direct caller tests and the package's table-driven core
	// test.
	CriticalTests []string `json:"critical_tests"`
	// RiskLevel is the aggregate risk classification: one of [RiskLow],
	// [RiskMedium], [RiskHigh], or [RiskCritical]. Derived from the
	// fan-out of AffectedPackages and the seniority/centrality of the
	// touched symbols.
	RiskLevel string `json:"risk_level"`
	// SuggestedVerifyInput is a pre-built [VerifyInput] targeting the
	// affected packages. Consumed by lang.go.verify when the request's
	// CompareTo field was set.
	SuggestedVerifyInput *VerifyInput `json:"suggested_verify_input,omitempty"`
}

// DepsInput is the agent-facing request for the legacy unified
// lang.go.deps tool — a single entry point that bundles callers,
// implementations, references, and invocations behind one Include
// discriminator.
//
// The four narrow per-relationship tools ([CallersInput],
// [ImplementationsInput], [ReferencesInput], [InvocationsInput]) are
// preferred for new code because their schemas are self-describing
// and the agent's prompt only ever sees the fields relevant to the
// question being asked. DepsInput remains available for callers
// that batch multiple relationship queries in one call.
type DepsInput struct {
	// Symbol identifies the target to trace. Bare name for top-level
	// symbols, `Receiver.Method` for methods. Examples: `Snapshot`,
	// `Engine.Execute`.
	Symbol string `json:"symbol" jsonschema:"Target identifier to trace. Example: 'Snapshot' or 'Engine.Execute'. AGENT HINT: Use symbol names from lang.go.explore."`
	// Include lists the relationship types to discover in this call.
	// Valid entries: [IncludeImplementations], [IncludeCallers],
	// [IncludeReferences], [IncludeInvocations]. Multiple may be
	// requested in one call; results are grouped by include name in
	// the response's Relationships map.
	Include []string `json:"include" jsonschema:"Relationship types to discover. 'implementations': finds structs satisfying an interface. 'callers': finds functions calling the target. 'references': finds all usages."`
	// Package is an optional scope for the search — limits the lookup
	// to a single package's import path. Empty means search the whole
	// workspace.
	Package string `json:"package,omitempty" jsonschema:"Scope package"`
	// Depth is the traversal depth for call graphs. Default 1 (direct
	// callers only). Higher values include transitive callers up to
	// the specified depth.
	Depth int `json:"depth,omitempty" jsonschema:"Traversal depth for call graphs. Default: 1"`
	// IncludeExternal expands the search beyond the workspace into
	// external Go modules and the standard library. Default false.
	IncludeExternal bool `json:"include_external,omitempty" jsonschema:"Include symbols from external packages"`
	// MaxOutputTokens is the upper bound on response size in tokens.
	// When exceeded the result is truncated and Truncated is set on
	// the response.
	MaxOutputTokens int `json:"max_output_tokens,omitempty" jsonschema:"Cap output size to fit token budget"`
	// AutoExplore inlines full caller/implementor implementations into
	// each [DepReference] when the total reference count is small
	// (typically ≤3). Collapses the deps→explore pattern from two
	// turns into one. Enable when the agent plans to modify the
	// callers.
	AutoExplore bool `json:"auto_explore,omitempty" jsonschema:"When true and <=3 total references found, includes full implementation of each caller/implementor inline. Collapses deps→explore from 2 turns to 1. AGENT HINT: Enable this when you plan to modify the callers."`
}

// DepsOutput is the structured response from the unified
// lang.go.deps tool. Carries dependency relationships keyed by
// include-type name (`callers`, `implementations`, `references`,
// `invocations`) so a single response can answer multiple
// relationship questions for one target symbol.
//
// The four per-relationship tools return [DepsResult] instead; this
// shape exists for the legacy combined entrypoint.
type DepsOutput struct {
	// Symbol is the target symbol that was traced, echoed from the
	// input for self-describing responses.
	Symbol string `json:"symbol" jsonschema:"The target symbol that was traced"`
	// Relationships is the map of include-type name to discovered
	// references. Keys are the Include strings the agent requested:
	// `callers`, `implementations`, `references`, `invocations`. Values
	// are flat slices of [DepReference] hits.
	Relationships map[string][]DepReference `json:"relationships" jsonschema:"Keyed by include type"`
	// Truncated reports whether the response was cut to fit the
	// request's MaxOutputTokens budget. When true, inspect the
	// BloatPrevention logs or narrow the search to recover the dropped
	// entries.
	Truncated bool `json:"truncated,omitempty" jsonschema:"True if output was cut to fit token budget"`
	// NextActions carries up to [MaxNextActions] follow-up tool-call
	// suggestions — typically a lang.go.explore on a specific caller
	// or implementor.
	NextActions []NextAction `json:"next_actions,omitempty" jsonschema:"Suggested follow-up tool calls"`
}

// DepReference is one entry in a dependency tracing result —
// caller, implementor, reference site, or invocation site
// depending on which deps tool produced it.
//
// The shape is the same across tools so [DepsResult] and
// [DepsOutput] can share it. Each DepReference carries the
// referencing symbol's identity (Symbol, Package, Location, Kind),
// a one-line call snippet, the containing function's name and doc,
// an inferred causal context, and (when AutoExplore was enabled)
// the full source of the referencing function.
//
// Fields beyond Symbol/Package/Location are populated according to
// the requested Detail level — see [DetailFlags] for the matrix.
type DepReference struct {
	// Symbol is the name of the referencing symbol (the caller's
	// function name, the implementor's type name, etc.).
	Symbol string `json:"symbol" jsonschema:"Referenced symbol name"`
	// Package is the import path of the package containing Symbol.
	Package string `json:"package" jsonschema:"Package path containing the referenced symbol"`
	// Location is the reference site in `file:line` form, relative to
	// the module root. Points at the actual call or use, not the
	// declaration of Symbol.
	Location string `json:"location" jsonschema:"file:line"`
	// Kind classifies the relationship: one of [RelDirect],
	// [RelTransitive], or [RelInterface]. Drives whether the agent
	// should treat the relationship as a strong (direct) or weak
	// (transitive) coupling.
	Kind string `json:"kind" jsonschema:"direct, transitive, interface"`
	// CallSnippet is the calling line plus up to two lines of context
	// below — typically enough to capture the surrounding error
	// handling or assignment. Useful for understanding how the caller
	// uses the target result without exploring the full function.
	CallSnippet string `json:"call_snippet" jsonschema:"The calling line plus 2 lines of context below (typically error handling). AGENT HINT: Use this to understand HOW the caller uses the result without exploring the full function."`
	// CallerSymbol is the function or method that contains this
	// reference. Pass to lang.go.explore for the full caller
	// implementation. Empty when the reference is at package scope
	// (e.g. a `var` initialiser).
	CallerSymbol string `json:"caller_symbol" jsonschema:"The function/method containing this reference. AGENT HINT: Pass to lang.go.explore for full caller implementation."`
	// CallerDocblock is the doc comment of CallerSymbol with `//`
	// markers stripped. Explains the semantic intent of the caller —
	// why it uses the target symbol. Populated at [DetailFull]; empty
	// at lower detail levels.
	CallerDocblock string `json:"caller_docblock,omitempty" jsonschema:"Doc comment of the calling function. Explains the semantic intent of the caller — WHY it uses the target symbol."`
	// Context is the inferred causal reason for the dependency, drawn
	// from nearby comments or the caller's doc. Explains why this
	// dependency exists in human terms — useful for the agent when
	// deciding whether to break or preserve the relationship.
	Context string `json:"context" jsonschema:"Inferred causal reason from nearby comments or function doc. Explains WHY this dependency exists."`
	// SymbolSource is the full implementation of the caller or
	// implementor, populated only when the request's AutoExplore flag
	// was true. Use to understand and modify the caller without a
	// follow-up explore call.
	SymbolSource string `json:"symbol_source,omitempty" jsonschema:"Full implementation of the caller/implementor when auto_explore is enabled. AGENT HINT: Use this to understand and modify the caller without a follow-up explore call."`
}

// WorkspaceInput is the request shape for lang.go.workspace — the
// canonical "first call when encountering a Go project" tool. The
// handler discovers the enclosing Go boundary (a go.work file in
// multi-module mode, falling back to the nearest go.mod), enumerates
// every package under it, and returns a single map keyed by import
// path with just enough metadata to plan the next drilling step.
//
// The contract is deliberately narrow: no symbol filters, no per-symbol
// implementation extraction, no token budget. The output is a directory
// of the workspace, not a substitute for [ExploreInput]. Once the agent
// has chosen a target package from the returned map, the natural
// follow-up is [ExploreInput] for an API surface scan or
// [SearchInput] for a name lookup.
//
// Detail levels trade verbosity for token cost in a predictable way:
//   - [DetailSummary] returns only import paths, package names, and
//     symbol counts — the smallest possible inventory, suitable for
//     workspaces with many hundreds of packages.
//   - [DetailStandard] (the default) adds the first paragraph of each
//     package's godoc comment and the in-source order of exported
//     symbol names. The right balance for the typical 10–50-package
//     workspace.
//   - [DetailFull] additionally renders one summary line per exported
//     symbol — function signatures with their first doc sentence, type
//     kinds with their first doc sentence — so the agent can decide
//     which packages and symbols to explore without a second round
//     trip. Use it sparingly on large workspaces; the per-symbol cost
//     is comparable to [ModeDocs] on every package at once.
type WorkspaceInput struct {
	// Package is the workspace anchor. Discovery walks UP from this
	// directory looking for the outermost go.work file (preferred) or
	// the closest enclosing go.mod (fallback). Empty means the current
	// working directory — the right default when an agent has just
	// landed in a project. Filesystem paths are accepted; import paths
	// are not, because the handler needs a directory to anchor
	// discovery.
	Package string `json:"package,omitempty" jsonschema:"Workspace anchor; defaults to current directory. Discovery walks UP from here to find go.work or go.mod."`
	// IncludePrivate adds unexported (lowercase-leading) top-level
	// symbol counts to each WorkspacePackage entry via InternalSyms.
	// Default false — the public API surface is usually what the agent
	// is mapping, and unexported counts can be misleading on packages
	// with large internal helpers. Enable when planning a refactor that
	// will touch package internals.
	IncludePrivate bool `json:"include_private,omitempty" jsonschema:"Include unexported symbol counts. Default: false."`
	// IncludeTests adds *_test.go files to the symbol counts and emits
	// synthetic test-only packages (the [foo/bar.test] variants from
	// packages.Load). Default false — agents that are mapping a
	// codebase rarely want test code competing for output budget. Enable
	// when investigating test coverage or test-package structure.
	IncludeTests bool `json:"include_tests,omitempty" jsonschema:"Include *_test.go files in counts and synthetic test packages. Default: false."`
	// Detail controls response verbosity. [DetailSummary] is the
	// smallest possible inventory (counts only); [DetailStandard] (the
	// default) adds package_doc and the in-source order of exported
	// symbol names; [DetailFull] additionally emits one summary line
	// per exported symbol. See the type-level comment for the trade-offs.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity: 'summary' = packages + counts only, 'standard' (default) = + package_doc + symbol_order, 'full' = + one-line-per-symbol summary."`
}

// WorkspaceOutput is the response shape for lang.go.workspace. It is
// structured as a single flat map keyed by import path so the agent
// can index into it without a directory walk — the workspace
// equivalent of an LSP-style symbol table for packages rather than
// declarations.
//
// The Modules slice exists so callers can tell, in one read, whether
// the workspace is single-module or go.work-multi-module without
// inspecting paths. IsGoWork repeats the same information at boolean
// granularity for callers that only branch on layout.
//
// The Packages map deliberately excludes vendored dependencies
// (anywhere under a `vendor/` directory) and, by default, synthetic
// test-only packages — both would dilute the inventory with code the
// agent did not author. The IncludeTests toggle re-enables the test
// variants when needed.
type WorkspaceOutput struct {
	// Root is the absolute filesystem path of the workspace root. In
	// go.work mode this is the directory containing go.work; in
	// single-module mode it is the directory containing go.mod.
	Root string `json:"root" jsonschema:"Workspace root directory (the dir containing go.work or go.mod)."`
	// IsGoWork is true when the workspace was discovered from a go.work
	// file. False for single-module workspaces. Callers that need to
	// behave differently for the two layouts can branch on this without
	// parsing paths.
	IsGoWork bool `json:"is_go_work" jsonschema:"True if the workspace uses a go.work file with multiple modules."`
	// Modules is one entry per module under the workspace root. In
	// single-module mode the slice has exactly one entry whose Dir
	// equals Root; in go.work mode it has one entry per 'use' directive.
	// Sorted by module path for deterministic iteration.
	Modules []WorkspaceModule `json:"modules" jsonschema:"One entry per module under the workspace root."`
	// Packages is keyed by import path. Excludes vendored dependencies
	// (anywhere under a `vendor/` directory) and, unless IncludeTests
	// is set on the request, the synthetic test-only packages emitted
	// by packages.Load. Iterate alphabetically (or sort the keys
	// yourself) for stable output.
	Packages map[string]WorkspacePackage `json:"packages" jsonschema:"Keyed by import path. Excludes vendored dependencies."`
}

// WorkspaceModule describes a single Go module discovered inside a
// workspace, returned in [WorkspaceOutput.Modules]. Path is the logical
// import root declared by the module's go.mod; Dir is the physical
// filesystem directory containing that go.mod.
//
// In go.work-multi-module workspaces, multiple WorkspaceModule entries
// appear — one per 'use' directive. In single-module workspaces, the
// slice has exactly one entry whose Dir equals [WorkspaceOutput.Root].
type WorkspaceModule struct {
	// Path is the module path declared in go.mod (the 'module <path>'
	// directive). Other modules import this module by this path.
	Path string `json:"path" jsonschema:"Module path declared in go.mod (e.g. 'go.thesmos.sh/techne')."`
	// Dir is the absolute filesystem path of the module root — the
	// directory containing the module's go.mod.
	Dir string `json:"dir" jsonschema:"Filesystem directory containing the go.mod."`
}

// WorkspacePackage is the per-package entry returned by lang.go.workspace.
// It carries just enough metadata for the agent to decide which packages
// merit a follow-up [ExploreInput] call: the import path and name (for
// identification), the directory (for filesystem operations), the first
// paragraph of the package's godoc (for intent), and an exported-symbol
// count (for size).
//
// Two optional slices appear at higher detail levels. SymbolOrder lists
// exported symbol names in source order, which preserves the author's
// intended reading sequence — typically the most important types and
// constructors cluster near the top of the first file. SymbolLines, set
// only at [DetailFull], renders one line per exported symbol with its
// signature or kind plus the first sentence of its doc comment, capped
// at roughly 120 characters per line and skipped entirely when a symbol
// has no doc (the handler never fabricates documentation).
type WorkspacePackage struct {
	// ImportPath is the full import path of the package, repeated from
	// the map key for self-describing responses.
	ImportPath string `json:"import_path" jsonschema:"Full import path of the package."`
	// Name is the Go package name from the 'package X' clause. Usually
	// matches the last path segment of ImportPath; differs for packages
	// like `main` or those that intentionally rename (e.g. `package v1`
	// in `.../api/v1`).
	Name string `json:"name" jsonschema:"Go package name (the 'package X' clause)."`
	// Dir is the absolute filesystem directory containing the package's
	// source files. Useful as an anchor for filesystem tools or as
	// input to [ExploreInput] when the agent prefers paths over
	// import paths.
	Dir string `json:"dir" jsonschema:"Filesystem directory."`
	// PackageDoc is the first paragraph of the package's godoc comment
	// — the comment attached to the 'package X' clause in the first
	// file that declares one. Empty when the package is undocumented.
	// Present at [DetailStandard] and [DetailFull].
	PackageDoc string `json:"package_doc,omitempty" jsonschema:"First paragraph of the package's godoc comment, or empty when none."`
	// ExportedSyms counts top-level exported declarations: functions,
	// methods, types, consts, and vars whose name starts with an
	// uppercase letter. Always populated. Use as a rough "is this
	// package big?" signal before drilling.
	ExportedSyms int `json:"exported_syms" jsonschema:"Count of exported top-level symbols."`
	// InternalSyms counts top-level unexported declarations using the
	// same kinds as ExportedSyms. Populated only when the request set
	// IncludePrivate true; otherwise omitted from the JSON.
	InternalSyms int `json:"internal_syms,omitempty" jsonschema:"Count of unexported top-level symbols. Populated only when include_private is true."`
	// SymbolOrder lists exported top-level symbol names in source
	// order across the package's files. Preserves the author's
	// intended reading sequence — typically the most important types
	// appear first. Present at [DetailStandard] and [DetailFull].
	SymbolOrder []string `json:"symbol_order,omitempty" jsonschema:"Top-level exported symbol names in source order. Present in 'standard' and 'full' detail modes."`
	// SymbolLines renders one human-readable line per exported symbol:
	// for functions and methods the signature plus the first sentence
	// of the doc comment, for types the kind ('struct'/'interface'/etc.)
	// plus the first sentence of the doc. Lines are capped at roughly
	// 120 characters and symbols without doc comments are omitted (the
	// handler never fabricates prose). Present only at [DetailFull].
	SymbolLines []string `json:"symbol_lines,omitempty" jsonschema:"One-line summary per exported symbol ('FuncName(args) returns ...' or 'TypeName struct/interface — brief godoc'). Present only in 'full' detail mode."`
}
