// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

func TestExtractInterface(t *testing.T) {
	t.Run("generates interface from exported methods", func(t *testing.T) {
		dir := writeMod(t, "testextiface", map[string]string{
			"a.go": `package testextiface

type Cache struct{}

func (c *Cache) Get(k string) string { return "" }
func (c *Cache) Set(k, v string)     {}
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractInterface, lang.ExtractInterfaceInput{
			TargetStruct:     "Cache",
			NewInterfaceName: "CacheStore",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type CacheStore interface") {
			t.Errorf("expected CacheStore interface to be generated; got:\n%s", body)
		}
	})

	t.Run("mixed pointer and value receivers build successfully", func(t *testing.T) {
		dir := writeMod(t, "prodmixedrcv", map[string]string{
			"a.go": "package prodmixedrcv\n\n" +
				"type Service struct{}\n\n" +
				"func (s Service) Read() string { return \"r\" }\n" +
				"func (s *Service) Write(v string) {}\n\n" +
				"func Use(s *Service) string { return s.Read() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractInterface, lang.ExtractInterfaceInput{
			TargetStruct:     "Service",
			NewInterfaceName: "Servicer",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Servicer interface") {
			t.Errorf("interface not generated; got:\n%s", body)
		}
	})

	t.Run("struct with embedding includes directly-declared methods", func(t *testing.T) {
		dir := writeMod(t, "stressextiface", map[string]string{
			"a.go": "package stressextiface\n\n" +
				"type Base struct{}\n\n" +
				"func (b *Base) BaseMethod() string { return \"base\" }\n\n" +
				"type Child struct{ *Base }\n\n" +
				"func (c *Child) ChildMethod() string { return \"child\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractInterface, lang.ExtractInterfaceInput{
			TargetStruct:     "Child",
			NewInterfaceName: "Childer",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Childer interface") {
			t.Errorf("expected Childer interface generated; got:\n%s", body)
		}
		if !strings.Contains(body, "ChildMethod()") {
			t.Errorf("Childer must include directly-declared ChildMethod; got:\n%s", body)
		}
	})
}
