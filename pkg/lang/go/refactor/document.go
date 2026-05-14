// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// DocumentAction sets or replaces doc comments for many Go symbols in a single
// transaction. Each (symbol, doc) pair is resolved via AST lookup, the supplied
// prose is validated against godoc conventions, then formatted (line-wrapped
// and `// `-prefixed) before being staged through the workspace transaction's
// build gate.
//
// Supported symbol forms are governed by [docFindTarget]:
//
//   - `Foo` — top-level func, type, var, or const.
//   - `Type.Method` — method on a struct type.
//   - `Type.Field` — field on a struct type; the comment lands above the field,
//     not above the type.
//   - A single value inside a multi-spec const/var block — the comment lands
//     above that spec, not above the GenDecl.
//
// Validation runs BEFORE any edit stages:
//
//   - strict_prefix (when set): the first prose word must equal the symbol's
//     local name (`Foo` for `pkg.Foo`, `Method` for `Type.Method`), matching
//     the godoc convention.
//   - Backtick-quoted identifiers in function/method docs (e.g. `x` or `ctx`)
//     must match a parameter or named return — catches typos that would
//     otherwise produce misleading docs.
//   - Godoc links of the form bracket-Symbol-bracket are resolved against the
//     workspace's loaded packages — broken links fail validation rather than
//     being silently emitted.
//
// Mode controls existing-comment behavior: `replace` (default) overwrites the
// prior comment; `skip_existing` leaves it alone and bumps a counter.
// ListMissing flips the action into read-only mode that reports exported
// symbols without doc comments instead of writing anything.
//
// Deprecated paragraphs are preserved automatically by [docPreserveDeprecated]:
// if the existing doc carries a `Deprecated:` line and the new doc doesn't have
// its own, the old paragraph is appended. Silently dropping a Deprecated tag
// would break godoc's deprecation warnings for callers, which is almost always
// a bug.
//
// The transaction is atomic across files when input.DocumentFiles is used: a
// single build gate covers the entire multi-file batch.
type DocumentAction struct{}

// Name implements [RefactorStrategy] and returns [ActionDocument].
func (*DocumentAction) Name() string { return ActionDocument }

func init() { RegisterAction(&DocumentAction{}) }

const (
	docModeReplace      = "replace"
	docModeSkipExisting = "skip_existing"
	docDefaultLineLen   = 80
)

// Execute is the [RefactorStrategy] entry point. It normalises the input file
// list, resolves the comment-handling mode and wrap budget, lazily loads
// packages for link/param validation, and processes each file via
// [docProcessFile].
//
// Failure modes:
//
//   - Both single-file (File+Comments) and multi-file (DocumentFiles) shapes
//     populated — error (caller should pick one).
//   - Neither shape populated — error.
//   - Unknown Mode value — error listing valid options.
//   - A symbol is not found in its file — error listing the missing symbol(s).
//   - Validation fails (prefix, backtick, or link check) — error listing every
//     failing entry.
//   - The build gate fails after edits — full rollback.
//
// When input.ListMissing is true, no edits are staged at all — the action
// records [StatusSkipped] FileResults whose Message lists the exported symbols
// without doc comments, suitable as a quality-gate input for follow-up
// generation. Pure-list-missing calls bypass the build gate entirely (no edits
// to verify).
func (*DocumentAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	files, err := docNormaliseFiles(input)
	if err != nil {
		return err
	}
	mode := input.Mode
	switch mode {
	case "", docModeReplace:
		mode = docModeReplace
	case docModeSkipExisting:
	default:
		return fmt.Errorf("unknown mode %q (expected 'replace' or 'skip_existing')", input.Mode)
	}

	maxLen := input.MaxLineLength
	if maxLen == 0 {
		maxLen = docDefaultLineLen
	}
	if input.NoWrap {
		maxLen = 0
	}

	// Lazy package load shared across files, used by link validation,
	// param-name validation, and list_missing.
	var pkgsCache []*packages.Package
	pkgs := func() ([]*packages.Package, error) {
		if pkgsCache != nil {
			return pkgsCache, nil
		}
		loaded, lerr := ws.LoadPackages(ctx)
		if lerr != nil {
			return nil, lerr
		}
		pkgsCache = loaded
		return pkgsCache, nil
	}

	if input.ListMissing {
		return docHandleListMissing(ws, files)
	}

	for _, fd := range files {
		if err := docProcessFile(ws, fd, mode, maxLen, input.StrictPrefix, pkgs); err != nil {
			return err
		}
	}
	return nil
}

