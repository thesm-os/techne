// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package mcp — registration is handled via each tool's own
// RegisterMCP(server) hook on the [tool.Tool] interface. This file is
// intentionally empty; the registration walk lives in server.go inside
// [NewServer], and the per-tool MCP wire-up lives next to each tool's
// implementation. The file exists only to anchor a future home for
// registration helpers (e.g. metric wrappers, panic guards) that would
// otherwise crowd server.go.
package mcp
