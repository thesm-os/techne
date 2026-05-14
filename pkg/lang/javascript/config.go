// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package javascript provides stub JavaScript/TypeScript-shape tools — the
// concrete implementations live in pkg/lang/typescript.
package javascript

// Config is a placeholder for javascript-tool configuration.
//
// It is intentionally empty today: the lang.javascript tools are stubs
// that share an eventual TypeScript-based implementation (see the
// package doc comment), so most knobs — Node binary, package manager,
// ESLint config path, tsconfig.json path — will live on the
// typescript.Config side and be consumed by both languages.
//
// The zero value is, and will remain, a valid configuration; reserving
// the type now keeps the tool factory signature stable so adding fields
// later is non-breaking.
type Config struct{}
