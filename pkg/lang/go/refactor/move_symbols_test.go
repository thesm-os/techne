// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestMoveSymbols(t *testing.T) {
	t.Run("basic — three symbols to one target file", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/util/util.go", `package util

func A() string { return "a" }
func B() string { return "b" }
func C() string { return "c" }
func D() string { return "d" }
`)

		out := runRefactor(t, dir, Input{
			Action: ActionMoveSymbols,
			Moves: []Move{
				{Symbol: "A", TargetFile: filepath.Join(dir, "pkg/util/abc.go")},
				{Symbol: "B", TargetFile: filepath.Join(dir, "pkg/util/abc.go")},
				{Symbol: "C", TargetFile: filepath.Join(dir, "pkg/util/abc.go")},
			},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		src := mustRead(t, filepath.Join(dir, "pkg/util/util.go"))
		if strings.Contains(src, "func A()") || strings.Contains(src, "func B()") || strings.Contains(src, "func C()") {
			t.Errorf("source should no longer contain A/B/C:\n%s", src)
		}
		if !strings.Contains(src, "func D()") {
			t.Errorf("source should still contain D:\n%s", src)
		}

		target := mustRead(t, filepath.Join(dir, "pkg/util/abc.go"))
		for _, want := range []string{"func A()", "func B()", "func C()"} {
			if !strings.Contains(target, want) {
				t.Errorf("target missing %q:\n%s", want, target)
			}
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("symbols from one source can target different files", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/util/util.go", `package util

func GetThing() string { return "g" }
func PutThing(s string) {}
func ListThings() []string { return nil }
`)

		out := runRefactor(t, dir, Input{
			Action: ActionMoveSymbols,
			Moves: []Move{
				{Symbol: "GetThing", TargetFile: filepath.Join(dir, "pkg/util/get.go")},
				{Symbol: "PutThing", TargetFile: filepath.Join(dir, "pkg/util/put.go")},
				{Symbol: "ListThings", TargetFile: filepath.Join(dir, "pkg/util/list.go")},
			},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		for fileName, wantSym := range map[string]string{
			"pkg/util/get.go":  "GetThing",
			"pkg/util/put.go":  "PutThing",
			"pkg/util/list.go": "ListThings",
		} {
			body := mustRead(t, filepath.Join(dir, fileName))
			if !strings.Contains(body, wantSym) {
				t.Errorf("%s missing %s:\n%s", fileName, wantSym, body)
			}
			for _, other := range []string{"GetThing", "PutThing", "ListThings"} {
				if other != wantSym && strings.Contains(body, "func "+other+"(") {
					t.Errorf("%s should not contain %s", fileName, other)
				}
			}
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("type brings its methods along", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/store/store.go", `package store

type Cache struct{ data map[string]string }

func (c *Cache) Get(k string) string { return c.data[k] }
func (c *Cache) Set(k, v string)      { c.data[k] = v }

type Backend struct{}

func (b *Backend) Run() {}
`)

		out := runRefactor(t, dir, Input{
			Action: ActionMoveSymbols,
			Moves: []Move{
				{Symbol: "Cache", TargetFile: filepath.Join(dir, "pkg/store/cache.go")},
				{Symbol: "Backend", TargetFile: filepath.Join(dir, "pkg/store/backend.go")},
			},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		cache := mustRead(t, filepath.Join(dir, "pkg/store/cache.go"))
		if !strings.Contains(cache, "type Cache") || !strings.Contains(cache, "func (c *Cache) Get") ||
			!strings.Contains(cache, "func (c *Cache) Set") {
			t.Errorf("cache.go must contain Cache type and its methods:\n%s", cache)
		}
		if strings.Contains(cache, "type Backend") {
			t.Errorf("cache.go should NOT contain Backend:\n%s", cache)
		}

		backend := mustRead(t, filepath.Join(dir, "pkg/store/backend.go"))
		if !strings.Contains(backend, "type Backend") || !strings.Contains(backend, "func (b *Backend) Run") {
			t.Errorf("backend.go must contain Backend and its method:\n%s", backend)
		}
	})

	t.Run("empty moves list returns error", func(t *testing.T) {
		dir := setupTestModule(t)
		_, err := Handle(t.Context(), Input{
			Action:  ActionMoveSymbols,
			Package: dir,
			Moves:   nil,
		})
		if err == nil {
			t.Fatal("expected error for empty moves; got nil")
		}
		if !strings.Contains(err.Error(), "at least one move") {
			t.Errorf("error should mention 'at least one move'; got %q", err.Error())
		}
	})

	t.Run("source/target overlap rejected", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/util/a.go", "package util\n\nfunc A() string { return \"a\" }\n")
		writeTestFile(t, dir, "pkg/util/b.go", "package util\n\nfunc B() string { return \"b\" }\n")

		_, err := Handle(t.Context(), Input{
			Action:  ActionMoveSymbols,
			Package: dir,
			Moves: []Move{
				{Symbol: "A", TargetFile: filepath.Join(dir, "pkg/util/b.go")},
				{Symbol: "B", TargetFile: filepath.Join(dir, "pkg/util/a.go")},
			},
		})
		if err == nil {
			t.Fatal("expected error for source/target overlap; got nil")
		}
		if !strings.Contains(err.Error(), "both a source and a target") {
			t.Errorf("error should mention 'both a source and a target'; got %q", err.Error())
		}
	})

	t.Run("unknown symbol fails batch atomically — nothing written", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/util/util.go", `package util

func A() string { return "a" }
func B() string { return "b" }
`)
		originalUtil := mustRead(t, filepath.Join(dir, "pkg/util/util.go"))

		_, err := Handle(t.Context(), Input{
			Action:  ActionMoveSymbols,
			Package: dir,
			Moves: []Move{
				{Symbol: "A", TargetFile: filepath.Join(dir, "pkg/util/a.go")},
				{Symbol: "DoesNotExist", TargetFile: filepath.Join(dir, "pkg/util/x.go")},
			},
		})
		if err == nil {
			t.Fatal("expected error for unknown symbol; got nil")
		}

		if got := mustRead(t, filepath.Join(dir, "pkg/util/util.go")); got != originalUtil {
			t.Errorf("source must be untouched on rollback:\nwant:\n%s\ngot:\n%s", originalUtil, got)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "pkg/util/a.go")); statErr == nil {
			t.Error("partial target file must not exist after rollback")
		}
	})
}
