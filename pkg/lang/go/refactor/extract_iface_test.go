// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractInterface(t *testing.T) {
	t.Run("basic — all exported methods included", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "store/store.go", `package store

type MemStore struct{ data map[string]string }

func (s *MemStore) Get(key string) string    { return s.data[key] }
func (s *MemStore) Put(key, val string)      { s.data[key] = val }
func (s *MemStore) Delete(key string)        { delete(s.data, key) }
`)

		filePath := filepath.Join(dir, "store/store.go")
		out := runRefactor(t, dir, Input{
			Action:           ActionExtractInterface,
			TargetStruct:     "MemStore",
			NewInterfaceName: "Store",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "type Store interface") {
			t.Error("interface declaration not found")
		}
		for _, m := range []string{"Get(", "Put(", "Delete("} {
			if !strings.Contains(s, m) {
				t.Errorf("method %s not in interface", m)
			}
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("excludes unexported methods", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "svc/svc.go", `package svc

type Worker struct{}

func (w *Worker) Run() {}
func (w *Worker) stop() {}
func (w *Worker) Pause() {}
`)

		filePath := filepath.Join(dir, "svc/svc.go")
		out := runRefactor(t, dir, Input{
			Action:           ActionExtractInterface,
			TargetStruct:     "Worker",
			NewInterfaceName: "Runnable",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		ifaceStart := strings.Index(s, "type Runnable interface")
		if ifaceStart == -1 {
			t.Fatal("interface declaration not found")
		}
		ifaceEnd := strings.Index(s[ifaceStart:], "}")
		if ifaceEnd == -1 {
			t.Fatal("interface closing brace not found")
		}
		ifaceBlock := s[ifaceStart : ifaceStart+ifaceEnd+1]
		if strings.Contains(ifaceBlock, "stop") {
			t.Error("unexported 'stop' must not appear in interface")
		}
		if !strings.Contains(ifaceBlock, "Run()") {
			t.Error("exported Run() not in interface")
		}
		if !strings.Contains(ifaceBlock, "Pause()") {
			t.Error("exported Pause() not in interface")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("writes interface to target file when specified", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "repo/repo.go", `package repo

type UserRepo struct{}

func (r *UserRepo) Find(id int) string { return "" }
func (r *UserRepo) Save(val string) {}
`)

		srcFile := filepath.Join(dir, "repo/repo.go")
		ifaceFile := filepath.Join(dir, "repo/iface.go")

		out := runRefactor(t, dir, Input{
			Action:           ActionExtractInterface,
			TargetStruct:     "UserRepo",
			NewInterfaceName: "Repository",
			TargetFile:       ifaceFile,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		ifaceContent, _ := os.ReadFile(ifaceFile)
		srcContent, _ := os.ReadFile(srcFile)
		if !strings.Contains(string(ifaceContent), "type Repository interface") {
			t.Error("interface not written to target file")
		}
		if strings.Contains(string(srcContent), "type Repository interface") {
			t.Error("interface should not appear in source file when target file is specified")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("rejects struct with no exported methods", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "hidden/hidden.go", `package hidden

type Secret struct{}

func (s *Secret) hide() {}
`)

		_, err := Handle(t.Context(), Input{
			Action:           ActionExtractInterface,
			Package:          dir,
			TargetStruct:     "Secret",
			NewInterfaceName: "ISecret",
		})
		if err == nil {
			t.Fatal("expected error for struct with no exported methods, got nil")
		}
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "dry/dry.go", `package dry

type Cache struct{}

func (c *Cache) Set(key, val string) {}
func (c *Cache) Get(key string) string { return "" }
`)

		filePath := filepath.Join(dir, "dry/dry.go")
		original, _ := os.ReadFile(filePath)

		out, err := Handle(t.Context(), Input{
			Action:           ActionExtractInterface,
			Package:          dir,
			TargetStruct:     "Cache",
			NewInterfaceName: "Cacher",
			DryRun:           true,
		})
		if err != nil {
			t.Fatalf("dry run error: %v", err)
		}
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		after, _ := os.ReadFile(filePath)
		if string(after) != string(original) {
			t.Error("dry run modified file on disk")
		}
	})
}
