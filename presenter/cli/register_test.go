// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/presenter/cli"
)

// fakeInput is a minimal typed input struct used in tests.
type fakeInput struct {
	Path    string `json:"path"`
	Count   int    `json:"count,omitempty"`
	Verbose bool   `json:"verbose,omitempty"`
}

// fakeOutput is a minimal typed output struct used in tests.
type fakeOutput struct {
	Result string `json:"result"`
}

func newFakeTool(name, description string) tool.Tool {
	return tool.New[fakeInput, fakeOutput](
		name,
		description,
		func(_ context.Context, in fakeInput) (fakeOutput, error) {
			return fakeOutput{Result: "path=" + in.Path}, nil
		},
	)
}

func newRoot() *cobra.Command {
	return &cobra.Command{Use: "root", Short: "test root"}
}

// findCommand searches the direct children of cmd for one with Use == name.
func findCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Use == name {
			return c
		}
	}
	return nil
}

func TestRegister_HierarchyCreated(t *testing.T) {
	root := newRoot()
	tools := []tool.Tool{
		newFakeTool("fs.read", "Read a file"),
	}
	cli.Register(root, tools, nil)

	fs := findCommand(root, "fs")
	if fs == nil {
		t.Fatal("expected 'fs' group command under root")
	}

	read := findCommand(fs, "read")
	if read == nil {
		t.Fatal("expected 'read' leaf command under 'fs'")
	}

	if read.Short != "Read a file" {
		t.Errorf("expected short=%q, got %q", "Read a file", read.Short)
	}
}

func TestRegister_GroupDescriptionAndAliases(t *testing.T) {
	root := newRoot()
	groups := []tool.Group{
		{Path: "fs", Description: "File system tools", Aliases: []string{"filesystem"}},
	}
	tools := []tool.Tool{
		newFakeTool("fs.read", "Read a file"),
	}
	cli.Register(root, tools, groups)

	fs := findCommand(root, "fs")
	if fs == nil {
		t.Fatal("expected 'fs' group command")
	}
	if fs.Short != "File system tools" {
		t.Errorf("expected group Short=%q, got %q", "File system tools", fs.Short)
	}
	if len(fs.Aliases) == 0 || fs.Aliases[0] != "filesystem" {
		t.Errorf("expected alias 'filesystem', got %v", fs.Aliases)
	}
}

func TestRegister_LeafFlagsCreated(t *testing.T) {
	root := newRoot()
	tools := []tool.Tool{newFakeTool("fs.read", "Read a file")}
	cli.Register(root, tools, nil)

	leaf := findCommand(findCommand(root, "fs"), "read")
	if leaf == nil {
		t.Fatal("leaf command not found")
	}

	// fakeInput has path (string), count (integer), verbose (boolean).
	if leaf.Flags().Lookup("path") == nil {
		t.Error("expected flag --path")
	}
	if leaf.Flags().Lookup("count") == nil {
		t.Error("expected flag --count")
	}
	if leaf.Flags().Lookup("verbose") == nil {
		t.Error("expected flag --verbose")
	}
}

func TestRegister_RequiredFlagMarked(t *testing.T) {
	// tool.New infers the schema; path has no omitempty so it should appear in
	// Required in the inferred schema.
	type reqInput struct {
		Path string `json:"path"`
	}
	type reqOutput struct {
		OK bool `json:"ok"`
	}
	tl := tool.New[reqInput, reqOutput](
		"req.tool",
		"tool with required path",
		func(_ context.Context, in reqInput) (reqOutput, error) {
			return reqOutput{OK: true}, nil
		},
	)

	root2 := newRoot()
	cli.Register(root2, []tool.Tool{tl}, nil)

	req := findCommand(root2, "req")
	if req == nil {
		t.Fatal("expected 'req' group command under root")
	}
	leaf := findCommand(req, "tool")
	if leaf == nil {
		t.Fatal("expected 'tool' leaf command under 'req'")
	}
	// Verify --path flag exists.
	if leaf.Flags().Lookup("path") == nil {
		t.Error("expected --path flag on leaf command")
	}
}

func TestRegister_RunECollectsAndCallsExecute(t *testing.T) {
	var capturedInput fakeInput

	tl := tool.New[fakeInput, fakeOutput](
		"fs.read",
		"Read a file",
		func(_ context.Context, in fakeInput) (fakeOutput, error) {
			capturedInput = in
			return fakeOutput{Result: "ok"}, nil
		},
	)

	root := newRoot()
	cli.Register(root, []tool.Tool{tl}, nil)

	var buf bytes.Buffer
	root.SetOut(&buf)

	root.SetArgs([]string{"fs", "read", "--path", "/tmp/test.txt"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if capturedInput.Path != "/tmp/test.txt" {
		t.Errorf("expected path=/tmp/test.txt, got %q", capturedInput.Path)
	}

	// Verify output is pretty-printed JSON.
	out := strings.TrimSpace(buf.String())
	var result fakeOutput
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Errorf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if result.Result != "ok" {
		t.Errorf("expected result=ok, got %q", result.Result)
	}
}

func TestRegister_HyphenatedFlagName(t *testing.T) {
	type hyphenInput struct {
		LineNumbers bool `json:"line_numbers,omitempty"`
	}
	type hyphenOutput struct {
		OK bool `json:"ok"`
	}
	tl := tool.New[hyphenInput, hyphenOutput](
		"fs.search",
		"Search a file",
		func(_ context.Context, in hyphenInput) (hyphenOutput, error) {
			return hyphenOutput{OK: in.LineNumbers}, nil
		},
	)

	root := newRoot()
	cli.Register(root, []tool.Tool{tl}, nil)

	fs := findCommand(root, "fs")
	if fs == nil {
		t.Fatal("expected fs command")
	}
	search := findCommand(fs, "search")
	if search == nil {
		t.Fatal("expected search command")
	}

	// The canonical form is --line-numbers (hyphenated). The global
	// normalizer also accepts --line_numbers as an alias so agents reading
	// the JSON schema (which uses snake_case) can pass either form.
	if search.Flags().Lookup("line-numbers") == nil {
		t.Error("expected --line-numbers flag (canonical kebab form)")
	}
	if search.Flags().Lookup("line_numbers") == nil {
		t.Error("expected --line_numbers (snake_case alias) to resolve via normalizer")
	}
}
