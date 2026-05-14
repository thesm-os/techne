<!--
SPDX-License-Identifier: MIT
Copyright Thesmos B.V. 2026
-->

# Contributing to techne

Thanks for your interest in techne. This file describes the workflow,
quality bar, and conventions a contribution is expected to meet. If
anything is unclear, open an issue before writing code.

## Ground rules

- **Discuss first for non-trivial work.** A 200-line refactor or a new
  tool deserves a design issue before a PR; we'd rather pre-align on the
  approach than ask you to redo a sound implementation that doesn't fit
  the architecture.
- **One concern per PR.** Don't bundle unrelated changes. A "while I'm
  here, I also reformatted these files" diff is harder to review and
  harder to revert than two separate PRs.
- **No new tool without dogfooding.** New `lang.*` or `fs.*` tools must
  carry tests that exercise the agent-facing JSON schema and the
  build-gate / rollback path. See existing tests in `pkg/lang/go/` for
  the pattern.

## Development setup

```bash
git clone https://github.com/thesmos/techne
cd techne
make bootstrap   # installs gofumpt, gci, golangci-lint, govulncheck, ...
make install     # downloads + verifies module dependencies
make check       # full pre-merge gate (mod + lint + test + vuln + ...)
```

Go version: the toolchain version in [`go.mod`](go.mod) is authoritative.
Use it directly or via [`gvm`](https://github.com/moovweb/gvm) /
[`asdf`](https://asdf-vm.com/). CI installs from `go-version-file: go.mod`.

## Pre-merge gate

Every PR must pass `make check` locally before review. The umbrella gate
runs:

| Stage | Tooling |
|---|---|
| `mod verify` | `go mod tidy` produces no diff; `go.sum` matches |
| `lint` | `go vet`, `golangci-lint` (full suite), markdownlint, SPDX headers |
| `test` | `go test ./...` with race + coverage |
| `vuln` | `govulncheck ./...` |
| `coverage` | per-layer thresholds (see `.ergon/`) |

The CI workflow runs the same gate on every push and PR; if it's red on
CI but green locally, your local toolchain is stale — `make bootstrap`
refreshes it.

## Commit conventions

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <subject>

<body, wrapped at 72 cols>

<footer with Co-Authored-By, refs, etc.>
```

**Types:** `feat`, `fix`, `refactor`, `docs`, `test`, `perf`, `build`,
`ci`, `chore`. **Scope** is the package path or tool name (`lang.go.rename`,
`presenter/mcp`, `internal/tool`).

The subject line is imperative ("add", not "added") and under 70
characters. Use the body to explain *why*, not *what* — the diff already
shows what. Reference the failure mode that motivated the change when
relevant.

## Code style

- **Formatting:** `gofumpt` + `gci` (see `.golangci.yml` for grouping).
  `make fmt` applies both.
- **Linting:** the full suite in `.golangci.yml` is mandatory. Don't
  add `//nolint` comments; if a rule is genuinely wrong for the
  project, propose a change to `.golangci.yml` instead.
- **Naming:** standard Go (`MixedCaps`, no underscores, exported names
  start with the package's concern). The `revive` config catches the
  common violations.
- **Doc comments:** every exported symbol gets a godoc comment.
  Non-trivial types and functions get multi-paragraph treatment that
  covers rationale, lifecycle, failure modes, and concurrency where
  relevant. See `pkg/lang/go/refactor/transaction.go` for the bar.
- **Tests:** external test packages (`package foo_test`) for new code.
  Use `t.Context()`, not `context.Background()`. Use `t.Chdir()`
  instead of manual `os.Chdir` + cleanup.

## Adding a new tool

1. **Design first.** Open an issue with the proposed JSON schema (Input
   and Output Go structs with `jsonschema:"..."` tags) and a one-line
   description per field. The schema is the agent-facing API — it must
   be self-documenting.
2. **Implement.** Put the tool entry-point in `pkg/<domain>/<tool>.go`
   and the heavy lifting in a focused helper file or sub-package. For
   Go refactor tools, the action goes in `pkg/lang/go/refactor/` and
   the wrapper in `pkg/lang/go/`.
3. **Test.** Cover the happy path, the build-gate-rollback path, and at
   least one edge case (the equivalent of the gotchas your tool exists
   to avoid). Tests must hit the public agent-facing entry point, not
   the internal helper.
4. **Document.** The doc on the tool's `tool.New(...)` invocation is the
   agent-facing description. Explain what the tool does, when an agent
   should reach for it, what makes it preferable to generic
   Read/Edit/Grep, and what its atomicity guarantees are. Multi-paragraph.
5. **Wire it in.** Add the var to `pkg/<domain>/tools.go`.

The bar for a "good tool" is: an agent's grep-and-edit workflow that
would take 5+ turns collapses into one type-checked, build-gated call
with structured output.

## Reporting issues

- **Bug:** use the *Bug report* issue template. Include a minimal
  reproducer, the techne version (`techne --version`), and the
  expected vs. actual behaviour.
- **Feature request / new tool:** use the *Feature request* template
  and describe the agent workflow you're trying to make atomic.
- **Security:** see [SECURITY.md](SECURITY.md). Do **not** open a
  public issue for security problems.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
By participating, you agree to abide by its terms.

## License

By contributing, you agree that your contributions will be licensed
under the [MIT License](LICENSE).
