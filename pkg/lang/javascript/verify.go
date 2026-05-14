// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package javascript

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Verify is the lang.javascript.verify tool. It is intended to run
// quality gates on a JavaScript project and translate the output of
// the underlying tooling into the unified [lang.VerifyOutput] shape.
//
// The expected suite mapping for the eventual implementation:
//
//   - lint: ESLint via `eslint --format json` (its native machine-
//     readable format), with the project's existing eslint.config.js or
//     .eslintrc.* honoured. Severity tiers are mapped: ESLint "error"
//     -> Error, "warning" -> Fail when treated as failing, else Pass.
//   - test: detected from package.json scripts — jest, vitest, mocha,
//     or node --test. Each has a JSON reporter (`--reporters=json` for
//     jest, `--reporter=json` for vitest, mocha-json-stream for mocha)
//     that the runner will parse into failure lists.
//   - bench: project-specific — likely benchmark.js or vitest's bench
//     mode.
//   - fuzz: jsfuzz or fast-check property-based tests, depending on
//     project conventions.
//
// The symbol is currently a stub registered via tool.Stub. Because the
// future implementation will exec Node and npm/pnpm/yarn from PATH, it
// inherits Node's portability story: the correct Node version must be
// installed, node_modules must be installed (or pnpm with a frozen
// lockfile must be available), and projects using corepack pin specific
// package-manager versions that must match. Until this is built, invoke
// the tools directly via Bash.
var Verify = tool.Stub[lang.VerifyInput, lang.VerifyOutput](
	"lang.javascript.verify",
	"Run quality gates on a JavaScript project. Supports lint, test, bench, and fuzz suites with structured output.",
	tool.WithShortDescription("Run JavaScript lint/test/bench/fuzz suites with structured reports (stub)"),
)
