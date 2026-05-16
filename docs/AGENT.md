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
- "What's in this file/package?" at-a-glance index →
  `lang.go.explore` with `mode=overview` (symbol order + kind + a
  one-line summary, ~500 tokens)
- Who calls / what implements / where referenced / invocations of a
  func type → `lang.go.{callers,implementations,references,invocations}`
  — workspace-local by default; pass `include_external=true` to
  include stdlib + transitive deps. Sibling modules in a `go.work`
  tree are always considered workspace-local
- Distinguish direct calls from value-uses → `lang.go.callers` with
  `kind=call` (default, `f(args)` sites), `kind=value` (`cb := f`,
  `g(f)`, `return f` — function used as a value), or `kind=all`
- Rename a top-level symbol, OR a function/method parameter that
  shadows an imported package (the `func F(kind kind.Kind)` case),
  OR a local variable → `lang.go.rename` (top-level: `symbol` alone;
  local/param: `symbol` + `file` + `line` at the defining identifier)
- Signature change, type swap, move, extract, inline →
  `lang.go.{change_signature,change_type,move_symbol,move_file,move_package,extract_function,extract_interface,extract_variable,implement_interface,inline_constant}`
  — `change_signature` supports `add_params` / `add_returns` /
  `remove_params` (by name) / `remove_returns` (by type, left-to-right)
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

**`dry_run` field** on every refactor tool: previews the change
without writing to disk AND runs the build gate against the
post-change projection via `go build -overlay`. A response with
`build_status: pass` guarantees the real commit will compile — you
can apply confidently without a follow-up verify. A `dry_run` failure
returns the same compiler diagnostic a real run would.

**Reflection caveat:** the static refactors can't see references hidden
in string literals like `reflect.ValueOf(x).MethodByName("Process")`.
After renaming an exported method or field, manually grep for
`MethodByName` / `FieldByName` callers and update them.
