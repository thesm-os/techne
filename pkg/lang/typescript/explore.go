// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package typescript

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Explore is the lang.typescript.explore tool. It is intended to walk a
// TypeScript package (a directory containing .ts/.tsx/.mts/.cts files
// alongside a tsconfig.json or package.json) and return its syntactic
// items — interfaces, classes, type aliases, enums, namespaces,
// exported functions, react components, top-level const/let bindings,
// and declaration-file ambient types — each annotated with file:line,
// TSDoc, signature, and (in code mode) source body.
//
// The symbol is currently a stub registered via tool.Stub: invocation
// returns the declared schemas with a "not implemented" status. A full
// implementation will most likely host the TypeScript compiler API
// (`typescript` package, accessed via createProgram / getSemanticDiagnostics)
// in a long-running Node subprocess and communicate via JSON RPC, which
// lets the tool re-use tsserver's symbol table — the same one VS Code
// uses — for accurate type-aware extraction including type aliases
// resolved through generics and conditional types. The alternative,
// lexical-only tree-sitter parsing (as used by the Rust
// [pkg/lang/rust].Explore),
// would be much faster but would miss interface inheritance, mapped
// types, and template-literal types.
//
// Mode (docs/skeleton/code) and IncludePrivate semantics match the
// shared [lang.ExploreInput] contract. "Private" in TS means the
// `private` access modifier on class members and the absence of `export`
// on top-level declarations — both will be filtered out when
// IncludePrivate is false.
//
// Notable TS-specific complications the implementation must handle:
// barrel re-exports (`export * from './foo'`) that synthesise symbols
// in the importing module; ambient `.d.ts` files that declare modules
// without an implementation; declaration merging (interfaces and
// namespaces with the same name); and tsconfig path mappings, which
// the compiler API will resolve correctly only if tsconfig.json is
// discovered and passed in.
//
// Until this is built, agents should fall back to Read + Grep, accepting
// that .d.ts merging and re-exports may hide symbols.
var Explore = tool.Stub[lang.ExploreInput, lang.ExploreOutput](
	"lang.typescript.explore",
	"Explore symbols in a TypeScript package. Returns classes, functions, and interfaces, and methods with configurable verbosity.",
)
