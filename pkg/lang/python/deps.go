// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package python

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Deps is the lang.python.deps tool. It is intended to map symbol
// dependencies in a Python project — callers of a function, references
// to a class or attribute, implementations of an abstract base class or
// Protocol, and invocations of callables passed as values — with the
// same causal-context shape produced by the Go equivalents.
//
// The symbol is currently a stub registered via tool.Stub: agents see
// the declared schemas but invocations return "not implemented". A full
// implementation will need a Python-aware type/reference resolver; the
// pragmatic choice is jedi or rope, both of which already parse and
// type-infer real-world Python code. Pyright produces richer JSON
// but runs as a Node subprocess, which complicates the build.
//
// Notable Python-specific complications that the future implementation
// must handle: dynamic attribute creation (`setattr`, `__getattr__`),
// duck typing (callers may not import the symbol they call), and the
// fact that "implementations of an interface" maps onto two distinct
// concepts — nominal subclasses of an ABC, and structural subtypes
// of a Protocol. Until this is built, callers should fall back to Grep,
// noting these limitations.
var Deps = tool.Stub[lang.DepsInput, lang.DepsOutput](
	"lang.python.deps",
	"Map symbol dependencies in a Python project. Find implementations, callers, and references with causal context.",
	tool.WithShortDescription("Map Python symbol dependencies — callers, references, implementations (stub)"),
)
