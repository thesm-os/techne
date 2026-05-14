// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package rust

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Deps is the lang.rust.deps tool. It is intended to map symbol
// dependencies in a Rust crate — finding implementations of a trait,
// callers of a function, and references to a type or const, with the
// same causal-context output shape exposed by the Go equivalents
// (lang.go.callers, lang.go.implementations, lang.go.references,
// lang.go.invocations).
//
// The symbol is currently a stub registered via tool.Stub: invoking it
// over MCP / CLI returns a structured "not implemented" response with
// the declared input/output schemas, so agents can still discover the
// tool surface and plan around it. A full implementation will require a
// proper Rust semantic-analysis backend; the most likely path is to
// drive rust-analyzer's LSIF/SCIP export and merge it with the
// tree-sitter syntactic index already produced by [Explore].
//
// Until that lands, agents that need Rust dependency information must
// fall back to literal Grep — noting that, unlike Go, name-based
// searches in Rust suffer from heavy macro/trait indirection and may
// miss large classes of references.
var Deps = tool.Stub[lang.DepsInput, lang.DepsOutput](
	"lang.rust.deps",
	"Map symbol dependencies in a Rust project. Find implementations, callers, and references with causal context.",
	tool.WithShortDescription("Map Rust symbol dependencies — trait impls, callers, references (stub)"),
)
