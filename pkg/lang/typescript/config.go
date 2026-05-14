// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package typescript provides stub TypeScript-shape tools (verify, explore, deps).
package typescript

// Config is a placeholder for typescript-tool configuration.
//
// Future fields are expected to cover the Node binary used to host the
// typescript compiler API (CompilerPath), the tsconfig.json that scopes
// the project (TSConfigPath), the package manager used to install
// typescript and its plugins (PackageManager: npm/pnpm/yarn/bun), and
// strictness toggles for the eventual lang.typescript.verify suite.
//
// The zero value is, and will remain, a valid configuration: every
// field should default to PATH lookup of the canonical tool name plus
// automatic discovery of the nearest tsconfig.json, so a properly
// provisioned project needs no configuration at all. Reserving the
// type now keeps the tool factory signature stable so adding fields
// later is non-breaking.
type Config struct{}