// docNormaliseFiles collapses the dual single-file (File+Comments) /
// multi-file (DocumentFiles) input shapes into one slice. Returns an
// error when both shapes are populated or when neither is.
func docNormaliseFiles(input Input) ([]DocumentFile, error) {
	if len(input.DocumentFiles) > 0 && (input.File != "" || len(input.Comments) > 0) {
		return nil, fmt.Errorf("provide either file+comments OR files, not both")
	}
	if len(input.DocumentFiles) > 0 {
		for i, fd := range input.DocumentFiles {
			if fd.File == "" {
				return nil, fmt.Errorf("files[%d] missing file path", i)
			}
			if !input.ListMissing && len(fd.Comments) == 0 {
				return nil, fmt.Errorf("files[%d] (%s) has no comments", i, fd.File)
			}
		}
		return input.DocumentFiles, nil
	}
	if input.File == "" {
		return nil, fmt.Errorf("file is required (or pass files for multi-file batch)")
	}
	if !input.ListMissing && len(input.Comments) == 0 {
		return nil, fmt.Errorf("at least one comments entry is required")
	}
	return []DocumentFile{{File: input.File, Comments: input.Comments}}, nil
}

// docProcessFile validates and stages the edits for a single file.
func docProcessFile(
	ws Transaction,
	fd DocumentFile,
	mode string,
	maxLen int,
	strictPrefix bool,
	pkgs func() ([]*packages.Package, error),
) error {
	filePath := fd.File
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(ws.ModRoot(), filePath)
	}

	original, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, original, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", filePath, err)
	}

	type plan struct {
		start   int
		end     int
		payload []byte
	}
	var edits []plan
	var notFound, validationErrs []string
	skipped := 0

	for _, c := range fd.Comments {
		target := docFindTarget(file, c.Symbol)
		if target == nil {
			notFound = append(notFound, c.Symbol)
			continue
		}
		if target.doc != nil && mode == docModeSkipExisting {
			skipped++
			continue
		}

		if strictPrefix {
			if perr := docValidatePrefix(c.Symbol, c.Doc); perr != nil {
				validationErrs = append(validationErrs, fmt.Sprintf("%s: %v", c.Symbol, perr))
				continue
			}
		}
		if errs := docValidateParamRefs(target, c.Doc); len(errs) > 0 {
			for _, e := range errs {
				validationErrs = append(validationErrs, fmt.Sprintf("%s: %s", c.Symbol, e))
			}
			continue
		}
		if links := docFindLinks(c.Doc); len(links) > 0 {
			loaded, lerr := pkgs()
			if lerr != nil {
				validationErrs = append(
					validationErrs,
					fmt.Sprintf("%s: load packages for link validation: %v", c.Symbol, lerr),
				)
				continue
			}
			if errs := docValidateLinks(filePath, links, loaded); len(errs) > 0 {
				for _, e := range errs {
					validationErrs = append(validationErrs, fmt.Sprintf("%s: %s", c.Symbol, e))
				}
				continue
			}
		}

		declStart := fset.Position(target.pos).Offset
		startOff := docLineStart(original, declStart)
		endOff := startOff
		if target.doc != nil {
			docStart := fset.Position(target.doc.Pos()).Offset
			startOff = docLineStart(original, docStart)
		}
		indent := docIndentBefore(original, declStart)
		newDoc := docPreserveDeprecated(target.doc, original, fset, c.Doc)
		formatted := docFormatComment(newDoc, maxLen-len(indent)) + "\n"
		formatted = docPrefixLines(formatted, indent)
		edits = append(edits, plan{
			start:   startOff,
			end:     endOff,
			payload: []byte(formatted),
		})
	}

	if len(notFound) > 0 {
		return fmt.Errorf("symbols not found in %s: %s", filePath, strings.Join(notFound, ", "))
	}
	if len(validationErrs) > 0 {
		return fmt.Errorf("doc validation failed in %s:\n  %s", filePath, strings.Join(validationErrs, "\n  "))
	}
	if len(edits) == 0 {
		return nil
	}

	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })

	modified := make([]byte, len(original))
	copy(modified, original)
	for _, e := range edits {
		modified = ReplaceBytes(modified, e.start, e.end, e.payload)
	}

	msg := fmt.Sprintf("set doc comments on %d symbol(s)", len(edits))
	if skipped > 0 {
		msg += fmt.Sprintf(" (skipped %d already-documented)", skipped)
	}
	return ws.AddChange(filePath, original, modified, msg)
}

