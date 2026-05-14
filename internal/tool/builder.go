// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handler is the typed handler signature every tool built via [New]
// supplies. The In and Out type parameters are the same ones [New]
// uses to derive InputSchema and OutputSchema, so the JSON the
// presenter receives, the value passed to the handler, and the value
// the handler returns are guaranteed to share a single source of
// truth.
//
// Handlers must be reentrant: a single Tool value is shared across
// all concurrent presenters and all concurrent invocations on each
// presenter. Treat the input as immutable for the duration of the
// call, and do not retain references to it after returning.
//
// The ctx propagates the presenter's cancellation, deadline, and
// any trace correlation. Long-running handlers must respect it and
// return promptly when it is cancelled.
type Handler[In, Out any] func(ctx context.Context, input In) (Out, error)

// Option modifies a toolData after its schemas have been inferred
// but before it is returned from [New] or [Stub]. Options are
// applied in the order supplied, so later options observe (and may
// overwrite) earlier modifications.
//
// Options operate on the inferred input schema in place — they are
// the escape hatch for constraints that struct tags cannot express
// cleanly, such as enum membership ([Enum]) or default values
// ([Default]). Options that target a missing property silently no-op
// so that adding an Option to a tool whose input struct has changed
// does not break compilation.
type Option func(t *toolData)

// toolData is the internal representation of a built tool.
type toolData struct {
	name         string
	description  string
	inputSchema  *jsonschema.Schema
	outputSchema *jsonschema.Schema
	execute      func(ctx context.Context, rawInput json.RawMessage) (any, error)
	registerMCP  func(server *mcp.Server)
}

// Name returns the registered tool identifier, the dotted routing
// key used by every presenter (for example, 'fs.read' or
// 'lang.go.rename'). It is set at construction time by [New] or
// [Stub] and never changes for the lifetime of the value.
func (t *toolData) Name() string { return t.name }

// Description returns the human-readable summary shown to agents
// discovering this tool through the MCP descriptor, the CLI --help
// output, or the TUI catalogue. It is set at construction time and
// is safe to call concurrently.
func (t *toolData) Description() string { return t.description }

// InputSchema returns the JSON Schema inferred from the In type
// parameter passed to [New] or [Stub]. The returned pointer is the
// live schema — callers must not mutate it; use [Enum] or [Default]
// at construction time instead. Safe for concurrent reads.
func (t *toolData) InputSchema() *jsonschema.Schema { return t.inputSchema }

// OutputSchema returns the JSON Schema inferred from the Out type
// parameter passed to [New] or [Stub]. Presenters use it to
// advertise the shape of successful responses; the runtime value
// returned from Execute is always a typed Out, never a generic map.
// The returned pointer is shared — do not mutate.
func (t *toolData) OutputSchema() *jsonschema.Schema { return t.outputSchema }

// Execute is the JSON-on-the-wire entry point. It coerces common
// stringified primitives in rawInput against the input schema (see
// coerceJSONToSchema), unmarshals into the typed Input, invokes
// the user-supplied [Handler], and returns the typed Out value
// plus any handler error.
//
// Unmarshalling errors are wrapped with the tool name so upstream
// logs and presenter responses can attribute failures without
// extra plumbing. The handler error is returned unwrapped so that
// domain code retains control over error semantics (sentinel
// values, errors.Is / errors.As chains, etc.).
//
// Execute is safe for concurrent calls: the toolData fields it
// reads are immutable after construction, and the handler
// contract requires reentrancy.
func (t *toolData) Execute(ctx context.Context, rawInput json.RawMessage) (any, error) {
	return t.execute(ctx, rawInput)
}

// RegisterMCP attaches this tool to the supplied MCP server using
// the typed handler captured at construction time. The MCP SDK
// then routes inbound CallTool requests directly to the typed
// handler, bypassing the json.RawMessage path used by Execute.
//
// Called once per tool at server startup. Registering the same
// tool twice on a single server is undefined; the MCP SDK does
// not deduplicate.
func (t *toolData) RegisterMCP(server *mcp.Server) {
	t.registerMCP(server)
}

