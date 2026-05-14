// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package lang

// Per-tool input types for the four narrow dependency-tracing tools that
// replaced the old union-shaped lang.go.deps. Each tool answers exactly one
// question — who calls this, what implements this, who references this,
// where is this called through a function value — so the agent's schema
// view shows only the fields relevant to the question being asked.

// CallersInput is the agent-facing request for lang.go.callers — the
// "who calls this function?" query.
//
// Callers answers one specific question: given a function or method,
// which functions and methods in the workspace call it? It is the
// type-aware replacement for grepping `Symbol(` — the latter misses
// method calls through interface variables and produces false positives
// on symbols whose names overlap with other identifiers.
//
// Callers is a strict subset of [DepsInput] with Include=["callers"];
// the narrow shape exists so the agent's schema-driven prompt sees only
// fields that bear on the question. Use [ReferencesInput] when you need
// to know every read or write of a symbol (including non-call uses),
// [InvocationsInput] when the target is a function-typed value, or
// [ImplementationsInput] when the target is an interface.
type CallersInput struct {
	// Symbol identifies the function or method whose call sites to locate.
	// Use the bare name for package-level functions (`NewUser`) and
	// `Receiver.Method` for methods (`Engine.Execute`). The receiver may be
	// the value or pointer name; the tool's symbol resolver canonicalises
	// both forms.
	Symbol string `json:"symbol" jsonschema:"Function or method to find callers of. Example: 'NewUser' or 'Engine.Run'."`
	// Package scopes the lookup to a single package, given as an import path
	// (`go.thesmos.sh/techne/pkg/lang`) or a workspace-relative directory
	// (`./pkg/lang`). When empty the tool searches the current working
	// directory, which is typically the package the agent is editing — the
	// fastest and least-ambiguous case. Set Package only when the target
	// symbol lives elsewhere.
	Package string `json:"package,omitempty" jsonschema:"Package import path or relative path containing the symbol. Defaults to the current directory."`
	// Limit caps the number of caller sites returned. Defaults to 50 when
	// zero. A small cap keeps the response within an LLM's effective
	// context; if the response sets Truncated=true the agent should narrow
	// by Package or raise Limit explicitly rather than scrolling blindly.
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of caller sites to return. Default: 50."`
	// Detail controls how much per-caller information is returned. Use
	// "summary" for a flat list of symbol/package/location triples (cheapest
	// when you only need to know whether anything calls the target);
	// "standard" (default) adds a one-line call snippet and the containing
	// function name; "full" adds the calling function's docblock and an
	// inferred causal context note explaining why the call exists.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity: 'summary' (symbol/package/location only), 'standard' (default — adds call snippet + caller symbol), 'full' (adds caller docblock + inferred causal context)."`
}

