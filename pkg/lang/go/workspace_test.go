// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
)

// executeWorkspace marshals input through the tool's JSON entry point and
// returns the decoded WorkspaceOutput, mirroring the pattern used by the
// other lang.go.* tests (see executeSearchTyped, decodeExploreOutput).
func executeWorkspace(t *testing.T, input lang.WorkspaceInput) lang.WorkspaceOutput {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := golang.Workspace.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if v, ok := result.(lang.WorkspaceOutput); ok {
		return v
	}
	var out lang.WorkspaceOutput
	b, _ := json.Marshal(result)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

// writeSingleModuleWorkspace builds a small single-module fixture with two
// packages: the root (with a documented Greet function) and a subpackage
// (with one exported type and one unexported helper). Returns the module
// root.
func writeSingleModuleWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"go.mod": "module example.com/ws\n\ngo 1.21\n",
		"greet.go": `// Package ws is the root of the workspace test fixture.
// It exists to exercise lang.go.workspace's single-module path.
package ws

// Greet returns a friendly greeting for name.
func Greet(name string) string { return "hello, " + name }

// internalHelper is unexported and should only be counted when
// include_private is true.
func internalHelper() {}
`,
		"sub/sub.go": `// Package sub is a child package under the workspace root.
package sub

// Widget is a unit of work.
type Widget struct {
	// Name identifies the widget.
	Name string
}

// helperConst is unexported.
const helperConst = 1
`,
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return dir
}

func TestWorkspaceSingleModule(t *testing.T) {
	dir := writeSingleModuleWorkspace(t)
	t.Chdir(dir)

	out := executeWorkspace(t, lang.WorkspaceInput{})
	if out.IsGoWork {
		t.Errorf("expected is_go_work=false for single-module workspace; got true")
	}
	if len(out.Modules) != 1 {
		t.Errorf("expected one module; got %d (%+v)", len(out.Modules), out.Modules)
	}
	if out.Modules[0].Path != "example.com/ws" {
		t.Errorf("unexpected module path: %q", out.Modules[0].Path)
	}

	rootPkg, ok := out.Packages["example.com/ws"]
	if !ok {
		t.Fatalf("expected example.com/ws in packages; got keys %v", pkgKeys(out))
	}
	if rootPkg.Name != "ws" {
		t.Errorf("unexpected name for root package: %q", rootPkg.Name)
	}
	if rootPkg.ExportedSyms != 1 {
		t.Errorf("expected 1 exported symbol (Greet) in root; got %d (%v)", rootPkg.ExportedSyms, rootPkg.SymbolOrder)
	}
	if !slices.Contains(rootPkg.SymbolOrder, "Greet") {
		t.Errorf("expected Greet in symbol_order; got %v", rootPkg.SymbolOrder)
	}
	if !strings.HasPrefix(rootPkg.PackageDoc, "Package ws is the root") {
		t.Errorf("unexpected package_doc: %q", rootPkg.PackageDoc)
	}

	subPkg, ok := out.Packages["example.com/ws/sub"]
	if !ok {
		t.Fatalf("expected example.com/ws/sub in packages; got keys %v", pkgKeys(out))
	}
	if subPkg.ExportedSyms != 1 {
		t.Errorf("expected 1 exported symbol (Widget) in sub; got %d", subPkg.ExportedSyms)
	}
}

func TestWorkspaceGoWork(t *testing.T) {
	twoModuleWorkspace(t, map[string]string{
		"foo.go": `// Package a is module A's root.
package a

// Foo is the answer.
const Foo = 42
`,
	}, map[string]string{
		"bar.go": `// Package b is module B's root.
package b

// Bar wraps Foo.
func Bar() int { return 0 }
`,
	})

	out := executeWorkspace(t, lang.WorkspaceInput{})
	if !out.IsGoWork {
		t.Errorf("expected is_go_work=true for go.work workspace")
	}
	if len(out.Modules) != 2 {
		t.Errorf("expected two modules; got %d (%+v)", len(out.Modules), out.Modules)
	}
	wantMods := map[string]bool{"example.com/a": false, "example.com/b": false}
	for _, m := range out.Modules {
		if _, ok := wantMods[m.Path]; ok {
			wantMods[m.Path] = true
		}
	}
	for path, found := range wantMods {
		if !found {
			t.Errorf("missing module %q in output (got %+v)", path, out.Modules)
		}
	}

	if _, ok := out.Packages["example.com/a"]; !ok {
		t.Errorf("expected example.com/a in packages; got %v", pkgKeys(out))
	}
	if _, ok := out.Packages["example.com/b"]; !ok {
		t.Errorf("expected example.com/b in packages; got %v", pkgKeys(out))
	}
}

func TestWorkspaceIncludePrivate(t *testing.T) {
	dir := writeSingleModuleWorkspace(t)
	t.Chdir(dir)

	t.Run("default omits internal counts", func(t *testing.T) {
		out := executeWorkspace(t, lang.WorkspaceInput{})
		rootPkg := out.Packages["example.com/ws"]
		if rootPkg.InternalSyms != 0 {
			t.Errorf("expected internal_syms=0 by default; got %d", rootPkg.InternalSyms)
		}
	})

	t.Run("include_private populates internal counts", func(t *testing.T) {
		out := executeWorkspace(t, lang.WorkspaceInput{IncludePrivate: true})
		rootPkg := out.Packages["example.com/ws"]
		// One unexported function: internalHelper.
		if rootPkg.InternalSyms != 1 {
			t.Errorf("expected internal_syms=1 with include_private; got %d", rootPkg.InternalSyms)
		}
		// Exported count must not change.
		if rootPkg.ExportedSyms != 1 {
			t.Errorf("exported_syms changed unexpectedly: %d", rootPkg.ExportedSyms)
		}

		subPkg := out.Packages["example.com/ws/sub"]
		// sub has one unexported const helperConst.
		if subPkg.InternalSyms != 1 {
			t.Errorf("expected sub internal_syms=1; got %d", subPkg.InternalSyms)
		}
	})
}

func pkgKeys(out lang.WorkspaceOutput) []string {
	keys := make([]string, 0, len(out.Packages))
	for k := range out.Packages {
		keys = append(keys, k)
	}
	return keys
}
