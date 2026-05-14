// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package typescript

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Verify is the lang.typescript.verify tool. It is intended to run
// quality gates on a TypeScript project and translate the output of
// the underlying tooling into the unified [lang.VerifyOutput] shape.
//
// The expected suite mapping for the eventual implementation:
//
//   - lint: `tsc --noEmit` for type errors (mandatory) plus ESLint with
//     @typescript-eslint rules. tsc emits its diagnostics in a pretty
//     format by default; the runner will use `--pretty false` and parse
//     the canonical `file(line,col): error TSxxxx: message` form into
//     structured issues. ESLint JSON parsing matches the lang.javascript
//     runner.
//   - test: vitest or jest with their JSON reporters — detected from
//     package.json scripts and present devDeps.
//   - bench: vitest bench or tinybench.
//   - fuzz: fast-check property-based tests, which integrate into the
//     test runners above.
//
// The symbol is currently a stub registered via tool.Stub. Because the
// future implementation will exec Node, tsc, and a package manager from
// PATH, it inherits Node's portability story (correct Node version,
// node_modules installed, corepack-pinned package manager available)
// and tsc's project-discovery story (tsconfig.json must be reachable
// from the working directory). When tsc's project graph is large the
// run can take tens of seconds — the eventual implementation should
// use `--incremental` and a build cache. Until this is built, invoke
// tsc and ESLint directly via Bash.
var Verify = tool.Stub[lang.VerifyInput, lang.VerifyOutput](
	"lang.typescript.verify",
	"Run quality gates on a TypeScript project. Supports lint, test, bench, and fuzz suites with structured output.",
	tool.WithShortDescription("Run TypeScript lint/test/bench/fuzz suites with structured reports (stub)"),
)