// docTarget describes where a doc comment should be placed. Exactly one
// of decl, field, or spec is non-nil; doc is the existing comment group
// (nil if none); pos is the position the new doc should precede.
type docTarget struct {
	decl  ast.Decl   // top-level declaration (FuncDecl or GenDecl)
	field *ast.Field // struct field
	spec  ast.Spec   // ValueSpec/TypeSpec inside a multi-spec GenDecl
	doc   *ast.CommentGroup
	pos   token.Pos
}

// docFindTarget locates the AST node where a symbol's doc comment lives.
// Supported symbol forms:
//
//   - Foo                top-level func, type, var, or const
//   - Type.Method        method on a struct type
//   - Type.Field         field on a struct type (placed above the field)
//   - Active             single value/const inside a multi-spec block
//     (placed above the spec, not the GenDecl)
func docFindTarget(file *ast.File, symbol string) *docTarget {
	parts := strings.SplitN(symbol, ".", 2)
	qualifier := ""
	name := parts[0]
	if len(parts) == 2 {
		qualifier = parts[0]
		name = parts[1]
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if qualifier != "" {
				if d.Recv == nil || msReceiverTypeName(d) != qualifier {
					continue
				}
				if d.Name.Name == name {
					return &docTarget{decl: d, doc: d.Doc, pos: d.Pos()}
				}
				continue
			}
			if d.Recv != nil {
				continue
			}
			if d.Name.Name == name {
				return &docTarget{decl: d, doc: d.Doc, pos: d.Pos()}
			}
		case *ast.GenDecl:
			if qualifier != "" {
				// Looking for Type.Field on a struct type.
				if t := docFindFieldOnStruct(d, qualifier, name); t != nil {
					return t
				}
				continue
			}
			multiSpec := len(d.Specs) > 1
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.Name == name {
						if multiSpec {
							return &docTarget{spec: s, doc: s.Doc, pos: s.Pos()}
						}
						return &docTarget{decl: d, doc: d.Doc, pos: d.Pos()}
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name == name {
							if multiSpec {
								return &docTarget{spec: s, doc: s.Doc, pos: s.Pos()}
							}
							return &docTarget{decl: d, doc: d.Doc, pos: d.Pos()}
						}
					}
				}
			}
		}
	}
	return nil
}

// docFindFieldOnStruct locates a field by name on a named struct type.
// Returns nil if d is not a struct decl, or no matching field exists.
func docFindFieldOnStruct(d *ast.GenDecl, typeName, fieldName string) *docTarget {
	for _, spec := range d.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok || ts.Name.Name != typeName {
			continue
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return nil
		}
		for _, field := range st.Fields.List {
			if len(field.Names) == 0 {
				// Embedded field: name matches the type's identifier.
				if name := docEmbeddedFieldName(field.Type); name == fieldName {
					return &docTarget{field: field, doc: field.Doc, pos: field.Pos()}
				}
				continue
			}
			for _, n := range field.Names {
				if n.Name == fieldName {
					return &docTarget{field: field, doc: field.Doc, pos: field.Pos()}
				}
			}
		}
	}
	return nil
}