// ImplementationsInput is the agent-facing request for
// lang.go.implementations — the "what concrete types satisfy this
// interface?" query.
//
// Implementations answers a question that pure text search cannot:
// identifying every named type in the workspace whose method set
// satisfies a given interface. Go's structural typing makes this
// impossible to compute from grep alone — a type need not name the
// interface in its source to satisfy it.
//
// Like its sibling deps tools, this is a narrow projection of
// [DepsInput] (Include=["implementations"]). Use [CallersInput] when the
// target is a regular function and you want call sites instead.
type ImplementationsInput struct {
	// Symbol identifies the interface whose implementors to list. Bare name
	// for interfaces declared in the target package (`Reader`); use
	// qualified names like `http.Handler` only when IncludeExternal is
	// true and the interface lives outside the workspace.
	Symbol string `json:"symbol" jsonschema:"Interface to find implementors of. Example: 'Reader' or 'http.Handler'."`
	// Package scopes the search for the interface declaration to a single
	// package, given as an import path or a workspace-relative directory.
	// Empty means the current working directory. Note that this scopes
	// *where the interface is defined*, not where implementations live —
	// implementations are searched across the whole workspace regardless.
	Package string `json:"package,omitempty" jsonschema:"Package containing the interface. Defaults to the current directory."`
	// IncludeExternal expands the implementation search beyond the
	// workspace into external Go modules and the standard library. Default
	// false, which restricts hits to the project's own packages — the
	// common case when refactoring local code. Enable when investigating
	// how a stdlib interface like `io.Reader` is implemented or when
	// tracing an external interface a third-party library exports.
	IncludeExternal bool `json:"include_external,omitempty" jsonschema:"Include implementors from external packages and the standard library. Default: false (project-local only)."`
	// Limit caps the number of implementations returned. Defaults to 50
	// when zero. Widely-implemented interfaces (e.g. `io.Reader` with
	// IncludeExternal=true) can produce thousands of hits; the cap exists
	// to keep the response within an agent's context budget.
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of implementations to return. Default: 50."`
	// Detail controls per-implementor verbosity. "summary" returns just the
	// type name, package, and source location; "standard" (default) adds a
	// doc-block snippet so the agent can pick the right implementation
	// without opening every file; "full" inlines the full doc comment and
	// relevant source — costly but useful when the agent will subsequently
	// modify or extend the implementor.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity: 'summary' (symbol/package/location only), 'standard' (default — adds doc context), 'full' (adds full doc and source)."`
}

// ReferencesInput is the agent-facing request for lang.go.references —
// the "where is this identifier read or written?" query.
//
// References is the broadest of the deps tools: it returns every site
// where the named identifier is *used*, including non-call uses like
// type declarations, struct field access, and identifier reads. Where
// [CallersInput] is the right tool when you only care about call sites,
// References is the right tool when you are about to rename or remove
// the symbol and need to know every site that will break.
//
// Like its siblings, References is a narrow projection of [DepsInput]
// (Include=["references"]).
type ReferencesInput struct {
	// Symbol identifies the identifier to trace. Accepts top-level names
	// (`MaxRetries`), method qualifications (`Engine.Execute`), field
	// qualifications (`Config.Timeout`), and type names (`Reader`). The
	// resolver requires that the name parse as a single Go identifier or a
	// dotted qualifier — arbitrary expressions are not supported.
	Symbol string `json:"symbol" jsonschema:"Identifier to find references to. Example: 'Reader' or 'maxRetries'."`
	// Package scopes where the *definition* of Symbol is located, as an
	// import path or a workspace-relative directory. Empty means the
	// current working directory. The reference search itself spans the
	// whole workspace; this field only narrows the symbol resolution step,
	// which matters when the same bare name (`Run`, `New`) is defined in
	// multiple packages.
	Package string `json:"package,omitempty" jsonschema:"Package containing the symbol. Defaults to the current directory."`
	// Limit caps the number of reference sites returned. Defaults to 50
	// when zero. Reference counts on widely-used identifiers (`error`,
	// `string`, `nil`) can run into the thousands; the cap protects the
	// agent's context. If Truncated=true on the response, narrow the
	// search via Package or use [CallersInput] / [InvocationsInput] for a
	// more focused question.
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of references to return. Default: 50."`
	// Detail controls how much context each reference carries. "summary"
	// returns symbol/package/location only; "standard" (default) adds the
	// referring code snippet and the containing function name so the agent
	// can judge each site's significance without opening files; "full"
	// adds the containing function's docblock and inferred causal context.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity: 'summary' (symbol/package/location only), 'standard' (default — adds call snippet + caller symbol), 'full' (adds caller docblock + context)."`
}

