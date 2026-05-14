// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package rust provides Rust-language tools (verify via cargo, explore, deps).
package rust

// Config holds optional configuration for the Rust language tools.
//
// For now the only knob is CargoPath; future fields are expected to cover
// toolchain overrides (rustfmt, clippy), target triples, and feature-flag
// selection for lang.rust.verify. The zero value of Config is a valid,
// fully-defaulting configuration — the tools fall back to PATH lookup
// for any binary they need to invoke.
//
// Config is consumed by the tool factory wired up in the package's init
// function; callers that do not want to override defaults can omit the
// config block entirely.
type Config struct {
	// CargoPath is the absolute path (or PATH-resolvable name) of the cargo
	// binary that the future lang.rust.verify implementation will exec to run
	// `cargo check`, `cargo test`, `cargo clippy`, etc.
	//
	// Defaults to the literal string "cargo", causing the OS to perform a
	// standard PATH search. Set this explicitly when the host has multiple
	// toolchains installed (e.g. a rustup-managed cargo alongside a distro
	// package) and the verify suite must target a specific one, or when
	// running in a sandbox where PATH is unavailable.
	CargoPath string `json:"cargo_path,omitempty" jsonschema:"Path to cargo binary. Default: cargo"`
}