// docEmbeddedFieldName extracts the implicit field name of an embedded
// struct field (e.g., `*pkg.Foo` embeds with name `Foo`).
func docEmbeddedFieldName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return docEmbeddedFieldName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// docIndentBefore returns the leading whitespace (tabs/spaces) on the
// line that contains offset. Used to align per-field and per-spec doc
// comments with the declaration they describe.
func docIndentBefore(src []byte, offset int) string {
	lineStart := docLineStart(src, offset)
	end := lineStart
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[lineStart:end])
}

// docPrefixLines prepends prefix to every line of s except trailing
// empty lines (which would otherwise emit pointless trailing whitespace).
func docPrefixLines(s, prefix string) string {
	if prefix == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// docLineStart walks backward from offset to the start of the containing
// line so that doc-comment edits replace whole lines, preserving
// surrounding indentation and column alignment.
func docLineStart(src []byte, offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > len(src) {
		offset = len(src)
	}
	for offset > 0 && src[offset-1] != '\n' {
		offset--
	}
	return offset
}

// docFormatComment normalises text into godoc style. Each prose line is
// wrapped at maxLineLen (when > 0) and prefixed with `// `. Blank lines
// become bare `//`. Lines already prefixed with `//` are kept verbatim.
// Indented lines (godoc preformatted blocks) are kept verbatim too.
func docFormatComment(text string, maxLineLen int) string {
	text = strings.TrimRight(text, "\n\r \t")
	if text == "" {
		return ""
	}

	var out []string
	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimRight(raw, " \t")
		switch {
		case strings.HasPrefix(line, "//"):
			out = append(out, line)
		case line == "":
			out = append(out, "//")
		case startsWithWhitespace(line):
			// Preformatted block: keep indentation, wrap with `// ` prefix.
			out = append(out, "//"+line)
		default:
			wrapped := docWrapProse(line, maxLineLen)
			out = append(out, wrapped...)
		}
	}
	return strings.Join(out, "\n")
}

// docWrapProse wraps a single prose line at word boundaries so that no
// emitted `// `-prefixed line exceeds maxLineLen columns. When maxLineLen
// is 0, the input is returned as one `// `-prefixed line. Words longer
// than the budget are emitted on their own line (longer than max, but
// unbreakable).
func docWrapProse(line string, maxLineLen int) []string {
	if maxLineLen <= 0 {
		return []string{"// " + line}
	}
	const prefix = "// "
	budget := maxLineLen - len(prefix)
	if budget <= 0 {
		return []string{prefix + line}
	}

	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{prefix}
	}

	var lines []string
	var cur []string
	curLen := 0
	for _, w := range words {
		add := len(w)
		if curLen > 0 {
			add++ // separating space
		}
		if curLen > 0 && curLen+add > budget {
			lines = append(lines, prefix+strings.Join(cur, " "))
			cur = nil
			curLen = 0
			add = len(w)
		}
		cur = append(cur, w)
		curLen += add
	}
	if len(cur) > 0 {
		lines = append(lines, prefix+strings.Join(cur, " "))
	}
	return lines
}

func startsWithWhitespace(s string) bool {
	if s == "" {
		return false
	}
	return s[0] == ' ' || s[0] == '\t'
}

// docValidatePrefix checks that the first prose word of the doc matches
// the local symbol name. For "Type.Method", godoc convention is to
// start with the bare method name.
func docValidatePrefix(symbol, doc string) error {
	expected := symbol
	if i := strings.LastIndex(symbol, "."); i >= 0 {
		expected = symbol[i+1:]
	}
	first := docFirstProseWord(doc)
	if first == "" {
		return fmt.Errorf("doc is empty")
	}
	if first != expected {
		return fmt.Errorf("doc must start with %q (got %q)", expected, first)
	}
	return nil
}

