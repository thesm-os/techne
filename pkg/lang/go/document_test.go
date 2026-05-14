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

func TestDocument(t *testing.T) {
	t.Run("struct field doc lands above field with correct indent", func(t *testing.T) {
		dir := writeMod(t, "docfield", map[string]string{
			"a.go": "package docfield\n\ntype User struct {\n\tID    int\n\tEmail string\n}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "User.ID", Doc: "ID is the unique identifier."},
				{Symbol: "User.Email", Doc: "Email is the user's verified email address."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		// gofmt drops cross-field column alignment when doc comments are
		// interleaved between fields, so we don't assert on the original
		// `ID    int` spacing — only that the doc precedes the field with
		// a tab indent.
		if !strings.Contains(body, "\t// ID is the unique identifier.\n\tID int") {
			t.Errorf("ID field doc misplaced or wrong indent; got:\n%s", body)
		}
		if !strings.Contains(body, "\t// Email is the user's verified email address.\n\tEmail string") {
			t.Errorf("Email field doc misplaced; got:\n%s", body)
		}
	})

	t.Run("embedded field doc is placed correctly", func(t *testing.T) {
		dir := writeMod(t, "docembed", map[string]string{
			"a.go": "package docembed\n\ntype Base struct{}\n\ntype Child struct {\n\t*Base\n\tName string\n}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Child.Base", Doc: "Base is embedded so promoted methods carry over."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "// Base is embedded so promoted methods carry over.") {
			t.Errorf("embedded-field doc not placed; got:\n%s", body)
		}
	})

	t.Run("per-spec const doc lands above each spec not above the GenDecl", func(t *testing.T) {
		dir := writeMod(t, "docspec", map[string]string{
			"a.go": "package docspec\n\nconst (\n\tActive   = \"active\"\n\tInactive = \"inactive\"\n)\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Active", Doc: "Active means the entity is currently in service."},
				{Symbol: "Inactive", Doc: "Inactive means the entity is decommissioned."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "\t// Active means the entity is currently in service.\n\tActive") {
			t.Errorf("Active doc misplaced; got:\n%s", body)
		}
		if !strings.Contains(body, "\t// Inactive means the entity is decommissioned.\n\tInactive") {
			t.Errorf("Inactive doc misplaced; got:\n%s", body)
		}
		// Const opening line must remain unchanged (no group-level doc).
		if strings.Contains(body, "// Active") && strings.Contains(body, "const (\n// ") {
			t.Errorf("doc should not appear above the const ( line; got:\n%s", body)
		}
	})

	t.Run("multi-file batch documents symbols across both files", func(t *testing.T) {
		dir := writeMod(t, "docmulti", map[string]string{
			"types.go":   "package docmulti\n\ntype Order struct{ ID int }\n",
			"handler.go": "package docmulti\n\nfunc Handle(o Order) int { return o.ID }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			Files: []lang.FileDocs{
				{File: filepath.Join(dir, "types.go"), Comments: []lang.SymbolDoc{
					{Symbol: "Order", Doc: "Order represents a single purchase request."},
				}},
				{File: filepath.Join(dir, "handler.go"), Comments: []lang.SymbolDoc{
					{Symbol: "Handle", Doc: "Handle processes the order and returns its ID."},
				}},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		if !strings.Contains(mustReadFile(t, filepath.Join(dir, "types.go")), "// Order represents") {
			t.Errorf("types.go doc not applied")
		}
		if !strings.Contains(mustReadFile(t, filepath.Join(dir, "handler.go")), "// Handle processes") {
			t.Errorf("handler.go doc not applied")
		}
	})

	t.Run("multi-file atomic rollback when one symbol is missing", func(t *testing.T) {
		dir := writeMod(t, "docmultiroll", map[string]string{
			"a.go": "package docmultiroll\n\nfunc A() {}\n",
			"b.go": "package docmultiroll\n\nfunc B() {}\n",
		})
		t.Chdir(dir)

		originalA := mustReadFile(t, filepath.Join(dir, "a.go"))
		originalB := mustReadFile(t, filepath.Join(dir, "b.go"))

		_, err := executeRefactorRaw(t, golang.Document, lang.DocumentInput{
			Files: []lang.FileDocs{
				{File: filepath.Join(dir, "a.go"), Comments: []lang.SymbolDoc{
					{Symbol: "A", Doc: "A is documented."},
				}},
				{File: filepath.Join(dir, "b.go"), Comments: []lang.SymbolDoc{
					{Symbol: "DoesNotExist", Doc: "This will fail."},
				}},
			},
		})
		if err == nil {
			t.Fatal("expected error for missing symbol in second file")
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != originalA {
			t.Errorf("a.go must roll back; got:\n%s", got)
		}
		if got := mustReadFile(t, filepath.Join(dir, "b.go")); got != originalB {
			t.Errorf("b.go must roll back; got:\n%s", got)
		}
	})

	t.Run("backtick param refs matching parameter names are accepted", func(t *testing.T) {
		dir := writeMod(t, "docparams", map[string]string{
			"a.go": "package docparams\n\nfunc Format(s string, width int) string { return s }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Format", Doc: "Format truncates `s` to fit `width` columns."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
	})

	t.Run("backtick param ref not matching any parameter is rejected", func(t *testing.T) {
		dir := writeMod(t, "docparamsbad", map[string]string{
			"a.go": "package docparamsbad\n\nfunc Format(s string, width int) string { return s }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		_, err := executeRefactorRaw(t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Format", Doc: "Format truncates `text` to fit `width` columns."},
			},
		})
		if err == nil {
			t.Fatal("expected validation error: `text` is not a parameter")
		}
		if !strings.Contains(err.Error(), "`text`") {
			t.Errorf("error should name the offending backtick; got: %v", err)
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
			t.Errorf("source must be untouched; got:\n%s", got)
		}
	})

	t.Run("named return value is a valid backtick reference", func(t *testing.T) {
		dir := writeMod(t, "docparamsret", map[string]string{
			"a.go": "package docparamsret\n\nfunc Compute(x int) (result int, err error) { return x, nil }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Compute", Doc: "Compute squares `x` and yields it through `result`."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
	})

	t.Run("backtick refs on non-function symbol are not validated", func(t *testing.T) {
		dir := writeMod(t, "docparamstype", map[string]string{
			"a.go": "package docparamstype\n\ntype Status int\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Status", Doc: "Status uses values like `42` and `unknown_thing`."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
	})

	t.Run("list missing reports exported symbols without doc comments", func(t *testing.T) {
		dir := writeMod(t, "doclist", map[string]string{
			"a.go": "package doclist\n\n" +
				"// Documented is doc'd.\nfunc Documented() {}\n\n" +
				"func Undocumented() {}\n\n" +
				"type User struct {\n" +
				"\tID int\n" +
				"\tEmail string\n" +
				"}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File:        filepath.Join(dir, "a.go"),
			ListMissing: true,
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		if len(out.Results) == 0 {
			t.Fatalf("expected at least one FileResult; got %+v", out)
		}
		msg := out.Results[0].Message
		for _, want := range []string{"Undocumented", "User", "User.ID", "User.Email"} {
			if !strings.Contains(msg, want) {
				t.Errorf("missing-list should include %q; got %q", want, msg)
			}
		}
		if strings.Contains(msg, "Documented,") || strings.Contains(msg, "Documented ") {
			t.Errorf("missing-list must not include the already-documented symbol; got %q", msg)
		}
	})

	t.Run("list missing does not edit the file", func(t *testing.T) {
		dir := writeMod(t, "doclistnoop", map[string]string{
			"a.go": "package doclistnoop\n\nfunc Undocumented() {}\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File:        filepath.Join(dir, "a.go"),
			ListMissing: true,
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q", out.Status)
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
			t.Errorf("list_missing must not modify the file; got:\n%s", got)
		}
	})

	t.Run("deprecated paragraph is preserved when replacing doc", func(t *testing.T) {
		dir := writeMod(t, "docdeprecated", map[string]string{
			"a.go": "package docdeprecated\n\n" +
				"// Old is the old API.\n" +
				"//\n" +
				"// Deprecated: use NewAPI instead.\n" +
				"func Old() {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Old", Doc: "Old performs the legacy operation."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "// Old performs the legacy operation.") {
			t.Errorf("new doc not applied; got:\n%s", body)
		}
		if !strings.Contains(body, "// Deprecated: use NewAPI instead.") {
			t.Errorf("Deprecated: paragraph must be preserved; got:\n%s", body)
		}
	})

	t.Run("deprecated paragraph is replaced when new doc also has one", func(t *testing.T) {
		dir := writeMod(t, "docdeprecatedov", map[string]string{
			"a.go": "package docdeprecatedov\n\n" +
				"// Old is old.\n" +
				"//\n" +
				"// Deprecated: use V1.\n" +
				"func Old() {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Old", Doc: "Old does the old thing.\n\nDeprecated: use V2 (V1 is also gone)."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "// Deprecated: use V2") {
			t.Errorf("new Deprecated: must land; got:\n%s", body)
		}
		if strings.Contains(body, "// Deprecated: use V1") {
			t.Errorf("old Deprecated: must be replaced when new doc has one; got:\n%s", body)
		}
	})

	t.Run("long prose is wrapped at 80 cols by default", func(t *testing.T) {
		long := "Helper formats a string for log output by truncating to a fixed width and appending an ellipsis when the input exceeds the limit; the truncation rule matches the dashboard convention."
		dir := writeMod(t, "docqlong", map[string]string{
			"a.go": "package docqlong\n\nfunc Helper(s string) string { return s }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: long},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		for line := range strings.SplitSeq(body, "\n") {
			if strings.HasPrefix(line, "//") && len(line) > 80 {
				t.Errorf("doc line exceeds 80 cols (%d): %q", len(line), line)
			}
		}
		if !strings.Contains(body, "// Helper formats a string") {
			t.Errorf("first line should retain the start of the prose; got:\n%s", body)
		}
	})

	t.Run("custom max line length caps every emitted line", func(t *testing.T) {
		long := "Helper formats a string for log output by truncating to a fixed width and appending an ellipsis when the input exceeds the limit."
		dir := writeMod(t, "docqcustom", map[string]string{
			"a.go": "package docqcustom\n\nfunc Helper(s string) string { return s }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File:          filepath.Join(dir, "a.go"),
			MaxLineLength: 60,
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: long},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		for line := range strings.SplitSeq(body, "\n") {
			if strings.HasPrefix(line, "//") && len(line) > 60 {
				t.Errorf("doc line exceeds 60 cols (%d): %q", len(line), line)
			}
		}
	})

	t.Run("no_wrap emits input as a single comment line even if very long", func(t *testing.T) {
		long := strings.Repeat("Helper formats a string. ", 5)
		dir := writeMod(t, "docqnowrap", map[string]string{
			"a.go": "package docqnowrap\n\nfunc Helper() string { return \"\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File:   filepath.Join(dir, "a.go"),
			NoWrap: true,
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: long},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		// A single doc line containing the full long text.
		hasLong := false
		for line := range strings.SplitSeq(body, "\n") {
			if strings.HasPrefix(
				line,
				"// Helper formats a string. Helper formats a string. Helper formats a string.",
			) {
				hasLong = true
				break
			}
		}
		if !hasLong {
			t.Errorf("no_wrap should emit one long line; got:\n%s", body)
		}
	})

	t.Run("preformatted blocks are not wrapped", func(t *testing.T) {
		doc := "Helper does important work.\n\n" +
			"\tfor i := 0; i < n; i++ { /* this very long preformatted block must not be wrapped because godoc preserves it as-is for code examples */ }\n"
		dir := writeMod(t, "docqpre", map[string]string{
			"a.go": "package docqpre\n\nfunc Helper(n int) {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: doc},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "//\tfor i := 0; i < n; i++") {
			t.Errorf("preformatted block must be kept as one line with `//` + tab; got:\n%s", body)
		}
	})

	t.Run("strict prefix rejects doc that does not start with symbol name", func(t *testing.T) {
		dir := writeMod(t, "docqprefix", map[string]string{
			"a.go": "package docqprefix\n\nfunc Helper() string { return \"h\" }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		_, err := executeRefactorRaw(t, golang.Document, lang.DocumentInput{
			File:         filepath.Join(dir, "a.go"),
			StrictPrefix: true,
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: "This function returns the canonical greeting."},
			},
		})
		if err == nil {
			t.Fatal("expected error for prefix mismatch")
		}
		if !strings.Contains(err.Error(), "must start with") {
			t.Errorf("error should explain the prefix mismatch; got: %v", err)
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
			t.Errorf("source must be untouched on validation failure; got:\n%s", got)
		}
	})

	t.Run("strict prefix for method uses short name not qualified name", func(t *testing.T) {
		dir := writeMod(t, "docqprefixmethod", map[string]string{
			"a.go": "package docqprefixmethod\n\ntype Cache struct{}\n\nfunc (c *Cache) Get(k string) string { return \"\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File:         filepath.Join(dir, "a.go"),
			StrictPrefix: true,
			Comments: []lang.SymbolDoc{
				{Symbol: "Cache.Get", Doc: "Get retrieves a stored value."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}

		// And the qualified-form attempt must fail.
		_, err := executeRefactorRaw(t, golang.Document, lang.DocumentInput{
			File:         filepath.Join(dir, "a.go"),
			StrictPrefix: true,
			Comments: []lang.SymbolDoc{
				{Symbol: "Cache.Get", Doc: "Cache.Get retrieves a stored value."},
			},
		})
		if err == nil {
			t.Fatal("expected error: Cache.Get prefix isn't godoc convention; should be Get")
		}
	})

	t.Run("strict prefix is off by default", func(t *testing.T) {
		dir := writeMod(t, "docqprefixoff", map[string]string{
			"a.go": "package docqprefixoff\n\nfunc Helper() string { return \"\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: "This wrapper exists for legacy reasons."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
	})

	t.Run("local symbol link resolves and doc applies cleanly", func(t *testing.T) {
		dir := writeMod(t, "docqlinkok", map[string]string{
			"a.go": "package docqlinkok\n\nfunc Sibling() {}\n\nfunc Helper() {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: "Helper does work; see also [Sibling]."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "see also [Sibling]") {
			t.Errorf("link should be preserved verbatim; got:\n%s", body)
		}
	})

	t.Run("unresolved link rejects operation and leaves file untouched", func(t *testing.T) {
		dir := writeMod(t, "docqlinkbad", map[string]string{
			"a.go": "package docqlinkbad\n\nfunc Helper() {}\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		_, err := executeRefactorRaw(t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: "Helper does work; see also [DoesNotExist]."},
			},
		})
		if err == nil {
			t.Fatal("expected validation error for unresolved link")
		}
		if !strings.Contains(err.Error(), "does not resolve") {
			t.Errorf("error should explain unresolved link; got: %v", err)
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
			t.Errorf("source must be untouched; got:\n%s", got)
		}
	})

	t.Run("qualified package link resolves when package is imported", func(t *testing.T) {
		dir := writeMod(t, "docqlinkpkg", map[string]string{
			"util/util.go": "package util\n\nfunc Helper() {}\n",
			"main.go":      "package docqlinkpkg\n\nimport _ \"docqlinkpkg/util\"\n\nfunc Use() {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "main.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Use", Doc: "Use delegates to [util.Helper] for the actual work."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
	})

	t.Run("Type.Method link resolves to method in local package", func(t *testing.T) {
		dir := writeMod(t, "docqlinkmethod", map[string]string{
			"a.go": "package docqlinkmethod\n\ntype Cache struct{}\n\nfunc (c *Cache) Get(k string) string { return \"\" }\n\nfunc UseCache() {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "UseCache", Doc: "UseCache calls [Cache.Get] under the hood."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
	})

	t.Run("no links present means validation pass is a no-op", func(t *testing.T) {
		dir := writeMod(t, "docqlinknone", map[string]string{
			"a.go": "package docqlinknone\n\nfunc Helper() {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: "Helper does work without referencing other symbols."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
	})

	t.Run("top-level func gets doc comment placed correctly", func(t *testing.T) {
		dir := writeMod(t, "doctop", map[string]string{
			"a.go": "package doctop\n\nfunc Helper() string { return \"h\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: "Helper returns the canonical greeting."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "// Helper returns the canonical greeting.\nfunc Helper()") {
			t.Errorf("doc comment not placed correctly; got:\n%s", body)
		}
	})

	t.Run("existing doc is replaced not merged or duplicated", func(t *testing.T) {
		dir := writeMod(t, "docreplace", map[string]string{
			"a.go": "package docreplace\n\n// Old doc.\nfunc Helper() string { return \"h\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Helper", Doc: "Helper is the new doc."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if strings.Contains(body, "Old doc") {
			t.Errorf("old doc must be replaced; got:\n%s", body)
		}
		if !strings.Contains(body, "// Helper is the new doc.") {
			t.Errorf("new doc not placed; got:\n%s", body)
		}
	})

	t.Run("skip_existing mode leaves existing docs and applies to undocumented symbols", func(t *testing.T) {
		dir := writeMod(t, "docskip", map[string]string{
			"a.go": "package docskip\n\n// Existing doc.\nfunc A() {}\n\nfunc B() {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Mode: "skip_existing",
			Comments: []lang.SymbolDoc{
				{Symbol: "A", Doc: "A new doc."},
				{Symbol: "B", Doc: "B new doc."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "// Existing doc.") {
			t.Errorf("existing doc must remain in skip_existing mode; got:\n%s", body)
		}
		if !strings.Contains(body, "// B new doc.") {
			t.Errorf("undocumented symbol must still receive doc; got:\n%s", body)
		}
	})

	t.Run("method doc is placed directly above method declaration", func(t *testing.T) {
		dir := writeMod(t, "docmethod", map[string]string{
			"a.go": "package docmethod\n\ntype Cache struct{}\n\nfunc (c *Cache) Get(k string) string { return \"\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Cache.Get", Doc: "Get retrieves the value associated with k, or \"\" if absent."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "// Get retrieves the value associated with k") {
			t.Errorf("method doc not placed; got:\n%s", body)
		}
		if !strings.Contains(
			body,
			"// Get retrieves the value associated with k, or \"\" if absent.\nfunc (c *Cache) Get",
		) {
			t.Errorf("method doc not placed directly above the method; got:\n%s", body)
		}
	})

	t.Run("type and const documented in same call", func(t *testing.T) {
		dir := writeMod(t, "doctype", map[string]string{
			"a.go": "package doctype\n\ntype Status int\n\nconst Active Status = 1\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Status", Doc: "Status is the state of an entity."},
				{Symbol: "Active", Doc: "Active means the entity is currently in service."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "// Status is the state of an entity.\ntype Status int") {
			t.Errorf("type doc not placed; got:\n%s", body)
		}
		if !strings.Contains(body, "// Active means the entity is currently in service.\nconst Active") {
			t.Errorf("const doc not placed; got:\n%s", body)
		}
	})

	t.Run("multiline comment preserves blank lines as empty comment lines", func(t *testing.T) {
		dir := writeMod(t, "docmulti", map[string]string{
			"a.go": "package docmulti\n\nfunc Greet() string { return \"hi\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{
					Symbol: "Greet",
					Doc:    "Greet returns the canonical greeting.\n\nThe value is intentionally short for log readability.",
				},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "// Greet returns the canonical greeting.") {
			t.Errorf("first line not formatted; got:\n%s", body)
		}
		if !strings.Contains(body, "//\n// The value is intentionally short") {
			t.Errorf("blank line between paragraphs not formatted as `//`; got:\n%s", body)
		}
	})

	t.Run("lines already prefixed with // are kept verbatim not double-prefixed", func(t *testing.T) {
		dir := writeMod(t, "docpref", map[string]string{
			"a.go": "package docpref\n\nfunc F() {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "F", Doc: "// F does something.\n//\n// More detail."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if strings.Contains(body, "// // F does something") {
			t.Errorf("tool must not double-prefix `//`; got:\n%s", body)
		}
		if !strings.Contains(body, "// F does something.") {
			t.Errorf("// prefix preserved; got:\n%s", body)
		}
	})

	t.Run("symbol not found leaves file untouched", func(t *testing.T) {
		dir := writeMod(t, "docnotfound", map[string]string{
			"a.go": "package docnotfound\n\nfunc A() {}\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		_, err := executeRefactorRaw(t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "DoesNotExist", Doc: "Should fail."},
			},
		})
		if err == nil {
			t.Fatal("expected error for missing symbol")
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
			t.Errorf("source must remain untouched on failure; got:\n%s", got)
		}
	})

	t.Run("dry run does not modify file", func(t *testing.T) {
		dir := writeMod(t, "docdry", map[string]string{
			"a.go": "package docdry\n\nfunc A() {}\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "A", Doc: "A does nothing."},
			},
			DryRun: true,
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("dry-run should report success; got status=%q", out.Status)
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
			t.Errorf("dry-run must leave source intact; got:\n%s", got)
		}
	})

	t.Run("bulk rewrite documents five symbols in one call and result compiles", func(t *testing.T) {
		dir := writeMod(t, "docbulk", map[string]string{
			"a.go": "package docbulk\n\n" +
				"type Cache struct{ data map[string]string }\n\n" +
				"func New() *Cache { return &Cache{data: map[string]string{}} }\n\n" +
				"func (c *Cache) Get(k string) string { return c.data[k] }\n" +
				"func (c *Cache) Set(k, v string) { c.data[k] = v }\n\n" +
				"const DefaultSize = 1024\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Document, lang.DocumentInput{
			File: filepath.Join(dir, "a.go"),
			Comments: []lang.SymbolDoc{
				{Symbol: "Cache", Doc: "Cache is an in-memory key/value store."},
				{Symbol: "New", Doc: "New constructs an empty Cache."},
				{Symbol: "Cache.Get", Doc: "Get returns the value for k, or empty string if absent."},
				{Symbol: "Cache.Set", Doc: "Set stores v under k, overwriting any existing value."},
				{Symbol: "DefaultSize", Doc: "DefaultSize is the default capacity hint for new Cache instances."},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		for _, want := range []string{
			"// Cache is an in-memory key/value store.\ntype Cache",
			"// New constructs an empty Cache.\nfunc New()",
			"// Get returns the value for k",
			"// Set stores v under k",
			"// DefaultSize is the default capacity hint",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q in:\n%s", want, body)
			}
		}
	})
}
