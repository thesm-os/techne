// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package python provides stub Python-shape tools (verify, explore, deps).
package python

// Config is a placeholder for python-tool configuration.
//
// It exists today as an empty struct so that the wiring layer can pass
// a typed config block to the lang.python tool factory; future fields
// are expected to cover interpreter selection (CPython, PyPy, a uv- or
// rye-managed virtualenv), the path to the mypy / ruff / pytest
// binaries, and per-suite strictness profiles for the eventual
// lang.python.verify implementation.
//
// The zero value is, and will remain, a valid configuration: every
// field should default to PATH lookup of the canonical tool name so
// that the common case (a properly provisioned host) needs no
// configuration at all.
type Config struct{}
