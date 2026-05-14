<!--
SPDX-License-Identifier: MIT
Copyright Thesmos B.V. 2026
-->

# techne available

This project has the `lang.go.*` toolset registered (25 tools for Go
navigation, refactor, doc generation, and verification — typically via
the `techne serve --tools=lang.go` MCP server). They are
type-checked, workspace-aware (`go.work` native), and atomic: your edit
either commits with `go build` passing or every change rolls back.

**When working with existing Go code, check the `lang.go.*` tool list
before reaching for Grep / Read / Edit / Bash.** Each tool's
description includes a "PREFER OVER ..." line that says when to pick
it. Common matchups:

- Symbol search (fuzzy or by intent) → `lang.go.search` /
  `lang.go.search_explore` / `lang.go.explore`
- Who calls / what implements / where referenced / invocations of a
  func type → `lang.go.{callers,implementations,references,invocations}`
- Rename, signature change, type swap, move, extract, inline →
  `lang.go.{rename,change_signature,change_type,move_symbol,move_file,move_package,extract_function,extract_interface,extract_variable,implement_interface,inline_constant}`
- Bulk doc comments (with godoc validation, `[Symbol]` link checking,
  line wrap) → `lang.go.document`
- Scaffold Go test functions (top-level + subtests, each with
  `t.Parallel()` and a `t.Skip("not implemented")` stub) →
  `lang.go.add_tests`
- Build-verified text edits with atomic rollback → `lang.go.patch`
- Lint / test → `lang.go.verify` (or `lang.go.fix` for the lint →
  apply suggested patches → re-verify loop)

Generic tools (Read / Edit / Grep / Bash) are still right for writing
brand-new code, one-off edits, and non-Go work.

**`detail` field** on query and verify tools: `summary` (~80% smaller
output), `standard` (default), `full` (AST forensics + caller source,
for deep debugging). Pick the smallest level that answers your
question.

**Reflection caveat:** the static refactors can't see references hidden
in string literals like `reflect.ValueOf(x).MethodByName("Process")`.
After renaming an exported method or field, manually grep for
`MethodByName` / `FieldByName` callers and update them.
