// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package python

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Explore is the lang.python.explore tool. It is intended to surface
// the syntactic structure of a Python package — modules, classes,
// top-level functions, methods (including @classmethod / @staticmethod),
// decorators, and module-level constants — each annotated with its
// file:line, docstring, signature, and (in code mode) source body, in
// the same shape as the Go and Rust explore tools.
//
// The symbol is currently a stub registered via tool.Stub: invoking it
// over MCP / CLI returns the declared input/output schemas and a "not
// implemented" status, which is enough for agents to discover the
// future surface. A full implementation will most likely use the
// standard-library `ast` module via a subprocess (Python's tree-sitter
// grammar is also viable but loses semantic niceties like resolving
// @dataclass-generated fields). The subprocess approach inherits
// Python's portability story: the interpreter must be on PATH or
// explicitly configured, and "works on uncompilable code" depends on
// whether the file is syntactically valid — `ast.parse` raises on any
// SyntaxError.
//
// Until this is built, agents exploring a Python codebase should fall
// back to Read for individual files and Grep for cross-file searches,
// accepting that decorator stacks and dynamic attribute assignment may
// hide symbols that a future AST-based explore would surface.
var Explore = tool.Stub[lang.ExploreInput, lang.ExploreOutput](
	"lang.python.explore",
	"Explore symbols in a Python package. Returns classes, functions, and decorators, and methods with configurable verbosity.",
)