// New constructs a [Tool] backed by the supplied typed handler.
// It is the canonical entry point used by every domain package
// (fs, lang.go, lang.rust, and so on) to declare a tool.
//
// The two type parameters drive schema inference: at construction
// time, jsonschema.For of the input type and of the output type
// produces the JSON Schemas exposed via InputSchema and
// OutputSchema. The struct tags on the input and output types are
// therefore the single source of truth for the agent-facing
// contract — there is no separate schema file to keep in sync.
// Fields tagged with json:",omitempty" are optional; everything
// else is required. Use jsonschema:"..." tags to attach per-field
// documentation that surfaces in the MCP descriptor.
//
// Invariants and edge cases:
//
//   - name should be a stable dotted identifier matching the tool's
//     registered group prefix (verified at test time by
//     [AssertTools]).
//   - description must be non-empty; [AssertTools] flags empty ones.
//   - Schema inference is performed eagerly. Any failure (for
//     example, an unsupported type or a malformed jsonschema tag)
//     panics with the tool name embedded — the design assumption is
//     that schema errors are programmer errors caught at init time,
//     not runtime conditions to recover from.
//   - The returned Tool is stateless and safe for concurrent use.
//     Options are applied in order; later options can overwrite
//     earlier ones.
//   - The handler must be non-nil. New does not check; calling
//     Execute on a Tool built with a nil handler will panic when
//     the handler dispatch runs.
func New[In, Out any](name, description string, handler Handler[In, Out], opts ...Option) Tool {
	inputSchema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool.New: failed to infer input schema for tool %q: %v", name, err))
	}
	outputSchema, err := jsonschema.For[Out](nil)
	if err != nil {
		panic(fmt.Sprintf("tool.New: failed to infer output schema for tool %q: %v", name, err))
	}

	td := &toolData{
		name:         name,
		description:  description,
		inputSchema:  inputSchema,
		outputSchema: outputSchema,
		execute: func(ctx context.Context, rawInput json.RawMessage) (any, error) {
			coerced := coerceJSONToSchema(rawInput, inputSchema)
			var in In
			if err := json.Unmarshal(coerced, &in); err != nil {
				return nil, fmt.Errorf("tool %q: failed to unmarshal input: %w", name, err)
			}
			return handler(ctx, in)
		},
		registerMCP: func(server *mcp.Server) {
			mcpHandler := func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
				out, err := handler(ctx, input)
				return nil, out, err
			}
			mcp.AddTool[In, Out](server, &mcp.Tool{
				Name:        name,
				Description: description,
			}, mcpHandler)
		},
	}

	for _, opt := range opts {
		opt(td)
	}
	return td
}

// Stub constructs a [Tool] that has fully-inferred input and output
// schemas but whose Execute always returns 'not implemented'. It is
// the placeholder used by language packages (lang.javascript,
// lang.python, lang.rust, lang.typescript) for tools that should
// appear in the registry, advertise their contract over MCP, and
// list in --help output even before the per-language implementation
// exists.
//
// The stub also registers with the MCP server so that an agent
// asking for the tool descriptor sees a fully-typed signature; the
// call simply errors at invocation time. This lets the MCP
// presenter list a uniform tool set across languages without
// conditional registration logic at startup.
//
// Schema inference is performed eagerly with the same panic-on-
// failure contract as [New]. The handler is implicitly the
// 'not implemented' sentinel; supplying Options ([Enum], [Default])
// still applies to the published schema. Returned tool is stateless
// and safe for concurrent use.
func Stub[In, Out any](name, description string, opts ...Option) Tool {
	inputSchema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool.Stub: failed to infer input schema for tool %q: %v", name, err))
	}
	outputSchema, err := jsonschema.For[Out](nil)
	if err != nil {
		panic(fmt.Sprintf("tool.Stub: failed to infer output schema for tool %q: %v", name, err))
	}

	td := &toolData{
		name:         name,
		description:  description,
		inputSchema:  inputSchema,
		outputSchema: outputSchema,
		execute: func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, errors.New("not implemented")
		},
		registerMCP: func(server *mcp.Server) {
			stubHandler := func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
				var zero Out
				return nil, zero, errors.New("not implemented")
			}
			mcp.AddTool[In, Out](server, &mcp.Tool{
				Name:        name,
				Description: description,
			}, stubHandler)
		},
	}

	for _, opt := range opts {
		opt(td)
	}
	return td
}

// Enum constrains a string property in the input schema to a fixed
// set of values, surfacing as an enum array in the generated JSON
// Schema. Use it for properties that struct tags cannot express
// cleanly, such as a 'mode' field that accepts only a handful of
// literals.
//
// Applied as an [Option] at construction time. If the named
// property is missing from the input schema (for example, because
// the input struct field was renamed but the Option was not
// updated), Enum silently no-ops rather than panicking; this keeps
// refactors from cascading into init-time crashes. Pre-existing
// enum constraints on the property are replaced, not merged.
//
// Values are stored as []any so they appear as plain strings in the
// emitted schema. Non-string property types are accepted by the
// schema but will fail unmarshalling at request time.
func Enum(property string, values ...string) Option {
	return func(t *toolData) {
		if t.inputSchema.Properties == nil {
			return
		}
		prop, ok := t.inputSchema.Properties[property]
		if !ok {
			return
		}
		enum := make([]any, len(values))
		for i, v := range values {
			enum[i] = v
		}
		prop.Enum = enum
	}
}

// Default sets the default value on a named property in the input
// schema, making the field optional from the wire perspective and
// giving discovering agents a sensible starting value.
//
// Applied as an [Option] at construction time. The value is
// marshalled to JSON via encoding/json and stored as
// json.RawMessage; any marshalling failure or unknown property name
// results in a silent no-op so that schema authoring mistakes do
// not abort startup. Pre-existing defaults are replaced.
//
// Note that Default only changes the published schema. The Go-side
// zero value of the input struct field still applies when the
// request omits the property, so the value supplied here should
// match the struct's zero value or the handler should treat the
// schema default as advisory.
func Default(property string, value any) Option {
	return func(t *toolData) {
		if t.inputSchema.Properties == nil {
			return
		}
		prop, ok := t.inputSchema.Properties[property]
		if !ok {
			return
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return
		}
		prop.Default = json.RawMessage(raw)
	}
}
