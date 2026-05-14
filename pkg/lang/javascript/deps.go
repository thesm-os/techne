// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package javascript

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Deps is the lang.javascript.deps tool. It is intended to map symbol
// dependencies in a JavaScript project — callers of a function,
// references to a class or named export, implementations of a
// "duck-typed interface" (i.e. classes/objects with a matching
// method set), and invocations of callables passed as values — with
// the same causal-context shape produced by the Go equivalents.
//
// The symbol is currently a stub registered via tool.Stub. The eventual
// implementation is expected to share its semantic backend with
// lang.typescript.deps (the TypeScript compiler API parses .js files
// with `allowJs: true`), driven through a long-running Node subprocess.
// This lets the tool re-use tsserver's reference index, which already
// handles ES modules, CommonJS, default exports, and re-exports.
//
// JavaScript-specific complications the implementation must handle:
// the absence of static types means "references to a method" is
// inherently a name-match (the resolver cannot distinguish two unrelated
// classes both with a `.send()` method); dynamic property access via
// computed keys is opaque to static analysis; and bundlers may rewrite
// imports in ways the source-level resolver does not see. Callers
// should treat the eventual deps output as a high-quality starting set,
// not a sound under-approximation.
var Deps = tool.Stub[lang.DepsInput, lang.DepsOutput](
	"lang.javascript.deps",
	"Map symbol dependencies in a JavaScript project. Find implementations, callers, and references with causal context.",
	tool.WithShortDescription("Map JavaScript symbol dependencies — callers, references, implementations (stub)"),
)
