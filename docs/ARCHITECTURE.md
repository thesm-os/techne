<!--
SPDX-License-Identifier: MIT
Copyright Thesmos B.V. 2026
-->

# Architecture

This document describes how techne is put together. The intended reader is
someone who wants to add a new tool, change a presenter, or audit the
build-gate / rollback semantics — not a first-time user. For a user-facing
overview, read [`README.md`](../README.md) first.

## Bird's-eye view

```
┌──────────────────────┐      ┌───────────────────┐
│ MCP client           │      │ Human operator    │
│ (Claude, Gemini, …)  │      │ (shell, TUI)      │
└──────────┬───────────┘      └─────────┬─────────┘
           │ JSON-RPC over stdio        │ cobra
           ▼                            ▼
   ┌──────────────────────────────────────────────┐
   │  presenter/{mcp, cli, tui}                   │
   │  – schema-driven flag derivation             │
   │  – input decode + validate                   │
   │  – dispatch to tool.Tool.Execute             │
   │  – output encode                             │
   └────────────────────┬─────────────────────────┘
                        │ tool.Tool interface
                        ▼
   ┌──────────────────────────────────────────────┐
   │  internal/tool registry (init-time wired)    │
   │  Name → Tool, grouped by domain prefix       │
   └────────────────────┬─────────────────────────┘
                        │
   ┌────────────────────┼─────────────────────┐
   ▼                    ▼                     ▼
┌─────────┐    ┌───────────────────┐    ┌────────────────────┐
│ pkg/fs/ │    │ pkg/lang/go/      │    │ pkg/lang/{rust,    │
│         │    │ refactor + analysis│    │ python,js,ts}/     │
└────┬────┘    └────────┬──────────┘    └─────────┬──────────┘
     │                  │                         │
     └──┐         ┌─────┘                  ┌──────┘
        ▼         ▼                        ▼
   ┌──────────────────────┐         ┌──────────────────┐
   │ Transaction abstrac- │         │ Subprocess       │
   │ tion + build gate    │         │ (gopls, cargo,   │
   │ (pkg/lang/go/refac-  │         │ tsc, …)          │
   │ tor)                 │         │                  │
   └──────────────────────┘         └──────────────────┘
```

## Layers

### 1. Tool framework — `internal/tool`

This is the central abstraction every tool consumes and every presenter
depends on.

**`Tool` interface** — exposes `Name()`, `Description()`, `InputSchema()`,
`OutputSchema()`, `Execute(ctx, rawJSON)`. Every tool in the project
satisfies it. Presenters are written against this interface, so adding a
new tool requires zero changes in `presenter/`.

**Generic builder — `New[In, Out any]`** — given a typed handler
`func(ctx, In) (Out, error)`, the builder derives the JSON Schema from
struct tags using `github.com/google/jsonschema-go`. This keeps the
agent-facing schema in sync with the Go types: adding a field to a
`*Input` struct automatically surfaces it in the MCP tool descriptor and
the cobra `--flag` set.

**Registry** — every domain package (`pkg/fs`, `pkg/lang/go`, …) calls
`RegisterTools(prefix, callback)` from `init()` with the list of tools
it exposes. The registry is the single source of truth; presenters
enumerate it at startup.

**Validators / coercion** — schema-typed inputs come in as raw JSON.
`internal/tool/coerce.go` normalises a few common LLM mistakes
(string-encoded numbers, bare strings where `[]string` is expected) so
the agent gets fewer bounce-backs from the validator.

### 2. Presenters — `presenter/`

Each presenter maps the universal `tool.Tool` interface to a transport.

| Presenter | Transport | Input source |
|---|---|---|
| `mcp/` | JSON-RPC over stdio | MCP client (Claude, Gemini, …) |
| `cli/` | argv / `--flag` | shell |
| `tui/` | bubbletea | terminal |

The presenters do **not** carry domain knowledge — they only translate
between their transport and the `Execute(ctx, json.RawMessage)` shape.
The CLI presenter derives flags from the JSON Schema, falling back to
`--input -` (read full JSON from stdin) when a field's type can't be
expressed as a flat flag (array-of-object, nested struct, …).

### 3. Tool implementations — `pkg/`

Domain packages register tools and contain the logic.

**`pkg/fs/`** — atomic filesystem operations. The non-trivial one is
`pkg/fs/patch.go`: it snapshots every targeted file in memory, applies
edits atomically, optionally runs a verify command, and rolls back on
failure. Pattern edits (glob + regex) expand into literal edits and join
the same atomic envelope.

**`pkg/lang/go/`** — Go-aware tools. Each public entry point is a thin
wrapper that converts `lang.*Input` to `refactor.Input` and dispatches to
`pkg/lang/go/refactor`. The wrapper layer exists so the public agent-
facing schemas stay decoupled from internal types and can evolve
independently.

**`pkg/lang/go/refactor/`** — strategy registry. Each refactoring is an
`Action` (rename, change_signature, move_file, …) that implements
`Execute(ctx, input, tx)`. The action operates against a `Transaction`
abstraction; the production implementation
(`WorkspaceTransaction` in `transaction.go`) stages edits in memory,
then on `Commit` writes them atomically, runs `goimports` + `go vet`
+ `go build`, and rolls back on any failure.

