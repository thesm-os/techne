// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

// Realistic multi-package fixture tests. The toy single-file modules used in
// the focused tests don't exercise the workspace-wide type-checked behaviors
// the lang.go.* tools claim. This file builds a small but realistic project
// shape (4 packages, generics, embedding, cross-package interface dispatch,
// methods on pointer-and-value receivers) and stresses each tool against it.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// complexProject builds a temp Go module with these packages:
//
//	module example.com/cx
//
//	core/        — interfaces + a generic Buffer type
//	store/       — implements core.Reader; has multiple methods
//	store/memcache/  — alternative implementor (in subpackage)
//	api/         — depends on core + store; uses interface dispatch
//	api/middleware/  — embedded-struct method shadowing
//	internal/util/   — utility used by multiple packages
//
// Each package has:
//   - exported and unexported symbols
//   - methods on both value and pointer receivers
//   - cross-package call sites
//
// Returns the absolute path of the module root.
func complexProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"go.mod": "module example.com/cx\n\ngo 1.21\n",

		// core/ — interfaces + generic Buffer
		"core/reader.go": `package core

// Reader is something that can read.
type Reader interface {
	Read(n int) ([]byte, error)
}

// ReadCloser embeds Reader — implementors need both methods.
type ReadCloser interface {
	Reader
	Close() error
}
`,
		"core/buffer.go": `package core

// Buffer is a generic FIFO buffer over any type.
type Buffer[T any] struct {
	items []T
}

// Push adds an item.
func (b *Buffer[T]) Push(item T) {
	b.items = append(b.items, item)
}

// Pop removes and returns the head item.
func (b *Buffer[T]) Pop() (T, bool) {
	var zero T
	if len(b.items) == 0 {
		return zero, false
	}
	v := b.items[0]
	b.items = b.items[1:]
	return v, true
}

// Len returns the buffer's item count.
func (b *Buffer[T]) Len() int {
	return len(b.items)
}
`,

		// store/ — implements core.Reader
		"store/store.go": `package store

import "example.com/cx/core"

// FileStore reads from a file.
type FileStore struct {
	path string
}

// Read implements core.Reader.
func (f *FileStore) Read(n int) ([]byte, error) {
	return make([]byte, n), nil
}

// Close releases the underlying file handle.
func (f *FileStore) Close() error {
	return nil
}

// guard ensures FileStore satisfies core.ReadCloser at compile time.
var _ core.ReadCloser = (*FileStore)(nil)

// New creates a FileStore for the given path.
func New(path string) *FileStore {
	return &FileStore{path: path}
}
`,

		// store/memcache/ — alternative implementor in a subpackage
		"store/memcache/cache.go": `package memcache

// MemStore reads from an in-memory map.
type MemStore struct {
	data map[string][]byte
}

// Read returns up to n bytes from the in-memory store.
func (m *MemStore) Read(n int) ([]byte, error) {
	return make([]byte, n), nil
}

// Close is a no-op for the in-memory store.
func (m *MemStore) Close() error {
	return nil
}
`,

		// api/ — uses core + store; interface-dispatched calls
		"api/handler.go": `package api

import (
	"example.com/cx/core"
	"example.com/cx/store"
)

// Handler routes requests to a backing reader.
type Handler struct {
	r core.Reader
}

// NewHandler wires the default file-store backend.
func NewHandler(path string) *Handler {
	return &Handler{r: store.New(path)}
}

// Process pulls n bytes from the backing reader.
// This is interface-dispatched — the actual call site is r.Read where
// r is typed as core.Reader, NOT store.FileStore.
func (h *Handler) Process(n int) ([]byte, error) {
	return h.r.Read(n)
}
`,

		// api/middleware/ — embeds Handler, shadows one method
		"api/middleware/logging.go": `package middleware

import "example.com/cx/api"

// Logging wraps a Handler and logs every Process call.
type Logging struct {
	*api.Handler  // embedded; inherits Process and other methods
	logs []string
}

// Process overrides the embedded version to log first.
func (l *Logging) Process(n int) ([]byte, error) {
	l.logs = append(l.logs, "process called")
	return l.Handler.Process(n)
}
`,

		// internal/util/ — used by multiple packages
		"internal/util/strings.go": `package util

// JoinNonEmpty concatenates non-empty strings with sep.
func JoinNonEmpty(sep string, parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}
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

// ---- search / explore on complex shapes ----

// ---- callers / references / implementations ----

// ---- refactor tools on complex shapes ----

// ---- verify on complex projects ----

// ---- edge cases: type aliases ----

// ---- edge cases: build tags ----

// ---- edge cases: internal/ visibility ----

// ---- edge cases: vendored dependencies ----

// ---- edge cases: cgo ----

// ---- edge cases: workspace cross-module rename ----

// ---- edge cases: _test external test packages ----

// ---- edge cases: refactor on test files ----

// ---- edge cases: init functions ----

// ---- helpers (typed dispatchers + assertions) ----

func executeSearchTyped(t *testing.T, in lang.SearchInput) lang.SearchOutput {
	t.Helper()
	raw, _ := json.Marshal(in)
	result, err := golang.Search.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("search execute: %v", err)
	}
	if v, ok := result.(lang.SearchOutput); ok {
		return v
	}
	var out lang.SearchOutput
	b, _ := json.Marshal(result)
	_ = json.Unmarshal(b, &out)
	return out
}

func executeDepsTyped(t *testing.T, tl tool.Tool, in any) lang.DepsResult {
	t.Helper()
	raw, _ := json.Marshal(in)
	result, err := tl.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("deps execute: %v", err)
	}
	if v, ok := result.(lang.DepsResult); ok {
		return v
	}
	var out lang.DepsResult
	b, _ := json.Marshal(result)
	_ = json.Unmarshal(b, &out)
	return out
}

func executeRefactorTyped(t *testing.T, tl tool.Tool, in any) refactor.Output {
	t.Helper()
	raw, _ := json.Marshal(in)
	result, err := tl.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("refactor execute: %v", err)
	}
	if v, ok := result.(refactor.Output); ok {
		return v
	}
	var out refactor.Output
	b, _ := json.Marshal(result)
	_ = json.Unmarshal(b, &out)
	return out
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

func containsSym(refs []lang.SearchResult, name string) bool {
	for _, r := range refs {
		if r.Symbol == name {
			return true
		}
	}
	return false
}

func searchResultSymbols(refs []lang.SearchResult) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Symbol
	}
	return out
}

func anyCaller(refs []lang.DepReference, callerName string) bool {
	for _, r := range refs {
		if r.CallerSymbol == callerName {
			return true
		}
	}
	return false
}

func callerNames(refs []lang.DepReference) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.CallerSymbol
	}
	return out
}

func refResultSymbols(refs []lang.DepReference) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Symbol
	}
	return out
}

func symbolNames(m map[string]lang.SymbolMetadata) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustWriteFileC(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// runGit runs `git <args>` in dir and returns its error. Used by the
// compare_to tests for baseline-commit setup; callers can skip if git
// isn't on PATH.
func runGit(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w (%s)", args, err, string(out))
	}
	return nil
}

func decodeSearchOutput(t *testing.T, result any) lang.SearchOutput {
	t.Helper()
	if v, ok := result.(lang.SearchOutput); ok {
		return v
	}
	var out lang.SearchOutput
	b, _ := json.Marshal(result)
	_ = json.Unmarshal(b, &out)
	return out
}

func decodeExploreOutput(t *testing.T, result any) lang.ExploreOutput {
	t.Helper()
	if v, ok := result.(lang.ExploreOutput); ok {
		return v
	}
	var out lang.ExploreOutput
	b, _ := json.Marshal(result)
	_ = json.Unmarshal(b, &out)
	return out
}

func jsonMarshalInput(in any) ([]byte, error) {
	return json.Marshal(in)
}

func writeFileRaw(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// requireGopls skips the test when `gopls` isn't on PATH. The fuzzy
// name-matching signal in lang.go.search comes from a gopls subprocess;
// without it the tool falls back to the doc-content scorer alone, which
// doesn't produce identical rankings. Tests that assert on
// gopls-specific behavior should call this first.
func requireGopls(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping fuzzy-search integration test")
	}
}

// twoModuleWorkspace creates a temp workspace with two modules, A and B,
// where B imports A via a replace directive. Chdirs into the workspace
// root so tests run against the multi-module layout.
func twoModuleWorkspace(t *testing.T, modAFiles, modBFiles map[string]string) {
	t.Helper()
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, "go.work"),
		[]byte("go 1.21\n\nuse (\n\t./modA\n\t./modB\n)\n"), 0o644); err != nil {
		t.Fatalf("write go.work: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "modA"), 0o755); err != nil {
		t.Fatalf("mkdir modA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "modA/go.mod"),
		[]byte("module example.com/a\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write modA/go.mod: %v", err)
	}
	for rel, content := range modAFiles {
		full := filepath.Join(root, "modA", rel)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	if err := os.MkdirAll(filepath.Join(root, "modB"), 0o755); err != nil {
		t.Fatalf("mkdir modB: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "modB/go.mod"),
		[]byte(
			"module example.com/b\n\ngo 1.21\n\nrequire example.com/a v0.0.0\n\nreplace example.com/a => ../modA\n",
		),
		0o644,
	); err != nil {
		t.Fatalf("write modB/go.mod: %v", err)
	}
	for rel, content := range modBFiles {
		full := filepath.Join(root, "modB", rel)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	t.Chdir(root)
}

func executePatchTool(t *testing.T, in golang.GoPatchInput) golang.GoPatchOutput {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := golang.GoPatch.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("patch execute: %v", err)
	}
	if v, ok := result.(golang.GoPatchOutput); ok {
		return v
	}
	var out golang.GoPatchOutput
	b, _ := json.Marshal(result)
	_ = json.Unmarshal(b, &out)
	return out
}
