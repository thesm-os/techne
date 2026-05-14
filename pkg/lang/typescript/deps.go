// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package typescript

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Deps is the lang.typescript.deps tool. It is intended to map symbol
// dependencies in a TypeScript project — callers of a function,
// references to a class/type/enum, implementations of an interface or
// abstract class, and invocations of callable types passed as values
// — with the same causal-context shape produced by the Go equivalents.
//
// The symbol is currently a stub registered via tool.Stub. The eventual
// implementation will lean on the TypeScript compiler API
// (`findReferences`, `getImplementations` on a tsserver-backed program)
// driven from a long-running Node subprocess. This gives full type-aware
// answers: "who implements `Logger`?" returns nominal `implements`
// declarations *and* structural subtypes, because the compiler considers
// any object literal assignable to the interface to be an
// implementation. "References to a method" correctly distinguishes
// between two unrelated classes with the same method name.
//
// TypeScript-specific complications the implementation must handle:
// declaration merging (a single name may resolve to multiple
// declarations across .ts and .d.ts files); module-path remapping via
// tsconfig `paths`; symbols imported via barrel re-exports must be
// traced through to their original declaration; and generic
// instantiations may produce "phantom" references that an unaware
// consumer would not realise are the same symbol. Until the tool ships,
// callers should drive tsserver directly or fall back to Grep — noting
// that the latter has the duck-typing limitation absent in TS proper.
var Deps = tool.Stub[lang.DepsInput, lang.DepsOutput](
	"lang.typescript.deps",
	"Map symbol dependencies in a TypeScript project. Find implementations, callers, and references with causal context.",
)
