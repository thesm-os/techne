// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImplement(t *testing.T) {
	t.Run("generates stubs for all missing methods", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "store/store.go", `package store

type Store interface {
	Get(key string) string
	Put(key, val string)
	Delete(key string) error
}

type MemStore struct{}
`)

		out := runRefactor(t, dir, Input{
			Action:       ActionImplementInterface,
			TargetStruct: "MemStore",
			Interface:    "Store",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		content, _ := os.ReadFile(filepath.Join(dir, "store/store.go"))
		for _, method := range []string{"Get", "Put", "Delete"} {
			if !strings.Contains(string(content), "func (m *MemStore) "+method+"(") {
				t.Errorf("stub for %s not found", method)
			}
		}
		if !strings.Contains(string(content), `panic("not implemented")`) {
			t.Error("default stub body not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("already implemented — reports skipped, no duplicates", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "io2/io2.go", `package io2

type Reader interface {
	Read(p []byte) (int, error)
}

type MyReader struct{}

func (r *MyReader) Read(p []byte) (int, error) { return 0, nil }
`)

		out, err := Handle(t.Context(), Input{
			Action:       ActionImplementInterface,
			Package:      dir,
			TargetStruct: "MyReader",
			Interface:    "Reader",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		found := false
		for _, r := range out.Results {
			if r.Status == "skipped" {
				found = true
			}
		}
		if !found {
			t.Error("expected a 'skipped' result when struct already implements interface")
		}
	})

	t.Run("partial — only missing methods stubbed", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "cache/cache.go", `package cache

type Cache interface {
	Get(key string) (string, bool)
	Set(key, val string)
	Delete(key string)
	Flush()
	Size() int
}

type SimpleCache struct{}

func (c *SimpleCache) Get(key string) (string, bool) { return "", false }
func (c *SimpleCache) Set(key, val string)           {}
`)

		out := runRefactor(t, dir, Input{
			Action:       ActionImplementInterface,
			TargetStruct: "SimpleCache",
			Interface:    "Cache",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		content, _ := os.ReadFile(filepath.Join(dir, "cache/cache.go"))
		s := string(content)
		for _, method := range []string{"Delete", "Flush", "Size"} {
			if !strings.Contains(s, "func (s *SimpleCache) "+method) {
				t.Errorf("expected stub for %s, not found", method)
			}
		}
		// Generated stubs use receiver 's'; original uses 'c'.
		if strings.Count(s, "func (s *SimpleCache) Get(") != 0 {
			t.Error("Get should not be generated as a stub (already implemented)")
		}
		if strings.Count(s, "func (c *SimpleCache) Get(") != 1 {
			t.Error("original Get method should appear exactly once")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("external interface — io.Reader", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "rdr/rdr.go", "package rdr\n\ntype MyReader struct{}\n")

		out := runRefactor(t, dir, Input{
			Action:       ActionImplementInterface,
			TargetStruct: "MyReader",
			Interface:    "io.Reader",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		content, _ := os.ReadFile(filepath.Join(dir, "rdr/rdr.go"))
		s := string(content)
		if !strings.Contains(s, "func (m *MyReader) Read(") {
			t.Errorf("Read stub not found in:\n%s", s)
		}
		if !strings.Contains(s, "[]byte") {
			t.Error("expected []byte in Read signature")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("custom stub body replaces panic", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "repo/repo.go", `package repo

type Repo interface {
	Save(v string) error
}

type FileRepo struct{}
`)

		out := runRefactor(t, dir, Input{
			Action:       ActionImplementInterface,
			TargetStruct: "FileRepo",
			Interface:    "Repo",
			StubBody:     "return nil",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filepath.Join(dir, "repo/repo.go"))
		s := string(content)
		if strings.Contains(s, "panic") {
			t.Error("default panic stub should not appear when custom body provided")
		}
		if !strings.Contains(s, "return nil") {
			t.Error("custom stub body not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("missing struct — returns error", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg.go", "package main\n\nfunc main() {}\n")

		_, err := Handle(t.Context(), Input{
			Action:       ActionImplementInterface,
			Package:      dir,
			TargetStruct: "NoSuchStruct",
			Interface:    "io.Reader",
		})
		if err == nil {
			t.Fatal("expected error for missing struct, got nil")
		}
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "svc/svc.go", `package svc

type Worker interface {
	Work() string
}

type Bot struct{}
`)

		filePath := filepath.Join(dir, "svc/svc.go")
		original, _ := os.ReadFile(filePath)

		out, err := Handle(t.Context(), Input{
			Action:       ActionImplementInterface,
			Package:      dir,
			TargetStruct: "Bot",
			Interface:    "Worker",
			DryRun:       true,
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
