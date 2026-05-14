// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.thesmos.sh/techne/internal/tool"
)

type TestInput struct {
	Name string `json:"name"          jsonschema:"The name"`
	Age  int    `json:"age,omitempty" jsonschema:"The age"`
}

type TestOutput struct {
	Greeting string `json:"greeting" jsonschema:"The greeting"`
}

func makeHandler() tool.Handler[TestInput, TestOutput] {
	return func(_ context.Context, input TestInput) (TestOutput, error) {
		return TestOutput{Greeting: "Hello, " + input.Name}, nil
	}
}

func TestNew_NameAndDescription(t *testing.T) {
	tl := tool.New[TestInput, TestOutput]("test.greet", "Greets a person", makeHandler())
	if tl.Name() != "test.greet" {
		t.Errorf("Name(): got %q, want %q", tl.Name(), "test.greet")
	}
	if tl.Description() != "Greets a person" {
		t.Errorf("Description(): got %q, want %q", tl.Description(), "Greets a person")
	}
}

func TestNew_InputSchemaHasExpectedProperties(t *testing.T) {
	tl := tool.New[TestInput, TestOutput]("test.greet", "Greets a person", makeHandler())
	schema := tl.InputSchema()
	if schema == nil {
		t.Fatal("InputSchema() returned nil")
	}
	if _, ok := schema.Properties["name"]; !ok {
		t.Error("InputSchema() missing property 'name'")
	}
	if _, ok := schema.Properties["age"]; !ok {
		t.Error("InputSchema() missing property 'age'")
	}
}

func TestNew_OutputSchemaHasExpectedProperties(t *testing.T) {
	tl := tool.New[TestInput, TestOutput]("test.greet", "Greets a person", makeHandler())
	schema := tl.OutputSchema()
	if schema == nil {
		t.Fatal("OutputSchema() returned nil")
	}
	if _, ok := schema.Properties["greeting"]; !ok {
		t.Error("OutputSchema() missing property 'greeting'")
	}
}

func TestNew_Execute_ValidInput(t *testing.T) {
	tl := tool.New[TestInput, TestOutput]("test.greet", "Greets a person", makeHandler())
	raw, _ := json.Marshal(TestInput{Name: "Alice", Age: 30})
	result, err := tl.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	out, ok := result.(TestOutput)
	if !ok {
		t.Fatalf("Execute() result type: got %T, want TestOutput", result)
	}
	if out.Greeting != "Hello, Alice" {
		t.Errorf("Execute() greeting: got %q, want %q", out.Greeting, "Hello, Alice")
	}
}

func TestNew_Execute_InvalidJSON(t *testing.T) {
	tl := tool.New[TestInput, TestOutput]("test.greet", "Greets a person", makeHandler())
	_, err := tl.Execute(context.Background(), json.RawMessage(`{invalid json}`))
	if err == nil {
		t.Fatal("Execute() expected error for invalid JSON, got nil")
	}
}

func TestStub_Execute_ReturnsNotImplemented(t *testing.T) {
	tl := tool.Stub[TestInput, TestOutput]("test.stub", "A stub tool")
	_, err := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Stub Execute() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("Stub Execute() error: got %q, want to contain 'not implemented'", err.Error())
	}
}

func TestEnum_AddsEnumConstraint(t *testing.T) {
	tl := tool.New[TestInput, TestOutput](
		"test.greet", "Greets a person", makeHandler(),
		tool.Enum("name", "Alice", "Bob", "Charlie"),
	)
	schema := tl.InputSchema()
	prop, ok := schema.Properties["name"]
	if !ok {
		t.Fatal("InputSchema() missing property 'name'")
	}
	if len(prop.Enum) != 3 {
		t.Errorf("Enum count: got %d, want 3", len(prop.Enum))
	}
}

func TestRegisterMCP_DoesNotPanic(t *testing.T) {
	tl := tool.New[TestInput, TestOutput]("test.greet", "Greets a person", makeHandler())
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	tl.RegisterMCP(server)
}

func TestStub_RegisterMCP_DoesNotPanic(t *testing.T) {
	tl := tool.Stub[TestInput, TestOutput]("test.stub", "A stub tool")
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	tl.RegisterMCP(server)
}