// InvocationsInput is the agent-facing request for lang.go.invocations
// — the "where is a value of this function-type called?" query.
//
// Invocations exists for a niche but important case that [CallersInput]
// cannot handle: when the target is a function-typed *value* (e.g.
// `type Handler func(...)`), [CallersInput] would find the type
// references and not the call sites. Invocations finds calls through
// any variable typed as Symbol, regardless of how the variable is
// named.
//
// Distinct from:
//   - [CallersInput], which matches by function name and misses calls
//     through interface or function-typed values.
//   - [ReferencesInput], which finds type-name uses but does not detect
//     invocations.
//
// Use this when refactoring callback signatures, hook types, middleware
// types, or any first-class function type.
type InvocationsInput struct {
	// Symbol identifies the named function type whose invocations to
	// locate, e.g. `Handler` from `type Handler func(http.ResponseWriter,
	// *http.Request)`. The target must be a `func` type declaration; using
	// a regular function name here would return zero hits (use
	// [CallersInput] for that).
	Symbol string `json:"symbol" jsonschema:"Function-type symbol whose invocation sites to locate. Example: 'Handler' for a 'type Handler func(...)' definition."`
	// Package scopes where the function-type declaration lives, as an
	// import path or workspace-relative directory. Defaults to the current
	// working directory. The invocation search itself spans the whole
	// workspace; this field only resolves the type.
	Package string `json:"package,omitempty" jsonschema:"Package containing the function-type symbol. Defaults to the current directory."`
	// Limit caps the number of invocation sites returned. Defaults to 50
	// when zero. Hot callback types (HTTP handlers, middleware) can have
	// hundreds of invocation sites — narrow by Package or raise Limit when
	// Truncated=true.
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of invocation sites to return. Default: 50."`
	// Detail controls how much context each invocation carries. "summary"
	// returns symbol/package/location; "standard" (default) adds a call
	// snippet and the containing function name; "full" adds the containing
	// function's docblock and inferred causal context.
	Detail string `json:"detail,omitempty" jsonschema:"Output verbosity: 'summary' (symbol/package/location only), 'standard' (default — adds call snippet + caller symbol), 'full' (adds caller docblock + context)."`
}

// DepsResult is the shared response shape returned by lang.go.callers,
// lang.go.implementations, lang.go.references, and lang.go.invocations.
// All four queries answer "give me a ranked list of related symbols",
// so they share a single output struct rather than each carrying its
// own wrapper — agents only have to learn one response shape.
//
// The meaning of each [DepReference] in Results depends on which tool
// produced the response: caller, implementor, reference site, or
// invocation site. The kind is also recorded on each [DepReference] via
// its Kind field, so multi-tool batches can disambiguate.
type DepsResult struct {
	// Symbol echoes the input's Symbol verbatim so the response is
	// self-describing — useful when an agent batches multiple deps queries
	// and needs to correlate responses with requests by symbol name.
	Symbol string `json:"symbol" jsonschema:"The symbol that was traced."`
	// Results is the flat list of related symbols the query produced.
	// Ordering is stable but not formally ranked: within each call the
	// order matches the underlying gopls traversal, which is roughly
	// declaration order across files. Treat it as a set with insertion
	// ordering, not as a relevance ranking.
	Results []DepReference `json:"results" jsonschema:"Hits with caller/implementor name, location, surrounding docblock, and a one-line snippet."`
	// Truncated reports whether Results was capped at the request's Limit
	// value. When true, the underlying query found additional hits that
	// were dropped; the agent should either narrow the query (by Package)
	// or raise Limit and re-run. The truncation is at the tail of the
	// result set; there is no API to fetch additional pages.
	Truncated bool `json:"truncated,omitempty" jsonschema:"True if Results was capped at Limit. Increase Limit and re-run for more."`
	// NextActions carries up to [MaxNextActions] follow-up tool-call
	// suggestions tied to the most useful hit — typically a lang.go.explore
	// on the first caller or implementor so the agent can drill into its
	// implementation in a single follow-up turn. May be empty when the
	// response is self-sufficient.
	NextActions []NextAction `json:"next_actions,omitempty" jsonschema:"Suggested follow-up calls — typically lang.go.explore on the most interesting hit."`
}