**`pkg/lang/go/internal/workspace/`** — unified abstraction over a Go
module or a `go.work` multi-module setup. Every tool that runs
`packages.Load` goes through here so go.work is handled transparently.
The workspace caches its load result keyed by a mtime fingerprint;
subsequent loads against an unchanged tree are O(stat) rather than
O(packages).

**`pkg/lang/{rust,python,javascript,typescript}/`** — stub adapters
following the same shape (`Explore`, `Verify`, `Deps`). Most delegate
to subprocesses (`cargo`, `mypy`, `eslint`, `tsc`); the Rust package
also has a tree-sitter–driven explorer.

### 4. Entry points — `cmd/` + `internal/app`

`cmd/techne/main.go` is a 10-line shim that forwards exit codes. All the
wiring — cobra command tree, persistent flags, version metadata, blank
imports that trigger `init()`-time registration — lives in
`internal/app/app.go`. `Run()` is the single exported function.

`internal/version/` exposes ldflags-stamped build metadata
(`buildVersion`, `buildCommit`, `buildDate`) which the cobra root reads
into its `Version` field for `techne --version`. `goreleaser`
populates these at release time.

## The atomicity model

Every refactor tool in `pkg/lang/go/` is required to be atomic across
multiple files. The transaction abstraction enforces this:

1. **Stage phase.** The action collects every edit, file creation, and
   file deletion into the `WorkspaceTransaction` by calling
   `AddChange`, `AddFileMove`, or `AddDelete`. Each call validates that
   the new content parses as Go (`go/parser`) and runs `goimports` in
   memory. Nothing touches disk yet.

2. **Commit phase.** On `tx.Commit()`:
   1. Stage all deletions (`os.Remove`).
   2. Write all modified files via `fs.AtomicWrite` (temp file + rename).
   3. Snapshot `go.mod` / `go.sum` before any tidy.
   4. Run `go mod tidy` (best-effort — does not abort on failure).
   5. Run `go build ./...` (or per-module patterns in `go.work` mode).
   6. **On failure:** rollback all writes from their in-memory
      snapshots, restore deleted files, restore `go.mod` / `go.sum`,
      return a `Failure` status with the first compiler diagnostic.
   7. **On success:** the workspace is in a verified-buildable state.

The Transaction interface is what makes the refactor actions testable
without a real Go module: a fake `Transaction` implementation captures
the staged edits and asserts on them, while the real
`WorkspaceTransaction` runs the build gate end-to-end.

## What is and isn't updated by a refactor

The static refactor tools (`rename`, `change_signature`, etc.)
correctly handle:

- All references across the workspace, including in sibling modules
  under `go.work`.
- Method dispatch through interfaces.
- Identifier shadowing — operates on the resolved `types.Object`, not
  the text.
- Godoc links of the form `[Symbol]` and `[pkg.Symbol]`.

They **do not** handle (and never will, because the references are
invisible to static analysis):

- References in string literals (`reflect.ValueOf(x).MethodByName("Old")`).
- Struct tags (`json:"old_field"`).
- Names in unrelated comments.

When a refactor crosses one of these boundaries, the tool surfaces a
`Note` in the output advising manual grep — see
`CLAUDE.md`'s "Reflection-based references — manual audit" section.

## Output verbosity

Every query tool accepts a `detail` field with three levels:

| Level | Surface |
|---|---|
| `summary` | counts + paths only (~80% smaller) |
| `standard` | per-file diff snippets, issue context |
| `full` | adds AST forensics, caller source, escape analysis |

Verify and refactor tools follow the same convention. The agent should
pick the smallest level that answers its question.

## Concurrency model

- **Tool instances are shared** across concurrent presenter invocations.
  Handlers must be reentrant and stateless beyond their input/output.
- **The workspace cache** (`internal/workspace`) is safe for concurrent
  use; a mutex serialises only the fingerprint comparison, while the
  underlying `packages.Load` may run in parallel across separate
  Workspace instances.
- **The package lock** in `pkg/lang/go/patch.go` serialises concurrent
  edits to the same package directory within a single process.

## Adding a new tool

The bar: an agent's grep-and-edit workflow that takes 5+ turns
collapses into one type-checked, build-gated call.

1. **Design the schema.** Define `Input` and `Output` structs in the
   appropriate package with `jsonschema:"..."` tags. The schema is the
   agent-facing API surface; make it self-documenting.
2. **Implement the action.** For Go refactors, put the action in
   `pkg/lang/go/refactor/<action>.go` and call `RegisterAction`. For
   other tools, the handler lives in the domain package itself.
3. **Wire the wrapper.** For Go refactors, add a thin
   `pkg/lang/go/<tool>.go` that converts `lang.*Input` to
   `refactor.Input` and dispatches via `runRefactorAction`. For other
   tools, declare `var X = tool.New[In, Out](...)` directly.
4. **Register.** Add the tool to the appropriate `tools.go` `Tools`
   slice.
5. **Test.** Cover the happy path, the rollback path, and at least one
   edge case the tool exists to prevent.
6. **Document.** Production-grade godoc on the public entry point; the
   description is the agent-facing usage hint.

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the full workflow.