// docFirstProseWord returns the first whitespace-delimited word of the
// doc text after stripping leading `//` markers.
func docFirstProseWord(doc string) string {
	for line := range strings.SplitSeq(doc, "\n") {
		s := strings.TrimSpace(line)
		s = strings.TrimPrefix(s, "//")
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) == 0 {
			continue
		}
		return fields[0]
	}
	return ""
}

// docLinkPattern matches godoc links: [Symbol], [Type.Method],
// [pkg.Symbol], [pkg.Type.Method]. Conservatively requires the head to
// start with a letter or underscore.
var docLinkPattern = regexp.MustCompile(`\[([A-Za-z_][\w]*(?:\.[A-Za-z_][\w]*){0,2})\]`)

// docFindLinks extracts the inner identifiers from godoc-style links in
// the doc text. Returns the list of unique link targets (preserving
// first-seen order).
func docFindLinks(doc string) []string {
	matches := docLinkPattern.FindAllStringSubmatch(doc, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		s := m[1]
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// docValidateLinks resolves each link target against the loaded packages.
// Returns one error string per unresolved link.
func docValidateLinks(filePath string, links []string, pkgs []*packages.Package) []string {
	// Find the package containing filePath so package-local links resolve
	// against its scope.
	var localPkg *packages.Package
	for _, p := range pkgs {
		for i := range p.Syntax {
			if i >= len(p.CompiledGoFiles) {
				continue
			}
			if filepath.Clean(p.CompiledGoFiles[i]) == filepath.Clean(filePath) {
				localPkg = p
				break
			}
		}
		if localPkg != nil {
			break
		}
	}

	var errs []string
	for _, link := range links {
		if !docResolveLink(link, localPkg, pkgs) {
			errs = append(errs, fmt.Sprintf("godoc link [%s] does not resolve", link))
		}
	}
	return errs
}

// docResolveLink reports whether a link target resolves to a real
// declaration. Supports:
//
//   - Symbol           — identifier in localPkg's scope (or a method on
//     a type if the form is Type.Method)
//   - pkg.Symbol       — Symbol in some loaded package whose path or
//     last segment matches "pkg"
//   - pkg.Type.Method  — Method on Type in package "pkg"
func docResolveLink(link string, localPkg *packages.Package, pkgs []*packages.Package) bool {
	parts := strings.Split(link, ".")

	switch len(parts) {
	case 1:
		// Plain Symbol — must be in localPkg.
		return localPkg != nil && docPkgHasSymbol(localPkg, parts[0])
	case 2:
		// Either Type.Method in localPkg OR pkg.Symbol.
		if localPkg != nil && docPkgHasMethod(localPkg, parts[0], parts[1]) {
			return true
		}
		if p := docFindPkg(pkgs, parts[0]); p != nil {
			return docPkgHasSymbol(p, parts[1])
		}
		return false
	case 3:
		// pkg.Type.Method.
		if p := docFindPkg(pkgs, parts[0]); p != nil {
			return docPkgHasMethod(p, parts[1], parts[2])
		}
		return false
	}
	return false
}

// docPkgHasSymbol reports whether pkg's scope declares an identifier name.
func docPkgHasSymbol(pkg *packages.Package, name string) bool {
	if pkg == nil || pkg.Types == nil || pkg.Types.Scope() == nil {
		return false
	}
	return pkg.Types.Scope().Lookup(name) != nil
}

// docPkgHasMethod reports whether typeName is a named type in pkg whose
// method set contains methodName (pointer or value receiver).
func docPkgHasMethod(pkg *packages.Package, typeName, methodName string) bool {
	if pkg == nil || pkg.Types == nil || pkg.Types.Scope() == nil {
		return false
	}
	obj := pkg.Types.Scope().Lookup(typeName)
	if obj == nil {
		return false
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return false
	}
	for method := range named.Methods() {
		if method.Name() == methodName {
			return true
		}
	}
	// Also check pointer-receiver methods on the named type.
	ptr := types.NewPointer(named)
	mset := types.NewMethodSet(ptr)
	for method := range mset.Methods() {
		if method.Obj().Name() == methodName {
			return true
		}
	}
	return false
}

// docFindPkg matches a short package alias (the last segment of the
// import path, or the package name) against a loaded package.
func docFindPkg(pkgs []*packages.Package, alias string) *packages.Package {
	for _, p := range pkgs {
		if p.Name == alias {
			return p
		}
		// Match the trailing path segment, mirroring how godoc resolves
		// `[pkg.Symbol]` against an importer's imports list.
		if last := pathLastSegment(p.PkgPath); last == alias {
			return p
		}
	}
	return nil
}

func pathLastSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// docBacktickPattern captures content between matched backticks. We only
// care about identifier-shaped contents to avoid false positives from
// arbitrary inline code spans (`if x != nil`, etc.).
var docBacktickPattern = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`")

// docValidateParamRefs scans the doc text for backtick-quoted identifiers
// (e.g. `s` or `count`) and verifies each one matches a parameter or
// named return value of the target's function signature. No-op for non-
// function targets — backtick spans there can refer to anything.
func docValidateParamRefs(target *docTarget, doc string) []string {
	fn := docFuncDeclOf(target)
	if fn == nil {
		return nil
	}
	allowed := docFuncIdentifiers(fn)
	if len(allowed) == 0 {
		return nil
	}
	matches := docBacktickPattern.FindAllStringSubmatch(doc, -1)
	if len(matches) == 0 {
		return nil
	}
	var errs []string
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		ident := m[1]
		if seen[ident] || allowed[ident] {
			continue
		}
		seen[ident] = true
		errs = append(errs, fmt.Sprintf("backtick `%s` does not match any parameter or named return", ident))
	}
	return errs
}

// docFuncDeclOf returns the *ast.FuncDecl behind a target if the target
// describes a function or method (not a type, field, var, or const).
func docFuncDeclOf(target *docTarget) *ast.FuncDecl {
	if target == nil {
		return nil
	}
	if fn, ok := target.decl.(*ast.FuncDecl); ok {
		return fn
	}
	return nil
}

// docFuncIdentifiers returns the set of identifier names in scope for
// backtick references — receiver name, parameter names, and named
// return values.
func docFuncIdentifiers(fn *ast.FuncDecl) map[string]bool {
	out := make(map[string]bool)
	if fn == nil {
		return out
	}
	if fn.Recv != nil {
		for _, f := range fn.Recv.List {
			for _, n := range f.Names {
				if n.Name != "" && n.Name != "_" {
					out[n.Name] = true
				}
			}
		}
	}
	if fn.Type != nil && fn.Type.Params != nil {
		for _, f := range fn.Type.Params.List {
			for _, n := range f.Names {
				if n.Name != "" && n.Name != "_" {
					out[n.Name] = true
				}
			}
		}
	}
	if fn.Type != nil && fn.Type.Results != nil {
		for _, f := range fn.Type.Results.List {
			for _, n := range f.Names {
				if n.Name != "" && n.Name != "_" {
					out[n.Name] = true
				}
			}
		}
	}
	return out
}

// docPreserveDeprecated keeps any "Deprecated:" paragraph from the
// existing doc when the new doc text doesn't carry its own Deprecated:
// note. godoc treats this paragraph specially (showing tooling warnings
// for callers), and silently dropping it is almost always a bug.
func docPreserveDeprecated(existing *ast.CommentGroup, src []byte, fset *token.FileSet, newDoc string) string {
	if existing == nil {
		return newDoc
	}
	if docContainsDeprecatedMarker(newDoc) {
		return newDoc
	}
	deprecated := docExtractDeprecatedParagraph(existing, src, fset)
	if deprecated == "" {
		return newDoc
	}
	trimmed := strings.TrimRight(newDoc, "\n\r \t")
	if trimmed == "" {
		return deprecated
	}
	return trimmed + "\n\n" + deprecated
}

// docContainsDeprecatedMarker reports whether the doc text already carries
// a "Deprecated:" paragraph (case-sensitive per godoc convention).
func docContainsDeprecatedMarker(doc string) bool {
	for line := range strings.SplitSeq(doc, "\n") {
		s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
		if strings.HasPrefix(s, "Deprecated:") {
			return true
		}
	}
	return false
}

// docExtractDeprecatedParagraph pulls the godoc Deprecated: paragraph
// (and any continuation lines) out of an existing comment group, with
// // prefixes stripped. Returns "" if not present.
func docExtractDeprecatedParagraph(group *ast.CommentGroup, src []byte, fset *token.FileSet) string {
	if group == nil {
		return ""
	}
	// Reconstruct the comment lines without `//` markers, keeping blank
	// lines as empty strings.
	var lines []string
	for _, c := range group.List {
		text := c.Text
		text = strings.TrimPrefix(text, "//")
		text = strings.TrimPrefix(text, " ")
		// Block comments: split on \n.
		lines = append(lines, strings.Split(text, "\n")...)
	}
	// Walk lines looking for the Deprecated: paragraph.
	var collected []string
	inDeprecated := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inDeprecated {
			if strings.HasPrefix(trimmed, "Deprecated:") {
				inDeprecated = true
				collected = append(collected, line)
			}
			continue
		}
		if trimmed == "" {
			break
		}
		collected = append(collected, line)
	}
	_ = src
	_ = fset
	return strings.Join(collected, "\n")
}

