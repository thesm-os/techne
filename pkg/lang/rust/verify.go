// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package rust

import (
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Verify is the lang.rust.verify tool. It is intended to run quality
// gates on a Rust crate — typically `cargo check`, `cargo clippy --
// -D warnings`, `cargo test`, `cargo bench`, and `cargo +nightly fuzz`
// — and to translate their output into the unified [lang.VerifyOutput]
// shape (per-issue file:line, severity, suggested patches, and summary
// counts).
//
// The symbol is currently a stub registered via tool.Stub: invoking it
// over MCP / CLI surfaces the declared schemas and a "not implemented"
// status, which is enough for agents to plan but not to actually verify
// a crate. A full implementation will need to (a) shell out to the
// cargo binary configured by [Config]'s CargoPath, (b) parse the JSON
// diagnostics produced by `cargo --message-format=json`, and (c) map
// clippy's lint codes to the framework's Error / Fail / Pass severity
// tiers.
//
// Because the future implementation will exec an external binary, it
// will inherit cargo's failure modes: missing toolchain, locked
// Cargo.toml, network access required to fetch dependencies, and so on.
// Until this is built, callers should invoke cargo directly via Bash.
var Verify = tool.Stub[lang.VerifyInput, lang.VerifyOutput](
	"lang.rust.verify",
	"Run quality gates on a Rust project. Supports lint, test, bench, and fuzz suites with structured output.",
	tool.WithShortDescription("Run Rust cargo check/clippy/test/bench/fuzz suites with structured reports (stub)"),
)
