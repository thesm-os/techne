// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package mcp provides the Model Context Protocol presenter for techne.
//
// The presenter exposes every registered [tool.Tool] over JSON-RPC on
// stdio using the upstream model-context-protocol go-sdk. Tool input
// schemas (advertised via 'tools/list') and invocation results
// (returned from 'tools/call') are translated automatically by the
// tool's own RegisterMCP implementation — this package contributes only
// the server lifecycle: construction, transport binding, shutdown.
//
// The transport is currently fixed to stdio; an MCP client (typically a
// language-model agent harness) writes newline-delimited JSON-RPC
// requests to the server's stdin and reads responses from stdout. The
// server multiplexes calls onto the tool registry concurrently, so tool
// implementations are required to be safe for concurrent use.
package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"go.thesmos.sh/techne/internal/tool"
)

// Server presents the techne tool registry over the Model Context Protocol
// (MCP). It wraps the upstream go-sdk MCP server and remembers the slice
// of tools it was constructed with so the same instances can be inspected
// later — the SDK does not expose its registered tools after registration.
//
// A Server is a stateful object whose lifecycle is bound to a single
// transport: construct one with [NewServer], call [Server.Run] exactly
// once to attach it to a transport (currently stdio), and let the
// received context cancel to shut it down. Server is not safe to reuse
// across transports or to Run more than once.
//
// Concurrency: while Run is active, the MCP SDK serialises JSON-RPC
// requests on the transport and may invoke tool Execute methods
// concurrently from its internal worker pool. Tools are therefore
// responsible for their own thread-safety; Server itself does not
// synchronise tool calls.
type Server struct {
	inner *mcpsdk.Server
	tools []tool.Tool
}

// NewServer creates an MCP [Server] exposing the given tools. Each tool's
// RegisterMCP method (on the [tool.Tool] interface) is invoked, which is
// where the tool translates its InputSchema and OutputSchema into the
// wire format the SDK uses to advertise itself to clients during the
// 'tools/list' RPC.
//
// The returned Server is fully populated but inert until [Server.Run] is
// called — no transport is attached, no goroutines are started.
// NewServer never returns an error; misconfiguration of a tool surfaces
// later at invocation time as an MCP error reply.
//
// NewServer must be called from a single goroutine; the underlying SDK
// server is not safe to mutate concurrently while registrations are being
// added.
func NewServer(tools []tool.Tool) *Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "techne",
		Version: "0.1.0",
	}, nil)

	for _, t := range tools {
		t.RegisterMCP(s)
	}

	return &Server{inner: s, tools: tools}
}

// Run starts the MCP server bound to a stdio transport and blocks until
// ctx is cancelled or the peer closes its side of stdin. While Run is
// blocked, the server:
//
//   - reads newline-delimited JSON-RPC requests from os.Stdin,
//   - dispatches 'tools/list' to advertise the registered tool surface,
//   - dispatches 'tools/call' by decoding the request's 'arguments' as
//     raw JSON and forwarding it to the tool's Execute method,
//   - writes JSON-RPC responses to os.Stdout. Stderr is reserved for
//     diagnostics so it does not corrupt the transport.
//
// Error mapping: a non-nil error from Execute is returned to the MCP
// client as a JSON-RPC error response with the error string in the
// message field; structured tool output (the 'any' return) is marshalled
// as JSON and placed in the response result. Panics inside Execute are
// recovered by the SDK and surfaced as errors so a misbehaving tool
// cannot crash the server process.
//
// Run returns nil on a clean shutdown (ctx cancelled or stdin EOF) and
// an error if the transport itself fails (e.g. a write to stdout fails
// because the peer disconnected). It is safe to call Run only once per
// Server.
func (s *Server) Run(ctx context.Context) error {
	return s.inner.Run(ctx, &mcpsdk.StdioTransport{})
}