// docHandleListMissing scans each input file for exported top-level
// declarations (and exported struct fields) that lack a doc comment,
// and stages a synthetic FileResult on the transaction for each.
func docHandleListMissing(ws Transaction, files []DocumentFile) error {
	for _, fd := range files {
		filePath := fd.File
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(ws.ModRoot(), filePath)
		}
		src, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", filePath, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", filePath, err)
		}
		missing := docFindMissingExports(f)
		ws.AddSkipped(filePath, fmt.Sprintf("missing docs: %s", strings.Join(missing, ", ")))
	}
	return nil
}

// docFindMissingExports returns the names of exported top-level
// declarations (and exported struct fields) without a doc comment.
// Field names are reported as "Type.Field" for clarity.
func docFindMissingExports(file *ast.File) []string {
	var missing []string
	exported := func(n string) bool {
		if n == "" {
			return false
		}
		r := n[0]
		return r >= 'A' && r <= 'Z'
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !exported(d.Name.Name) {
				continue
			}
			label := d.Name.Name
			if d.Recv != nil {
				if recv := msReceiverTypeName(d); recv != "" {
					label = recv + "." + d.Name.Name
				}
			}
			if d.Doc == nil {
				missing = append(missing, label)
			}
		case *ast.GenDecl:
			multiSpec := len(d.Specs) > 1
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !exported(s.Name.Name) {
						continue
					}
					if multiSpec {
						if s.Doc == nil {
							missing = append(missing, s.Name.Name)
						}
					} else if d.Doc == nil {
						missing = append(missing, s.Name.Name)
					}
					if st, ok := s.Type.(*ast.StructType); ok && st.Fields != nil {
						for _, field := range st.Fields.List {
							for _, n := range field.Names {
								if exported(n.Name) && field.Doc == nil {
									missing = append(missing, s.Name.Name+"."+n.Name)
								}
							}
						}
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if !exported(n.Name) {
							continue
						}
						if multiSpec {
							if s.Doc == nil {
								missing = append(missing, n.Name)
							}
						} else if d.Doc == nil {
							missing = append(missing, n.Name)
						}
					}
				}
			}
		}
	}
	return missing
}
