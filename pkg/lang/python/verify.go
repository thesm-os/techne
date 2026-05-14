// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package python

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Verify is the lang.python.verify tool. It is intended to run quality
// gates on a Python project and translate the output of the underlying
// linters / test runners into the unified [lang.VerifyOutput] shape.
//
// The expected suite mapping for the eventual implementation:
//
//   - lint: ruff (combined linter+formatter) and mypy --strict for type
//     checking. Both emit JSON via `--output-format=json`, which the
//     runner will parse into per-issue diagnostics with severity
//     mapped onto the framework's Error / Fail / Pass tiers.
//   - test: pytest with `--json-report` (via the pytest-json-report
//     plugin) so the failure list, durations, and stdout/stderr are
//     structured rather than scraped.
//   - bench: pytest-benchmark, also exporting JSON.
//   - fuzz: atheris or hypothesis, depending on the project's existing
//     conventions — detected from pyproject.toml.
//
// The symbol is currently a stub registered via tool.Stub; invoking it
// returns the schema and a "not implemented" status. Because the future
// implementation will exec external binaries from PATH, it will inherit
// Python's portability story: missing interpreter, wrong virtualenv,
// missing optional deps (pytest-benchmark, atheris) will all surface as
// run-time errors that the runner must report cleanly instead of
// crashing. Until this is built, invoke pytest / mypy / ruff directly
// via Bash.
var Verify = tool.Stub[lang.VerifyInput, lang.VerifyOutput](
	"lang.python.verify",
	"Run quality gates on a Python project. Supports lint, test, bench, and fuzz suites with structured output.",
	tool.WithShortDescription("Run Python lint/test/bench/fuzz suites with structured reports (stub)"),
)
