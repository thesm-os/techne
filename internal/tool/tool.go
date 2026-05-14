// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package tool defines the core Tool interface used throughout techne.
// Every domain tool implements this interface, and every presenter (MCP, TUI, CLI)
// depends on it.
package tool

import (
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool is the core interface every techne tool implements. It is the
// abstraction every presenter (MCP server, CLI cobra commands, TUI panes)
// consumes: a tool exposes its name, description, JSON Schema for input
// and output, and a single Execute entry point that takes a raw JSON
// payload and returns a typed Go value plus an error.
//
// The canonical way to produce an implementation is [New], which derives
// InputSchema and OutputSchema from struct tags via
// github.com/google/jsonschema-go using the In and Out type parameters.
// Because the schema is generated from the same Go types the handler
// consumes, the agent-facing descriptor cannot drift from the runtime
// contract: adding a field to the input struct automatically surfaces
// it in the MCP tool descriptor, the CLI flag set, and the TUI form.
// Use [Stub] for not-yet-implemented tools that should still publish a
// schema and appear in tool listings.
//
// Name must be a stable dotted identifier (for example, 'fs.read' or
// 'lang.go.rename'). It is the routing key every presenter uses: the
// CLI converts dots into subcommand boundaries, the MCP server uses it
// as the wire-level tool ID, and the registry filters tools by prefix.
// Description is the one-paragraph summary shown to discovering agents
// and should describe both what the tool does and when to prefer it
// over generic alternatives.
//
// Execute is invoked with a context.Context (carrying cancellation,
// deadlines, and any trace correlation set by the presenter) and the
// raw JSON bytes received from the wire. Implementations produced by
// [New] coerce common stringified-primitive mismatches via
// coerceJSONToSchema, unmarshal into the typed Input, run the handler,
// and return either the typed Out value or an error wrapped with the
// tool name for upstream attribution. The returned 'any' is the typed
// Out value, not a generic map — presenters can rely on the declared
// output type when marshalling responses.
//
// RegisterMCP attaches the tool to an MCP server using its typed
// handler so the SDK can route inbound CallTool requests directly into
// the generic handler without going through the json.RawMessage path.
// It is called once per tool at server startup; calling it twice on
// the same server is undefined.
//
// Tools are stateless and concurrency-safe: a single Tool instance is
// shared across concurrent presenters and concurrent invocations.
// Handlers passed to [New] must therefore be reentrant and must not
// retain references to the input bytes beyond Execute's return.
type Tool interface {
	Name() string
	Description() string
	InputSchema() *jsonschema.Schema
	OutputSchema() *jsonschema.Schema
	Execute(ctx context.Context, rawInput json.RawMessage) (any, error)
	RegisterMCP(server *mcp.Server)
}
