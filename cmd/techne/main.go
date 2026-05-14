// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Command techne is the thin main entry point for the techne binary.
//
// All wiring — configuration, tool registration, and presenter selection
// (CLI, MCP, TUI) — lives in the [go.thesmos.sh/techne/internal/app]
// package; this file exists only so that the build target is a `main`
// package and so that errors surfaced from app.Run translate cleanly to
// a non-zero exit code with the error message on stderr.
//
// Keeping main minimal is deliberate: it makes the binary trivially
// embeddable (callers can vendor app.Run from a test or alternative
// host) and keeps the operational surface — flags, subcommands, signal
// handling — under unit-testable code rather than func main.
package main

import (
	"fmt"
	"os"

	"go.thesmos.sh/techne/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
