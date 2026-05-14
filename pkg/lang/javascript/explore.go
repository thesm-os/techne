// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package javascript

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Explore is the lang.javascript.explore tool. It is intended to walk a
// JavaScript package (a directory containing .js, .mjs, .cjs, and/or
// .jsx files alongside a package.json) and return the syntactic items
// declared there — ES module exports, classes, functions, methods,
// react components, top-level const/let bindings, and CommonJS
// `module.exports` assignments — each annotated with file:line,
// JSDoc, signature, and (in code mode) source body.
//
// The symbol is currently a stub registered via tool.Stub: invocation
// returns the declared schemas with a "not implemented" status. A full
// implementation will share infrastructure with [pkg/lang/typescript].Explore
// (JavaScript is parsed as TypeScript with `allowJs: true`,
// `checkJs: false`, so JSDoc type annotations are recognised but
// untyped code is not rejected). The most likely backend is the
// typescript compiler API hosted in a long-running Node subprocess,
// driven via JSON RPC — this gives access to the same symbol table
// used by tsserver, including JSDoc-derived types.
//
// Notable JS-specific complications the implementation must handle:
// the split between ES module and CommonJS export idioms; React/JSX
// that is syntactically invalid plain JS; dynamic property assignment
// on `module.exports`; and the fact that "all exported symbols" depends
// on the consumer's module resolution mode (`type: module` in
// package.json vs not).
//
// Until this is built, agents exploring a JavaScript codebase should
// fall back to Read for individual files and Grep for cross-file
// searches.
var Explore = tool.Stub[lang.ExploreInput, lang.ExploreOutput](
	"lang.javascript.explore",
	"Explore symbols in a JavaScript package. Returns classes, functions, and modules, and methods with configurable verbosity.",
	tool.WithShortDescription("Extract JavaScript classes, functions, modules, and methods (stub)"),
)
