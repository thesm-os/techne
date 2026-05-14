<!--
SPDX-License-Identifier: MIT
Copyright Thesmos B.V. 2026
-->

# techne

> *τέχνη — the skill, craft, and know-how of working a material.*

**Techne is a collection of atomic, type-checked developer tools designed for
LLM agents.** It exposes ~25 operations — search, navigate, refactor, verify,
patch — over MCP (Model Context Protocol) so an agent can edit code with the
same fidelity a human developer gets from an IDE, but at language-model
latency and cost.

The same operations are also available over a CLI and a TUI, so humans get
the same guarantees: every edit is type-checked, every commit is build-gated,
and every failure rolls back atomically.

[![CI](https://github.com/thesm-os/techne/actions/workflows/ci.yml/badge.svg)](https://github.com/thesm-os/eidos/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/go.thesmos.sh/techne.svg)](https://pkg.go.dev/go.thesmos.sh/techne)
[![Go Report Card](https://goreportcard.com/badge/go.thesmos.sh/techne)](https://goreportcard.com/report/go.thesmos.sh/techne)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

---

## Why techne?

Modern coding agents reach for `grep`, `read`, `edit`, and `bash` because
those are the tools they were trained on. The result is fragile:

- A project-wide rename misses method dispatches through interfaces.
- A signature change leaves callers broken until the next compile turn.
- A move silently produces unreachable code paths.
- A failed edit leaves the workspace half-broken with no rollback.
- Every operation costs 3–8 conversation turns instead of 1.

Techne replaces those generic primitives with **focused, type-aware,
build-gated operations**. The agent makes one call; the tool performs the
work atomically, runs `go build`, and either commits cleanly or rolls every
file back to its original content. Each operation that would take 5+ turns
of grep/edit/build collapses into a single round-trip.

The same guarantees apply when humans drive the tools through the CLI or
TUI — no agent required.

## Quick start

### Install

```bash
go install go.thesmos.sh/techne/cmd/techne@latest
```

Or from source:

```bash
git clone https://github.com/thesmos/techne
cd techne
make build           # binary at ./bin/techne
go install ./cmd/techne
```

### Wire into your agent

#### Claude Code / Claude Desktop

```json
{
  "mcpServers": {
    "techne": {
      "command": "techne",
      "args": ["serve"]
    }
  }
}
```

#### Gemini CLI (`.gemini/settings.json`)

```json
{
  "mcpServers": {
    "techne-go": {
      "command": "techne",
      "args": ["serve", "--tools=lang.go"],
      "trust": true
    }
  }
}
```

You can scope which tools the server exposes:

```bash
techne serve --tools=fs,lang.go        # filesystem + Go
techne serve --tools=lang.rust         # only Rust
techne serve                           # everything
```

### Use from the CLI

Every tool is also a CLI subcommand:

```bash
techne lang go rename --symbol=NewUser --new_name=CreateUser
techne lang go verify --suites=lint,test
techne fs grep --path=. --pattern='TODO\(.*\)'
techne lang go search --query='how is backpressure handled'
```

`techne --help` lists every command; each subcommand has its own `--help`.

## What's inside

### Tool catalogue (~25 operations)

#### `lang.go.*` — Go-aware operations

| Tool | Purpose |
|---|---|
| `rename` | Project-wide symbol rename (type-checked, handles method dispatch) |
| `change_signature` | Add/remove parameters and returns; rewrites every call site |
| `change_type` | Replace a type definition; updates every usage |
| `move_symbol` | Move a symbol between files in the same package |
| `move_symbols` | Atomic batch of moves (single build gate) |
| `move_file` | Relocate a `.go` file to a different package |
| `move_package` | Move a whole package; rewrites every importer |
| `extract_function` | Extract a code range into a new function or method |
| `extract_interface` | Generate an interface from a struct's method set |
| `extract_variable` | Lift an expression into a named local |
| `inline_constant` | Inline a constant at every use site |
| `implement_interface` | Generate method stubs to satisfy an interface |
| `document` | Bulk doc-comment rewrites with godoc validation |
| `add_tests` | Scaffold test functions (top-level + subtests with `t.Parallel()`) |
| `delete_file` | Atomic file deletion with build-gate rollback |
| `patch` | Build-verified text edits with atomic batch support |
| `fix` | Lint → apply suggested patches → re-verify in one turn |
| `verify` | Run lint, test, bench, and fuzz suites with structured output |
| `search` | Fuzzy + intent-based symbol search (gopls + in-process scorer) |
| `search_explore` | Search + show source bodies in one round-trip |
| `explore` | Map a package's API or read specific functions' source |
| `callers` / `references` / `implementations` / `invocations` | Type-checked relationship queries |

#### `fs.*` — Filesystem operations

| Tool | Purpose |
|---|---|
| `patch` | Atomic multi-file find-and-replace with optional verify command |
| `grep` / `find` / `list` / `stat` | Read-side primitives with structured output |
| `read` / `write` / `copy` / `move` / `delete` / `replace` | Write-side primitives with safety checks |

Stub toolsets for **Rust** (tree-sitter–powered), **Python**, **JavaScript**,
and **TypeScript** are present and follow the same shape; expand as needed.

### Invariants every Go tool enforces

| Invariant | What it means |
|---|---|
| **Type-checked** | No regex-driven name matching — operations use `go/types` |
| **Workspace-aware** | `go.work` setups handled transparently |
| **Atomic** | All-or-nothing; rollback on any post-edit failure |
| **Build-gated** | `go vet` + `go build` run before any commit |
| **Structured I/O** | Inputs and outputs are JSON-Schema validated |
| **Detail-controllable** | `summary` / `standard` / `full` verbosity per call |

## Architecture

```
┌─────────────────────┐       ┌──────────────────┐
│  Agent (Claude /    │       │  Human (CLI/TUI) │
│  Gemini / Cursor /  │       │                  │
│  Copilot)           │       │                  │
└──────────┬──────────┘       └────────┬─────────┘
           │ MCP (stdio JSON-RPC)      │ cobra subcommands
           ▼                           ▼
        ┌────────────────────────────────────┐
        │   presenter/{mcp,cli,tui}          │
        │   Schema → command tree, decode    │
        └─────────────────┬──────────────────┘
                          │ tool.Tool interface
                          ▼
        ┌────────────────────────────────────┐
        │   internal/tool registry           │
        │   N tools across M domains         │
        └─────────────────┬──────────────────┘
                          │
        ┌─────────────────┴───────────────────┐
        ▼                 ▼                   ▼
┌───────────────┐ ┌──────────────┐ ┌───────────────────┐
│  pkg/fs/*     │ │ pkg/lang/go/ │ │ pkg/lang/{rust,   │
│  Atomic FS    │ │ refactor +   │ │ python,js,ts}/    │
│  operations   │ │ analysis     │ │ Stub adapters     │
└───────────────┘ └──────┬───────┘ └───────────────────┘
                         │
                         ▼
                ┌──────────────────────┐
                │ Transaction / Build  │
                │ Gate / Rollback      │
                └──────────────────────┘
```

Detailed architecture notes: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

The agent-facing routing rules (when to reach for a `lang.go.*` tool versus
generic Read/Edit/Grep) are documented in [docs/CLAUDE.md](docs/CLAUDE.md) —
copy that file into your downstream project's `CLAUDE.md` to teach the agent
to use techne.

## Development

The full lifecycle is driven by [`ergon`](https://go.thesmos.sh/ergon)
through the `Makefile`:

```bash
make bootstrap     # install dev tools (gofumpt, gci, golangci-lint, ...)
make install       # go mod download
make build         # build every module
make test          # go test with coverage
make lint          # vet + golangci-lint + markdown + license headers
make check         # umbrella pre-merge gate (mod + lint + test + vuln + ...)
make help          # list every target with annotations
```

For Go work *inside* this repo, the routing rules in
[`CLAUDE.md`](CLAUDE.md) describe which `lang.go.*` tool to reach for; the
project dogfoods its own tools.

### Project layout

```
cmd/techne/                          Main binary (thin entrypoint)
internal/app/                        Cobra command tree
internal/config/                     Viper-backed config loading
internal/tool/                       Tool interface + generic builder + registry
internal/version/                    ldflags-stamped build metadata
pkg/fs/                              Filesystem tools
pkg/lang/                            Cross-language types + engine
pkg/lang/go/                         Go-specific tools
pkg/lang/go/refactor/                Strategy registry (rename, move, etc.)
pkg/lang/go/internal/workspace/      go.work / go.mod abstraction
pkg/lang/{rust,python,js,ts}/        Stub language toolsets
presenter/cli/                       Cobra subcommand registration
presenter/mcp/                       MCP stdio server
presenter/tui/                       Bubbletea TUI (stub)
docs/                                Architecture notes, consumer template
```

### Stability

Pre-1.0. The public Go API and the JSON-Schema inputs/outputs of every tool
are still evolving; expect breaking changes between minor versions until
v1.0.0 is tagged.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow, commit
conventions, and the quality bar new tools are expected to meet.

Bug reports and security issues: see [SECURITY.md](SECURITY.md) for the
disclosure policy. Use the GitHub issue templates for non-sensitive bug
reports and feature requests.

## License

[MIT](LICENSE). Copyright (c) 2026 Thesmos B.V.
