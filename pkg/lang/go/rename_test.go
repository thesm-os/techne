// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

func TestRename(t *testing.T) {
	t.Run("happy path — rewrites definition and call sites", func(t *testing.T) {
		dir := writeMod(t, "testrename", map[string]string{
			"a.go": "package testrename\n\nfunc OldName() int { return 42 }\n",
			"b.go": "package testrename\n\nfunc UseIt() int { return OldName() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "OldName",
			NewName: "NewName",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		mustContain(t, filepath.Join(dir, "a.go"), "func NewName()")
		mustContain(t, filepath.Join(dir, "b.go"), "return NewName()")
	})

	t.Run("symbol not found returns error", func(t *testing.T) {
		dir := writeMod(t, "testrename", map[string]string{
			"a.go": "package testrename\n",
		})
		t.Chdir(dir)

		if _, err := executeRefactorRaw(
			t,
			golang.Rename,
			lang.RenameInput{Symbol: "DoesNotExist", NewName: "Whatever"},
		); err == nil {
			t.Fatal("expected error for missing symbol")
		}
	})

	t.Run("detail=summary strips diff snippets from results", func(t *testing.T) {
		dir := writeMod(t, "testdetailsummary", map[string]string{
			"a.go": "package testdetailsummary\n\nfunc Old() int { return 1 }\n",
			"b.go": "package testdetailsummary\n\nfunc Use() int { return Old() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Old",
			NewName: "New",
			Detail:  lang.DetailSummary,
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		if len(out.Results) == 0 {
			t.Fatal("expected at least one result")
		}
		for _, r := range out.Results {
			if r.DiffSnippet != "" {
				t.Errorf("summary mode must strip DiffSnippet; got %q on %s", r.DiffSnippet, r.FilePath)
			}
			if r.FilePath == "" {
				t.Errorf("summary mode must keep FilePath; got empty on %+v", r)
			}
		}
	})

	t.Run("default detail keeps diff snippets in results", func(t *testing.T) {
		dir := writeMod(t, "testdetailstd", map[string]string{
			"a.go": "package testdetailstd\n\nfunc Old() int { return 1 }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Old",
			NewName: "New",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q", out.Status)
		}
		hasDiff := false
		for _, r := range out.Results {
			if r.DiffSnippet != "" {
				hasDiff = true
				break
			}
		}
		if !hasDiff {
			t.Errorf("standard mode must include DiffSnippet on at least one result")
		}
	})

	t.Run("dry_run leaves files unchanged on disk", func(t *testing.T) {
		dir := writeMod(t, "testdryrun", map[string]string{
			"a.go": "package testdryrun\n\nfunc OldName() int { return 1 }\n",
		})
		t.Chdir(dir)
		originalContent := readFile(t, filepath.Join(dir, "a.go"))

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "OldName",
			NewName: "NewName",
			DryRun:  true,
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		if got := readFile(t, filepath.Join(dir, "a.go")); got != originalContent {
			t.Errorf("dry_run should not modify files; got diff:\n%s", got)
		}
	})

	t.Run("complex project — across packages", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "NewHandler",
			NewName: "BuildHandler",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "api/handler.go"))
		if !strings.Contains(body, "func BuildHandler") {
			t.Errorf("expected BuildHandler declaration; got:\n%s", body)
		}
	})

	t.Run("complex project — method on generic receiver", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "Push",
			NewName: "Enqueue",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "core/buffer.go"))
		if !strings.Contains(body, "func (b *Buffer[T]) Enqueue") {
			t.Errorf("expected method renamed on generic receiver; got:\n%s", body)
		}
	})

	t.Run("complex project — ambiguous method name is rejected", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		raw, _ := json.Marshal(lang.RenameInput{Symbol: "Read", NewName: "Fetch"})
		_, err := golang.Rename.Execute(t.Context(), raw)
		if err == nil {
			t.Fatal("expected rename to refuse ambiguous symbol 'Read'; got success")
		}
		if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "ambiguous") {
			t.Errorf("expected error to mention not-found/ambiguous; got %q", err.Error())
		}
	})

	// Renaming an interface method (Reader.Read) updates the interface and
	// call sites, but NOT the implementor methods. The build then fails and
	// the whole rename rolls back atomically.
	t.Run("complex project — interface method rename rolls back when implementors break", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)
		originalReader := mustReadFile(t, filepath.Join(dir, "core/reader.go"))
		originalStore := mustReadFile(t, filepath.Join(dir, "store/store.go"))

		raw, _ := json.Marshal(lang.RenameInput{Symbol: "Reader.Read", NewName: "Fetch"})
		_, err := golang.Rename.Execute(t.Context(), raw)
		if err == nil {
			t.Fatal("expected interface-method rename to fail because implementors aren't auto-updated; got success")
		}
		if got := mustReadFile(t, filepath.Join(dir, "core/reader.go")); got != originalReader {
			t.Errorf("core/reader.go should be restored on rollback; got diff:\n%s", got)
		}
		if got := mustReadFile(t, filepath.Join(dir, "store/store.go")); got != originalStore {
			t.Errorf("store/store.go should be restored on rollback; got diff:\n%s", got)
		}
	})

	// Type alias (= Underlying) has same identity; rename updates only the alias.
	// Defined type (Underlying) and new-type variant must be untouched.
	t.Run("complex project — type alias vs definition", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFileC(t, filepath.Join(dir, "go.mod"), "module ex/alias\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(dir, "types.go"), `package alias

type Underlying struct{ V int }

// Alias points at the same underlying type.
type Alias = Underlying

// Definition is a NEW named type built on Underlying.
type Definition Underlying

func UseAlias() Alias        { return Alias{V: 1} }
func UseDefinition() Definition { return Definition{V: 1} }
`)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "Alias",
			NewName: "Renamed",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("alias rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "types.go"))
		if !strings.Contains(body, "type Renamed = Underlying") {
			t.Errorf("expected alias declaration renamed; got:\n%s", body)
		}
		if !strings.Contains(body, "type Underlying struct") {
			t.Errorf("Underlying must be untouched; got:\n%s", body)
		}
		if !strings.Contains(body, "type Definition Underlying") {
			t.Errorf("Definition must be untouched (different type identity); got:\n%s", body)
		}
		if !strings.Contains(body, "func UseAlias() Renamed") {
			t.Errorf("alias usage in UseAlias should reference the new name; got:\n%s", body)
		}
	})

	t.Run("complex project — internal package symbol and its importers", func(t *testing.T) {
		dir := complexProject(t)
		mustWriteFileC(t, filepath.Join(dir, "api/labels.go"), `package api

import "example.com/cx/internal/util"

func MakeLabel(parts ...string) string {
	return util.JoinNonEmpty(", ", parts...)
}
`)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "JoinNonEmpty",
			NewName: "JoinNotEmpty",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("internal rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		utilBody := mustReadFile(t, filepath.Join(dir, "internal/util/strings.go"))
		if !strings.Contains(utilBody, "func JoinNotEmpty") {
			t.Errorf("expected JoinNotEmpty in util/strings.go; got:\n%s", utilBody)
		}
		apiBody := mustReadFile(t, filepath.Join(dir, "api/labels.go"))
		if !strings.Contains(apiBody, "util.JoinNotEmpty") {
			t.Errorf("expected importer call site updated; got:\n%s", apiBody)
		}
	})

	t.Run("complex project — symbol used inside init() is updated", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFileC(t, filepath.Join(dir, "go.mod"), "module ex/initpkg\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(dir, "boot.go"), `package initpkg

var Cache = map[string]int{}

func register(name string, v int) {
	Cache[name] = v
}

func init() {
	register("default", 0)
	register("alpha", 1)
}
`)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "register",
			NewName: "addEntry",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "boot.go"))
		if !strings.Contains(body, "func addEntry(") {
			t.Errorf("expected addEntry declaration; got:\n%s", body)
		}
		if strings.Contains(body, "register(") {
			t.Errorf("expected all register() calls including init() to be renamed; got:\n%s", body)
		}
		if !strings.Contains(body, `addEntry("default", 0)`) || !strings.Contains(body, `addEntry("alpha", 1)`) {
			t.Errorf("expected init() body to use addEntry; got:\n%s", body)
		}
	})

	t.Run("complex project — white-box test file call sites updated", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFileC(t, filepath.Join(dir, "go.mod"), "module ex/whitebox\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(dir, "core.go"), `package whitebox

func helper() string { return "h" }
`)
		mustWriteFileC(t, filepath.Join(dir, "core_test.go"), `package whitebox

import "testing"

func TestHelper(t *testing.T) {
	if helper() != "h" {
		t.Fail()
	}
}
`)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "helper",
			NewName: "helperFn",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("white-box rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "core_test.go"))
		if !strings.Contains(body, "helperFn()") {
			t.Errorf("expected test to call renamed helperFn; got:\n%s", body)
		}
	})

	t.Run("go.work workspace — rename across modules", func(t *testing.T) {
		root := t.TempDir()
		mustWriteFileC(t, filepath.Join(root, "modA/go.mod"), "module example.com/a\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(root, "modA/api.go"), `package a

func OldName() string { return "from a" }
`)
		mustWriteFileC(t, filepath.Join(root, "modB/go.mod"),
			"module example.com/b\n\ngo 1.21\n\nrequire example.com/a v0.0.0\n\nreplace example.com/a => ../modA\n")
		mustWriteFileC(t, filepath.Join(root, "modB/main.go"), `package b

import "example.com/a"

func Caller() string { return a.OldName() }
`)
		mustWriteFileC(t, filepath.Join(root, "go.work"), "go 1.21\n\nuse (\n\t./modA\n\t./modB\n)\n")
		t.Chdir(root)

		out := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "OldName",
			NewName: "NewName",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("cross-module workspace rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		aBody := mustReadFile(t, filepath.Join(root, "modA/api.go"))
		if !strings.Contains(aBody, "func NewName") {
			t.Errorf("expected modA/api.go to declare NewName; got:\n%s", aBody)
		}
		bBody := mustReadFile(t, filepath.Join(root, "modB/main.go"))
		if !strings.Contains(bBody, "a.NewName()") {
			t.Errorf("expected modB/main.go to call a.NewName; got:\n%s", bBody)
		}
	})

	t.Run("external test package (package foo_test) references updated", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFileC(t, filepath.Join(dir, "go.mod"), "module ex/extpkg\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(dir, "core.go"), `package extpkg

func OldFunc() int { return 42 }
`)
		mustWriteFileC(t, filepath.Join(dir, "core_test.go"), `package extpkg_test

import (
	"testing"

	"ex/extpkg"
)

func TestOldFunc(t *testing.T) {
	if extpkg.OldFunc() != 42 {
		t.Fail()
	}
}
`)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "OldFunc",
			NewName: "NewFunc",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		coreBody := mustReadFile(t, filepath.Join(dir, "core.go"))
		if !strings.Contains(coreBody, "func NewFunc") {
			t.Errorf("expected NewFunc declaration; got:\n%s", coreBody)
		}
		testBody := mustReadFile(t, filepath.Join(dir, "core_test.go"))
		if !strings.Contains(testBody, "extpkg.NewFunc()") {
			t.Errorf("expected external test package to use renamed symbol; got:\n%s", testBody)
		}
	})

	t.Run("struct type", func(t *testing.T) {
		dir := writeMod(t, "kindsstruct", map[string]string{
			"a.go": "package kindsstruct\n\n" +
				"type Order struct{ ID int }\n\n" +
				"func New() *Order { return &Order{ID: 1} }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Order", NewName: "Purchase",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename struct failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Purchase struct") || !strings.Contains(body, "*Purchase") {
			t.Errorf("struct not renamed consistently; got:\n%s", body)
		}
	})

	t.Run("interface type", func(t *testing.T) {
		dir := writeMod(t, "kindsiface", map[string]string{
			"a.go": "package kindsiface\n\n" +
				"type Reader interface{ Read() string }\n\n" +
				"func Use(r Reader) string { return r.Read() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Reader", NewName: "Source",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename interface failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Source interface") || !strings.Contains(body, "r Source") {
			t.Errorf("interface not renamed consistently; got:\n%s", body)
		}
	})

	t.Run("named type with underlying kind", func(t *testing.T) {
		dir := writeMod(t, "kindsnamed", map[string]string{
			"a.go": "package kindsnamed\n\n" +
				"type Status int\n\n" +
				"const Active Status = 1\n\n" +
				"func IsActive(s Status) bool { return s == Active }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Status", NewName: "State",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename named type failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type State int") || !strings.Contains(body, "Active State") ||
			!strings.Contains(body, "s State") {
			t.Errorf("named type not renamed consistently; got:\n%s", body)
		}
	})

	t.Run("constant", func(t *testing.T) {
		dir := writeMod(t, "kindsconst", map[string]string{
			"a.go": "package kindsconst\n\n" +
				"const MaxRetries = 3\n\n" +
				"func Run() int { return MaxRetries }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "MaxRetries", NewName: "RetryLimit",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename constant failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "const RetryLimit") || !strings.Contains(body, "return RetryLimit") {
			t.Errorf("constant not renamed consistently; got:\n%s", body)
		}
	})

	t.Run("package-level variable", func(t *testing.T) {
		dir := writeMod(t, "kindsvar", map[string]string{
			"a.go": "package kindsvar\n\n" +
				"var DefaultClient = \"http\"\n\n" +
				"func Get() string { return DefaultClient }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "DefaultClient", NewName: "DefaultTransport",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename package-level variable failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "var DefaultTransport") || !strings.Contains(body, "return DefaultTransport") {
			t.Errorf("variable not renamed consistently; got:\n%s", body)
		}
	})

	t.Run("embedded field — embedded-field reference follows type rename", func(t *testing.T) {
		dir := writeMod(t, "kindsembed", map[string]string{
			"a.go": "package kindsembed\n\n" +
				"type Inner struct{ V int }\n\n" +
				"type Outer struct{ Inner }\n\n" +
				"func Use(o Outer) int { return o.V }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Inner", NewName: "Core",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename embedded type failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Core struct") {
			t.Errorf("embedded type not renamed; got:\n%s", body)
		}
		if !strings.Contains(body, "type Outer struct{ Core }") &&
			!strings.Contains(body, "type Outer struct{\n\tCore") {
			t.Errorf("embedded reference not updated; got:\n%s", body)
		}
	})

	// Renaming `Foo` to `Bar` when `Bar` already exists must not produce a
	// duplicate symbol. The build gate catches it and rolls back.
	t.Run("new name collides with existing symbol — rolls back", func(t *testing.T) {
		dir := writeMod(t, "hardcollide", map[string]string{
			"a.go": "package hardcollide\n\nfunc Foo() int { return 1 }\nfunc Bar() int { return 2 }\nfunc Use() int { return Foo() + Bar() }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		result, err := executeRefactorRaw(t, golang.Rename, lang.RenameInput{
			Symbol:  "Foo",
			NewName: "Bar",
		})
		if err != nil {
			if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
				t.Errorf("source must remain untouched on rejection; got:\n%s", got)
			}
			return
		}
		out, _ := result.(refactor.Output)
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		switch out.Status {
		case refactor.StatusSuccess:
			if strings.Count(body, "func Bar()") > 1 {
				t.Errorf("rename succeeded but produced a duplicate symbol; got:\n%s", body)
			}
		case refactor.StatusFailure:
			if body != original {
				t.Errorf("on refactor failure, source must roll back; got:\n%s", body)
			}
		}
	})

	t.Run("type alias renamed — underlying type untouched", func(t *testing.T) {
		dir := writeMod(t, "hardalias", map[string]string{
			"a.go": "package hardalias\n\n" +
				"type Original struct{ V int }\n\n" +
				"type Alias = Original\n\n" +
				"func Use() Alias { return Alias{V: 1} }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Alias",
			NewName: "Pseudonym",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Pseudonym = Original") {
			t.Errorf("alias name not renamed; got:\n%s", body)
		}
		if !strings.Contains(body, "type Original struct") {
			t.Errorf("regression: original type must remain unchanged; got:\n%s", body)
		}
		if !strings.Contains(body, "func Use() Pseudonym") {
			t.Errorf("alias use site not updated; got:\n%s", body)
		}
	})

	// json struct tag literal content must NOT be rewritten even when it
	// textually matches the renamed field name.
	t.Run("struct field — json tag literal preserved", func(t *testing.T) {
		dir := writeMod(t, "prodtag", map[string]string{
			"a.go": "package prodtag\n\n" +
				"type User struct {\n" +
				"\tName string `json:\"name\"`\n" +
				"\tAge  int    `json:\"age\"`\n" +
				"}\n\n" +
				"func GetName(u User) string { return u.Name }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "User.Name",
			NewName: "FullName",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "FullName string") {
			t.Errorf("expected field renamed in struct definition; got:\n%s", body)
		}
		if !strings.Contains(body, "u.FullName") {
			t.Errorf("expected use site updated; got:\n%s", body)
		}
		if !strings.Contains(body, "`json:\"name\"`") {
			t.Errorf("regression: json tag literal must NOT be rewritten; got:\n%s", body)
		}
	})

	// gopls doesn't always support type-parameter renames; either it renames
	// consistently or rejects with rollback — what it must NOT do is mangle source.
	t.Run("generic type parameter — renames consistently or rejects cleanly", func(t *testing.T) {
		dir := writeMod(t, "prodtparam", map[string]string{
			"a.go": "package prodtparam\n\n" +
				"type Container[T any] struct{ items []T }\n\n" +
				"func (c *Container[T]) Add(v T) { c.items = append(c.items, v) }\n" +
				"func (c *Container[T]) Get(i int) T { return c.items[i] }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		result, err := executeRefactorRaw(t, golang.Rename, lang.RenameInput{
			Symbol:  "Container.T",
			NewName: "Elem",
		})
		if err != nil {
			if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
				t.Errorf("source must remain untouched on rejection; got:\n%s", got)
			}
			t.Skipf("type-parameter rename not supported (acceptable): %v", err)
		}
		out, _ := result.(refactor.Output)
		if out.Status != refactor.StatusSuccess {
			if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
				t.Errorf("on refactor failure, source must roll back; got:\n%s", got)
			}
			t.Skipf("type-parameter rename declined (acceptable); status=%q", out.Status)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "Container[Elem any]") || !strings.Contains(body, "items []Elem") {
			t.Errorf("type parameter not renamed consistently; got:\n%s", body)
		}
	})

	t.Run("method value reference (f := obj.Method) is updated", func(t *testing.T) {
		dir := writeMod(t, "prodmethval", map[string]string{
			"a.go": "package prodmethval\n\n" +
				"type Greeter struct{}\n\n" +
				"func (g *Greeter) Hello() string { return \"hi\" }\n\n" +
				"func Use(g *Greeter) string {\n" +
				"\tf := g.Hello\n" +
				"\treturn f()\n" +
				"}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Greeter.Hello",
			NewName: "Greet",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "func (g *Greeter) Greet()") {
			t.Errorf("expected method renamed; got:\n%s", body)
		}
		if !strings.Contains(body, "f := g.Greet") {
			t.Errorf("expected method-value reference rewritten; got:\n%s", body)
		}
	})

	// Type-set constraint syntax (~T | ~U) historically broke regex rewriters.
	t.Run("generic interface with type-set constraint", func(t *testing.T) {
		dir := writeMod(t, "rwgeniface", map[string]string{
			"a.go": "package rwgeniface\n\n" +
				"type Numeric interface{ ~int | ~int64 | ~float64 }\n\n" +
				"func Sum[T Numeric](xs []T) T {\n\tvar s T\n\tfor _, x := range xs { s += x }\n\treturn s\n}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Numeric",
			NewName: "Number",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Number interface") {
			t.Errorf("interface not renamed; got:\n%s", body)
		}
		if !strings.Contains(body, "Sum[T Number]") {
			t.Errorf("constraint use not renamed; got:\n%s", body)
		}
		if !strings.Contains(body, "~int | ~int64 | ~float64") {
			t.Errorf("type-set body must be unchanged; got:\n%s", body)
		}
	})

	t.Run("Example function call sites in test file updated", func(t *testing.T) {
		dir := writeMod(t, "rwexample", map[string]string{
			"a.go":      "package rwexample\n\nfunc Add(a, b int) int { return a + b }\n",
			"a_test.go": "package rwexample_test\n\nimport (\n\t\"fmt\"\n\t\"rwexample\"\n)\n\nfunc ExampleAdd() {\n\tfmt.Println(rwexample.Add(2, 3))\n\t// Output: 5\n}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Add",
			NewName: "Sum",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		example := mustReadFile(t, filepath.Join(dir, "a_test.go"))
		if !strings.Contains(example, "rwexample.Sum(2, 3)") {
			t.Errorf("example must reference renamed symbol; got:\n%s", example)
		}
	})

	t.Run("TestMain reference in test file updated", func(t *testing.T) {
		dir := writeMod(t, "rwtestmain", map[string]string{
			"a.go":         "package rwtestmain\n\nvar Initialized bool\n\nfunc Setup() { Initialized = true }\n",
			"main_test.go": "package rwtestmain\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) {\n\tSetup()\n\tos.Exit(m.Run())\n}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Setup",
			NewName: "Initialize",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "main_test.go"))
		if !strings.Contains(body, "Initialize()") {
			t.Errorf("TestMain reference must update; got:\n%s", body)
		}
	})

	// Renaming to a name that is already used as a package alias must be
	// caught by the build gate.
	t.Run("new name shadows imported package — build gate rejects", func(t *testing.T) {
		dir := writeMod(t, "rwshadow", map[string]string{
			"a.go":    "package rwshadow\n\nfunc Helper() string { return \"h\" }\n",
			"main.go": "package rwshadow\n\nimport \"strings\"\n\nfunc Use() string { return strings.ToUpper(Helper()) }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "main.go"))
		result, err := executeRefactorRaw(t, golang.Rename, lang.RenameInput{
			Symbol:  "Helper",
			NewName: "strings",
		})
		if err != nil {
			if got := mustReadFile(t, filepath.Join(dir, "main.go")); got != original {
				t.Errorf("source must roll back on shadow rejection; got:\n%s", got)
			}
			return
		}
		out, _ := result.(refactor.Output)
		if out.Status == refactor.StatusSuccess {
			body := mustReadFile(t, filepath.Join(dir, "main.go"))
			t.Errorf("rename to imported-package name must not silently shadow; got:\n%s", body)
		}
	})

	t.Run("struct with 30 fields — all references updated", func(t *testing.T) {
		var fields strings.Builder
		for i := range 30 {
			fields.WriteString("\tF")
			fields.WriteByte(byte('0' + i/10))
			fields.WriteByte(byte('0' + i%10))
			fields.WriteString(" int\n")
		}
		dir := writeMod(t, "rwbigstruct", map[string]string{
			"a.go": "package rwbigstruct\n\ntype Big struct {\n" + fields.String() + "}\n\nfunc New() *Big { return &Big{} }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Big",
			NewName: "Massive",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Massive struct") || !strings.Contains(body, "*Massive") {
			t.Errorf("struct not renamed consistently; got:\n%s", body)
		}
	})

	t.Run("Benchmark and Fuzz function call sites updated", func(t *testing.T) {
		dir := writeMod(t, "rwbench", map[string]string{
			"a.go":      "package rwbench\n\nfunc Process(s string) string { return s + \"!\" }\n",
			"a_test.go": "package rwbench\n\nimport \"testing\"\n\nfunc BenchmarkProcess(b *testing.B) {\n\tfor i := 0; i < b.N; i++ { Process(\"x\") }\n}\n\nfunc FuzzProcess(f *testing.F) {\n\tf.Fuzz(func(t *testing.T, s string) { Process(s) })\n}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Process",
			NewName: "Run",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a_test.go"))
		if !strings.Contains(body, "Run(\"x\")") || !strings.Contains(body, "Run(s)") {
			t.Errorf("benchmark and fuzz must update; got:\n%s", body)
		}
	})

	// Renaming Helper in package alpha must not touch Helper in package beta.
	t.Run("same symbol name in two packages — only targeted package updated", func(t *testing.T) {
		dir := writeMod(t, "rwxdistinct", map[string]string{
			"alpha/alpha.go": "package alpha\n\nfunc Helper() string { return \"alpha\" }\n",
			"beta/beta.go":   "package beta\n\nfunc Helper() string { return \"beta\" }\n",
			"main.go":        "package rwxdistinct\n\nimport (\n\t\"rwxdistinct/alpha\"\n\t\"rwxdistinct/beta\"\n)\n\nfunc Use() string { return alpha.Helper() + beta.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Helper",
			NewName: "Run",
			Package: "rwxdistinct/alpha",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		alpha := mustReadFile(t, filepath.Join(dir, "alpha/alpha.go"))
		beta := mustReadFile(t, filepath.Join(dir, "beta/beta.go"))
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(alpha, "func Run() string") {
			t.Errorf("alpha.Helper should be renamed; got:\n%s", alpha)
		}
		if !strings.Contains(beta, "func Helper() string") {
			t.Errorf("regression: beta.Helper must be untouched; got:\n%s", beta)
		}
		if !strings.Contains(main, "alpha.Run()") || !strings.Contains(main, "beta.Helper()") {
			t.Errorf("main.go must update only alpha's call; got:\n%s", main)
		}
	})

	// Renaming the TYPE `Req` must not rewrite the receiver variable `req`
	// (lowercase), which is a different scope.
	t.Run("receiver variable name unchanged when type is renamed", func(t *testing.T) {
		dir := writeMod(t, "rwxrecv", map[string]string{
			"a.go": "package rwxrecv\n\n" +
				"type Req struct{ ID int }\n\n" +
				"func (req *Req) GetID() int { return req.ID }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Req",
			NewName: "Request",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Request struct") {
			t.Errorf("type not renamed; got:\n%s", body)
		}
		if !strings.Contains(body, "(req *Request)") {
			t.Errorf("receiver name must remain `req`; got:\n%s", body)
		}
		if !strings.Contains(body, "return req.ID") {
			t.Errorf("receiver-variable use must remain unchanged; got:\n%s", body)
		}
	})

	t.Run("aliased import preserved when symbol renamed", func(t *testing.T) {
		dir := writeMod(t, "rwxalias", map[string]string{
			"util/util.go": "package util\n\nfunc Helper() string { return \"h\" }\n",
			"main.go":      "package rwxalias\n\nimport u \"rwxalias/util\"\n\nfunc Use() string { return u.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Helper",
			NewName: "Aid",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "u.Aid()") {
			t.Errorf("alias-qualified call site must update; got:\n%s", main)
		}
		if !strings.Contains(main, `import u "rwxalias/util"`) {
			t.Errorf("alias must be preserved; got:\n%s", main)
		}
	})

	t.Run("linker-injected var renamed in source (build config not updated)", func(t *testing.T) {
		dir := writeMod(t, "rwxlink", map[string]string{
			"a.go": "package rwxlink\n\nvar Version = \"unset\"\n\nfunc V() string { return Version }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Version",
			NewName: "BuildVersion",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "var BuildVersion") || !strings.Contains(body, "return BuildVersion") {
			t.Errorf("var must be renamed everywhere in source; got:\n%s", body)
		}
	})

	t.Run("chained rename then change_signature both land", func(t *testing.T) {
		dir := writeMod(t, "rwzchain", map[string]string{
			"a.go":    "package rwzchain\n\nfunc Foo() int { return 1 }\n",
			"main.go": "package rwzchain\n\nfunc Use() int { return Foo() }\n",
		})
		t.Chdir(dir)

		out1 := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Foo", NewName: "Compute",
		})
		if out1.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed: status=%q", out1.Status)
		}
		out2 := executeRefactor[refactor.Output](t, golang.ChangeSignature, lang.ChangeSignatureInput{
			Symbol: "Compute",
			AddParams: []lang.AddParameter{
				{Name: "n", Type: "int", DefaultValue: "0"},
			},
		})
		if out2.Status != refactor.StatusSuccess {
			t.Fatalf("change_signature after rename failed: status=%q", out2.Status)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "Compute(0)") {
			t.Errorf("call site must be Compute(0) after both refactors; got:\n%s", main)
		}
	})

	t.Run("unicode identifier", func(t *testing.T) {
		dir := writeMod(t, "rwzuni", map[string]string{
			"a.go": "package rwzuni\n\nfunc Π() float64 { return 3.14 }\nfunc Use() float64 { return Π() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Π", NewName: "Pi",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "func Pi()") || !strings.Contains(body, "return Pi()") {
			t.Errorf("unicode rename must land; got:\n%s", body)
		}
	})

	t.Run("deeply-nested package path (5+ levels)", func(t *testing.T) {
		dir := writeMod(t, "rwzdeep", map[string]string{
			"x/y/z/q/r/leaf.go": "package leaf\n\nfunc Helper() string { return \"deep\" }\n",
			"main.go":           "package rwzdeep\n\nimport \"rwzdeep/x/y/z/q/r\"\n\nfunc Use() string { return leaf.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Helper", NewName: "Aid",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(body, "leaf.Aid()") {
			t.Errorf("deeply-nested call site must update; got:\n%s", body)
		}
	})

	t.Run("realistic cmd/+internal/+pkg/ layout", func(t *testing.T) {
		dir := writeMod(t, "rwzlayout", map[string]string{
			"pkg/api/types.go":   "package api\n\ntype Order struct{ ID int }\n",
			"internal/db/db.go":  "package db\n\nimport \"rwzlayout/pkg/api\"\n\nfunc Save(o api.Order) error { return nil }\n",
			"cmd/server/main.go": "package main\n\nimport (\n\t\"rwzlayout/internal/db\"\n\t\"rwzlayout/pkg/api\"\n)\n\nfunc main() { _ = db.Save(api.Order{ID: 1}) }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Order", NewName: "Purchase",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		for _, p := range []string{
			"pkg/api/types.go",
			"internal/db/db.go",
			"cmd/server/main.go",
		} {
			body := mustReadFile(t, filepath.Join(dir, p))
			if strings.Contains(body, "Order") {
				t.Errorf("%s still references Order; got:\n%s", p, body)
			}
			if !strings.Contains(body, "Purchase") {
				t.Errorf("%s missing Purchase; got:\n%s", p, body)
			}
		}
	})

	// Two types both define a method called `Read`. Rename of one type's
	// Read must not touch the other's — ensures matching by type identity,
	// not just name.
	t.Run("two types with same method name — only targeted one renamed", func(t *testing.T) {
		dir := writeMod(t, "stressrenameshadow", map[string]string{
			"a.go": "package stressrenameshadow\n\n" +
				"type FileR struct{}\n" +
				"func (f *FileR) Read() string { return \"file\" }\n" +
				"type NetR struct{}\n" +
				"func (n *NetR) Read() string { return \"net\" }\n" +
				"func UseFile(f *FileR) string { return f.Read() }\n" +
				"func UseNet(n *NetR) string { return n.Read() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "FileR.Read",
			NewName: "Fetch",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "func (f *FileR) Fetch()") {
			t.Errorf("expected FileR.Read renamed to Fetch; got:\n%s", body)
		}
		if !strings.Contains(body, "func (n *NetR) Read()") {
			t.Errorf("regression: NetR.Read must NOT be renamed; got:\n%s", body)
		}
		if !strings.Contains(body, "f.Fetch()") {
			t.Errorf("FileR caller should use Fetch; got:\n%s", body)
		}
		if !strings.Contains(body, "n.Read()") {
			t.Errorf("regression: NetR caller must NOT be touched; got:\n%s", body)
		}
	})

	// The loaded-packages cache must invalidate between operations so the
	// second rename sees the post-first-rename state.
	t.Run("sequential renames invalidate package cache between operations", func(t *testing.T) {
		dir := writeMod(t, "rwzseq", map[string]string{
			"a.go": "package rwzseq\n\nfunc Foo() string { return \"x\" }\nfunc Bar() string { return Foo() }\n",
		})
		t.Chdir(dir)

		out1 := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Foo", NewName: "Alpha",
		})
		if out1.Status != refactor.StatusSuccess {
			t.Fatalf("first rename failed: status=%q", out1.Status)
		}
		out2 := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol: "Bar", NewName: "Beta",
		})
		if out2.Status != refactor.StatusSuccess {
			t.Fatalf("second rename failed: status=%q results=%+v", out2.Status, out2.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "func Alpha()") || !strings.Contains(body, "func Beta()") {
			t.Errorf("both renames must land; got:\n%s", body)
		}
		if !strings.Contains(body, "return Alpha()") {
			t.Errorf("Beta should call Alpha (post-first-rename); got:\n%s", body)
		}
	})

	// auto_verify=true exercises affectedPackageDirs and the post-refactor
	// verification pipeline. VerificationStatus must not be empty or "unverified"
	// — that would mean the auto-verify code path was skipped entirely.
	t.Run("godoc links updated in same-package and cross-package files", func(t *testing.T) {
		dir := writeMod(t, "linktest", map[string]string{
			// core/core.go — defines OldFunc; has same-package godoc links.
			"core/core.go": "package core\n\n" +
				"// OldFunc does something. See also [OldFunc] for details.\n" +
				"func OldFunc() {}\n\n" +
				"// Helper calls [OldFunc] internally.\n" +
				"func Helper() { OldFunc() }\n",
			// ui/ui.go — cross-package consumer with a [core.OldFunc] link.
			"ui/ui.go": "package ui\n\n" +
				`import "linktest/core"` + "\n\n" +
				"// Render calls [core.OldFunc] to get data.\n" +
				"func Render() { core.OldFunc() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Package: "./...",
			Symbol:  "OldFunc",
			NewName: "NewFunc",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed: %+v", out.Results)
		}

		coreFile := readFile(t, filepath.Join(dir, "core", "core.go"))
		uiFile := readFile(t, filepath.Join(dir, "ui", "ui.go"))

		// Code references updated.
		if !strings.Contains(coreFile, "func NewFunc()") {
			t.Errorf("core.go: expected func NewFunc(), got:\n%s", coreFile)
		}
		if !strings.Contains(uiFile, "core.NewFunc()") {
			t.Errorf("ui.go: expected core.NewFunc(), got:\n%s", uiFile)
		}

		// Godoc links updated — same package.
		if strings.Contains(coreFile, "[OldFunc]") {
			t.Errorf("core.go: stale [OldFunc] link not updated:\n%s", coreFile)
		}
		if !strings.Contains(coreFile, "[NewFunc]") {
			t.Errorf("core.go: expected [NewFunc] link after rename:\n%s", coreFile)
		}

		// Godoc links updated — cross-package.
		if strings.Contains(uiFile, "[core.OldFunc]") {
			t.Errorf("ui.go: stale [core.OldFunc] link not updated:\n%s", uiFile)
		}
		if !strings.Contains(uiFile, "[core.NewFunc]") {
			t.Errorf("ui.go: expected [core.NewFunc] link after rename:\n%s", uiFile)
		}
	})

	t.Run("auto_verify populates VerificationStatus after success", func(t *testing.T) {
		dir := writeMod(t, "autoverify", map[string]string{
			"a.go": "package autoverify\n\nfunc Hello() string { return \"hi\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:     "Hello",
			NewName:    "Greet",
			AutoVerify: true,
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		if out.VerificationStatus == "" {
			t.Error("expected VerificationStatus to be set when AutoVerify=true")
		}
		if out.VerificationStatus == "unverified" {
			t.Errorf("auto_verify ran but VerificationStatus=%q — verify pipeline did not fire", out.VerificationStatus)
		}
	})
}
