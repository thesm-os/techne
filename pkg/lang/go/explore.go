// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Explore is the lang.go.explore tool. It extracts Go declarations
// (functions, methods, structs, interfaces, types, consts, vars) from a
// package at three configurable verbosity levels, returning structured
// lang.SymbolMetadata keyed by symbol name.
//
// Prefer this over Read whenever the question is "what does this Go
// symbol look like?" Read forces the whole-file cost (~10x more tokens
// for a 30-line function in a 400-line file) and includes unrelated
// declarations that confuse downstream summarization. Explore returns
// just the symbols you ask for and synthesizes their AST view:
// signature, doc comment, fields, receiver, and — only when requested —
// full source.
//
// Three modes balance token cost against detail:
//   - lang.ModeDocs: names + docblocks only (~50 tokens/symbol). Use
//     when an agent only needs to know "is there a Foo? what does its
//     comment say?"
//   - lang.ModeSkeleton: signatures + struct fields (~150
//     tokens/symbol). Default for whole-package discovery.
//   - lang.ModeCode: full source including bodies (~500+
//     tokens/symbol). Default when specific symbols are requested,
//     because the agent already knows what it wants and a second call
//     would waste a turn.
//
// The package field accepts either an import path ("go.thesmos.sh/...")
// or a filesystem path ("./pkg/foo") — the handler discriminates by
// detecting dots in the first path segment. When tests are loaded
// (packages.Load with Tests=true), the same package appears under both
// its regular and test variants; processing is sorted by PkgPath so the
// regular variant is seen first and its declarations win the dedup.
//
// When MaxOutputTokens is set, oversize results are truncated
// implementation-body first via lang.ApplyTokenBudget — signatures and
// doc comments survive so the agent can still navigate. For
// import-heavy packages, requesting specific symbols also prunes the
// Imports list to only those referenced in the extracted code, saving
// an additional ~500 tokens per turn.
var Explore = tool.New[lang.ExploreInput, lang.ExploreOutput](
	"lang.go.explore",
	"PREFER OVER Read for any Go-symbol exploration. Returns just the declarations you ask for (one function, a struct's API surface, etc.) instead of the whole file — saves ~80% of tokens. Three modes balance token cost vs detail: 'docs' (~50 tokens/symbol), 'skeleton' (signatures+fields, ~150 tokens/symbol, default), 'code' (full source, ~500+ tokens/symbol). Read remains right for non-Go files and for end-to-end reading of small files. AGENT HINT: Pass specific symbol names in 'symbols' to extract only what you need.",
	exploreHandler,
	tool.Enum("mode", lang.ModeDocs, lang.ModeCode, lang.ModeSkeleton),
)

// exploreHandler implements the lang.go.explore RPC. Loads the requested
// package(s) via golang.org/x/tools/go/packages, walks every top-level
// declaration, and builds lang.SymbolMetadata entries filtered by the
// input's Symbols / Kind / NamePrefix / NameSuffix selectors.
//
// The handler discriminates between filesystem-path and Go-import-path
// inputs by inspecting the first path segment: a dot before the first
// slash indicates an import path; absolute or ./-relative paths are
// treated as directories with a "." pattern. This lets agents pass
// either shape from earlier tool results without translation.
//
// Loader errors are fatal: any pkg.Errors entry returned by
// packages.Load is propagated as an error so the agent does not
// silently act on a partially-loaded package. Test variants ("foo/bar
// [foo/bar.test]") are processed after their regular counterparts so
// that the seen-map dedup prefers production declarations when a symbol
// is defined in both.
func exploreHandler(_ context.Context, input lang.ExploreInput) (lang.ExploreOutput, error) {
	// Determine whether input.Package is a filesystem path or a Go package pattern.
	// Patterns containing "..." (e.g. "./...") are passed as-is.
	// Absolute/relative directory paths use cfg.Dir + "." pattern.
	pattern := input.Package
	dir := "."

	isGoPattern := strings.Contains(input.Package, "...")
	isImportPath := !isGoPattern && strings.Contains(strings.SplitN(input.Package, "/", 2)[0], ".")

	if !isGoPattern && !isImportPath &&
		(strings.HasPrefix(input.Package, "/") ||
			strings.HasPrefix(input.Package, "./") ||
			strings.HasPrefix(input.Package, "../")) {
		dir = input.Package
		pattern = "."
	}

	cfg := &packages.Config{
		Mode: packages.NeedSyntax | packages.NeedFiles | packages.NeedImports |
			packages.NeedName | packages.NeedCompiledGoFiles,
		Dir:       dir,
		Tests:     true, // Include _test.go files so test symbols are indexed.
		ParseFile: nil,  // default — parses comments via NeedSyntax
		Fset:      token.NewFileSet(),
	}

	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		return lang.ExploreOutput{}, fmt.Errorf("load package %q: %w", input.Package, err)
	}
	if len(pkgs) == 0 {
		return lang.ExploreOutput{}, fmt.Errorf("no packages found for %q", input.Package)
	}

	// Collect any loader errors.
	var loaderErrs []string
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			loaderErrs = append(loaderErrs, e.Error())
		}
	}
	if len(loaderErrs) > 0 {
		return lang.ExploreOutput{}, fmt.Errorf(
			"package load errors for %q: %s",
			input.Package,
			strings.Join(loaderErrs, "; "),
		)
	}

	mode := input.Mode
	if mode == "" {
		if len(input.Symbols) > 0 {
			// Specific symbols requested — the agent already knows what it wants.
			// Default to code so it gets implementations without a second round-trip.
			mode = lang.ModeCode
		} else {
			// Whole-package discovery — the agent is browsing.
			// Default to skeleton to avoid dumping 50+ implementations.
			mode = lang.ModeSkeleton
		}
	}

	// Build a filter set for requested symbols.
	wantSymbol := make(map[string]bool, len(input.Symbols))
	for _, s := range input.Symbols {
		wantSymbol[s] = true
	}

	// Collect file bytes for source extraction.
	fileBytes := make(map[string][]byte)
	readFileSrc := func(filename string) []byte {
		if b, ok := fileBytes[filename]; ok {
			return b
		}
		b, _ := os.ReadFile(filename)
		fileBytes[filename] = b
		return b
	}

	out := lang.ExploreOutput{
		Package:          input.Package,
		WorkspaceVersion: lang.WorkspaceVersion(),
		Symbols:          make(map[string]lang.SymbolMetadata),
	}

	fileSet := make(map[string]bool)
	importSet := make(map[string]bool)
	importAliases := make(map[string]string) // alias → import path (for pruning)

	// Track methods per struct to populate SymbolMetadata.Methods later.
	structMethods := make(map[string][]string) // structName -> []methodName

	// We need two passes: first collect all declarations in order,
	// then resolve struct methods.
	var ordered []lang.PendingSymbol

	// Process packages in sorted order for determinism.
	// When Tests: true, packages.Load returns both the regular package (e.g.
	// "foo/bar") and the test variant (e.g. "foo/bar [foo/bar.test]"). Sort puts
	// the regular package first so the seen-map dedup below prefers non-test
	// declarations when both define the same symbol.
	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].PkgPath < pkgs[j].PkgPath
	})

	for _, pkg := range pkgs {
		fset := pkg.Fset
		pkgImportPath := pkg.PkgPath
		// Strip test variant suffix like " [foo/bar.test]" to get the clean import path.
		if idx := strings.Index(pkgImportPath, " ["); idx != -1 {
			pkgImportPath = pkgImportPath[:idx]
		}

		// Build a sorted list of syntax files paired with their paths.
		// pkg.CompiledGoFiles gives file paths in order; pkg.Syntax has the ASTs.
		// They correspond 1:1.
		type fileEntry struct {
			path string
			file *ast.File
		}
		var entries []fileEntry
		for i, f := range pkg.Syntax {
			path := ""
			if i < len(pkg.CompiledGoFiles) {
				path = pkg.CompiledGoFiles[i]
			} else if fset != nil {
				pos := fset.Position(f.Pos())
				path = pos.Filename
			}
			entries = append(entries, fileEntry{path: path, file: f})
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].path < entries[j].path
		})

		// Collect all imports — will be pruned later if specific symbols are requested.
		for importPath := range pkg.Imports {
			importSet[importPath] = true
		}

		// Build per-file import alias map for import pruning.
		for _, entry := range entries {
			for _, imp := range entry.file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				alias := filepath.Base(path) // default alias is last path segment
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				importAliases[alias] = path
			}
		}

		for _, entry := range entries {
			fname := entry.path
			file := entry.file

			fileSet[filepath.Base(fname)] = true

			if out.PackageDoc == "" && file.Doc != nil {
				out.PackageDoc = strings.TrimSpace(file.Doc.Text())
			}

			src := readFileSrc(fname)

			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					symbols := extractFunc(fset, d, src, mode, input.IncludePrivate, pkgImportPath)
					for _, sym := range symbols {
						name := sym.Key
						if len(wantSymbol) > 0 && !wantSymbol[name] {
							continue
						}
						ordered = append(ordered, sym)
						// Track method on receiver struct.
						if sym.Meta.Kind == lang.KindMethod && sym.Meta.Receiver != "" {
							recvBase := receiverBase(sym.Meta.Receiver)
							structMethods[recvBase] = append(structMethods[recvBase], d.Name.Name)
						}
					}

				case *ast.GenDecl:
					symbols := extractGenDecl(fset, d, src, mode, input.IncludePrivate)
					for _, sym := range symbols {
						name := sym.Key
						if len(wantSymbol) > 0 && !wantSymbol[name] {
							continue
						}
						ordered = append(ordered, sym)
					}
				}
			}
		}
	}

	// Build final output preserving declaration order. Apply kind/prefix/
	// suffix filters here so the inventory use case ("list all func
	// symbols starting with 'Test' in this package") doesn't need a
	// second-pass scan.
	keep := func(name string, meta lang.SymbolMetadata) bool {
		if input.Kind != "" && input.Kind != lang.KindAll && meta.Kind != input.Kind {
			return false
		}
		// For methods stored as "Type.Method", apply the prefix/suffix to
		// the bare method name (after the dot) so "name_prefix=Test"
		// behaves intuitively for receiver-bound test helpers.
		base := name
		if i := strings.LastIndex(base, "."); i >= 0 && i < len(base)-1 {
			base = base[i+1:]
		}
		if input.NamePrefix != "" && !strings.HasPrefix(base, input.NamePrefix) {
			return false
		}
		if input.NameSuffix != "" && !strings.HasSuffix(base, input.NameSuffix) {
			return false
		}
		return true
	}

	seen := make(map[string]bool)
	for _, sym := range ordered {
		if seen[sym.Key] {
			continue
		}
		seen[sym.Key] = true

		// Attach methods list to structs/interfaces.
		meta := sym.Meta
		if meta.Kind == lang.KindStruct || meta.Kind == lang.KindInterface {
			if methods, ok := structMethods[sym.Key]; ok {
				meta.Methods = methods
			}
		}
		if !keep(sym.Key, meta) {
			continue
		}
		out.SymbolOrder = append(out.SymbolOrder, sym.Key)
		out.Symbols[sym.Key] = meta
	}

	// Populate Files.
	for f := range fileSet {
		out.Files = append(out.Files, f)
	}
	sort.Strings(out.Files)

	// Populate Imports — prune to only those referenced by extracted symbols
	// when specific symbols are requested. This saves ~500 tokens per turn
	// in import-heavy packages.
	if len(input.Symbols) > 0 {
		usedImports := pruneImports(out.Symbols, importAliases)
		for imp := range usedImports {
			out.Imports = append(out.Imports, imp)
		}
	} else {
		for imp := range importSet {
			out.Imports = append(out.Imports, imp)
		}
	}
	sort.Strings(out.Imports)

	// Apply minification if requested — runs before token budget so the budget
	// accounts for the reduced size.
	if input.StripComments || input.Minify {
		for _, name := range out.SymbolOrder {
			sym := out.Symbols[name]
			if sym.Implementation != "" {
				if input.StripComments {
					sym.Implementation = stripGoComments(sym.Implementation)
				}
				if input.Minify {
					sym.Implementation = minifyGoSource(sym.Implementation)
				}
				out.Symbols[name] = sym
			}
		}
	}

	// Apply MaxOutputTokens budget.
	if input.MaxOutputTokens > 0 {
		lang.ApplyTokenBudget(&out, input.MaxOutputTokens, "lang.go.explore")
	}

	// When returning skeleton for whole-package discovery, suggest narrowing
	// to specific symbols with code mode. This guides the agent from
	// "what's in this package?" to "show me these specific implementations."
	// Not needed when symbols were specified — those default to code mode.
	if mode == lang.ModeSkeleton && len(input.Symbols) == 0 && len(out.SymbolOrder) > 0 && !out.Truncated {
		topSymbols := out.SymbolOrder
		if len(topSymbols) > 3 {
			topSymbols = topSymbols[:3]
		}
		out.NextActions = append(out.NextActions, lang.NextAction{
			Tool:       "lang.go.explore",
			Reason:     "Package overview returned — select symbols and request code mode for implementations",
			Confidence: lang.ConfidenceHigh,
			Input: lang.ExploreInput{
				Package: input.Package,
				Symbols: topSymbols,
				Mode:    lang.ModeCode,
			},
		})
	}

	return out, nil
}

// extractFunc produces lang.PendingSymbol entries for a single
// ast.FuncDecl. Methods are keyed as "Receiver.Name" to match the
// naming used by gopls workspace_symbol and the doc scorer, so the
// three symbol sources can deduplicate cleanly downstream.
//
// For test/benchmark/fuzz entry points the function also synthesizes a
// ReproCommand via buildGoTestReproCommand — agents that pull a single
// failing test from explore output can pipe the command directly into
// Bash without computing the package path themselves.
//
// The mode parameter controls how much source text is materialized:
// Mode=Skeleton extracts only the signature (cheap), Mode=Code copies
// the full function body using the FileSet position offsets.
// pkgImportPath is the cleaned (test-variant suffix stripped) import
// path.
func extractFunc(
	fset *token.FileSet,
	fn *ast.FuncDecl,
	src []byte,
	mode string,
	includePrivate bool,
	pkgImportPath string,
) []lang.PendingSymbol {
	name := fn.Name.Name
	if !includePrivate && !ast.IsExported(name) {
		return nil
	}

	pos := fset.Position(fn.Pos())
	location := fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)

	kind := lang.KindFunc
	receiver := ""
	mapKey := name

	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		kind = lang.KindMethod
		receiver = formatExpr(fn.Recv.List[0].Type)
		mapKey = receiverBase(receiver) + "." + name
	}

	meta := lang.SymbolMetadata{
		Kind:     kind,
		Location: location,
		Receiver: receiver,
	}

	if fn.Doc != nil {
		meta.Docblock = strings.TrimSpace(fn.Doc.Text())
	}

	if mode == lang.ModeSkeleton || mode == lang.ModeCode {
		meta.Signature = extractFuncSignature(fset, fn, src)
	}

	if mode == lang.ModeCode && fn.Body != nil {
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		if start >= 0 && end <= len(src) && start < end {
			meta.Implementation = string(src[start:end])
		}
	}

	// Populate ReproCommand for test/benchmark/fuzz functions.
	if pkgImportPath != "" && fn.Type != nil && fn.Type.Params != nil {
		params := fn.Type.Params.List
		if len(params) == 1 {
			paramType := formatExpr(params[0].Type)
			meta.ReproCommand = buildGoTestReproCommand(name, paramType, pkgImportPath)
		}
	}

	return []lang.PendingSymbol{{Key: mapKey, Meta: meta}}
}

// extractGenDecl processes a single ast.GenDecl (type, const, or var
// block) into lang.PendingSymbol entries. One GenDecl can produce many
// symbols ("var a, b, c = ..." yields three); each is keyed by its
// identifier name and inherits the GenDecl's doc comment when its own
// spec has none — godoc convention.
//
// For struct and interface types, the signature returned in skeleton
// mode is trimmed to the first line plus an opening brace so the agent
// sees the type shape without paying for every field body. Code mode
// returns the full GenDecl source so multi-spec declarations stay
// syntactically valid when re-emitted.
//
// Unexported symbols are skipped unless includePrivate is true.
func extractGenDecl(
	fset *token.FileSet,
	d *ast.GenDecl,
	src []byte,
	mode string,
	includePrivate bool,
) []lang.PendingSymbol {
	var results []lang.PendingSymbol

	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			name := s.Name.Name
			if !includePrivate && !ast.IsExported(name) {
				continue
			}

			pos := fset.Position(s.Pos())
			location := fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)

			kind := lang.KindType
			var fields []lang.FieldInfo

			switch t := s.Type.(type) {
			case *ast.StructType:
				kind = lang.KindStruct
				if mode == lang.ModeSkeleton || mode == lang.ModeCode {
					fields = extractFields(t)
				}
			case *ast.InterfaceType:
				kind = lang.KindInterface
			}

			meta := lang.SymbolMetadata{
				Kind:     kind,
				Location: location,
				Fields:   fields,
			}

			// Doc can come from the spec or from the parent GenDecl.
			if s.Doc != nil {
				meta.Docblock = strings.TrimSpace(s.Doc.Text())
			} else if d.Doc != nil {
				meta.Docblock = strings.TrimSpace(d.Doc.Text())
			}

			if mode == lang.ModeSkeleton || mode == lang.ModeCode {
				// Build signature: "type Name <TypeExpr>"
				var buf bytes.Buffer
				buf.WriteString("type ")
				buf.WriteString(name)
				buf.WriteString(" ")
				_ = format.Node(&buf, fset, s.Type)
				sig := buf.String()
				// For structs/interfaces, trim to first line for skeleton.
				if kind == lang.KindStruct || kind == lang.KindInterface {
					lines := strings.SplitN(sig, "\n", 2)
					sig = strings.TrimSpace(lines[0])
					if !strings.HasSuffix(sig, "{") {
						sig += " {"
					}
				}
				meta.Signature = sig
			}

			if mode == lang.ModeCode {
				start := fset.Position(d.Pos()).Offset
				end := fset.Position(d.End()).Offset
				if start >= 0 && end <= len(src) && start < end {
					meta.Implementation = string(src[start:end])
				}
			}

			results = append(results, lang.PendingSymbol{Key: name, Meta: meta})

		case *ast.ValueSpec:
			kind := lang.KindConst
			if d.Tok == token.VAR {
				kind = lang.KindVar
			}

			for _, ident := range s.Names {
				name := ident.Name
				if !includePrivate && !ast.IsExported(name) {
					continue
				}

				pos := fset.Position(ident.Pos())
				location := fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)

				meta := lang.SymbolMetadata{
					Kind:     kind,
					Location: location,
				}

				if s.Doc != nil {
					meta.Docblock = strings.TrimSpace(s.Doc.Text())
				} else if d.Doc != nil {
					meta.Docblock = strings.TrimSpace(d.Doc.Text())
				}

				if mode == lang.ModeSkeleton || mode == lang.ModeCode {
					// Build signature: "const Name Type = Value" or just "const Name"
					var buf bytes.Buffer
					buf.WriteString(kind)
					buf.WriteString(" ")
					buf.WriteString(name)
					if s.Type != nil {
						buf.WriteString(" ")
						_ = format.Node(&buf, fset, s.Type)
					}
					if len(s.Values) > 0 {
						buf.WriteString(" = ")
						_ = format.Node(&buf, fset, s.Values[0])
					}
					meta.Signature = buf.String()
				}

				if mode == lang.ModeCode {
					// For const/var, extract the full GenDecl.
					start := fset.Position(d.Pos()).Offset
					end := fset.Position(d.End()).Offset
					if start >= 0 && end <= len(src) && start < end {
						meta.Implementation = string(src[start:end])
					}
				}

				results = append(results, lang.PendingSymbol{Key: name, Meta: meta})
			}
		}
	}

	return results
}

// extractFields returns a lang.FieldInfo slice describing a struct's
// fields, including embedded anonymous fields (rendered with the type
// name as both Name and Type). Struct tags are surfaced verbatim with
// the surrounding backticks stripped so downstream consumers can parse
// them without unquoting.
//
// This runs unconditionally when extracting structs in skeleton/code
// mode — fields are part of a struct's API surface and dominate its
// utility in code-search contexts.
func extractFields(st *ast.StructType) []lang.FieldInfo {
	var fields []lang.FieldInfo
	if st.Fields == nil {
		return fields
	}
	for _, field := range st.Fields.List {
		typeName := formatExpr(field.Type)
		tag := ""
		if field.Tag != nil {
			// Strip surrounding backticks.
			tag = strings.Trim(field.Tag.Value, "`")
		}
		doc := ""
		if field.Doc != nil {
			doc = strings.TrimSpace(field.Doc.Text())
		}
		if len(field.Names) == 0 {
			// Embedded field.
			fields = append(fields, lang.FieldInfo{
				Name: typeName,
				Type: typeName,
				Tag:  tag,
				Doc:  doc,
			})
		} else {
			for _, ident := range field.Names {
				fields = append(fields, lang.FieldInfo{
					Name: ident.Name,
					Type: typeName,
					Tag:  tag,
					Doc:  doc,
				})
			}
		}
	}
	return fields
}

// extractFuncSignature returns the function's source from the func
// keyword up to (but not including) the opening body brace, trimmed of
// trailing whitespace. For body-less declarations (interface methods,
// assembly stubs) it returns the entire declaration text.
//
// The result is used as lang.SymbolMetadata.Signature when mode is
// skeleton or code. Token offsets come from the FileSet — the caller
// pre-loads file bytes so this is allocation-free per declaration.
func extractFuncSignature(fset *token.FileSet, fn *ast.FuncDecl, src []byte) string {
	start := fset.Position(fn.Pos()).Offset
	var end int
	if fn.Body != nil {
		end = fset.Position(fn.Body.Lbrace).Offset
	} else {
		end = fset.Position(fn.End()).Offset
	}
	if start >= 0 && end <= len(src) && start < end {
		return strings.TrimSpace(string(src[start:end]))
	}
	return ""
}

// formatExpr renders an ast.Expr back to Go source using the standard
// library's go/format printer. Returns the empty string for a nil
// expression; on formatter failure (which should not happen for valid
// input from go/parser) it falls back to the Go-syntax %T representation
// of the node so the result is never the empty string by accident.
//
// Used for receiver types, field types, and other type-expression
// rendering in metadata output. The fresh FileSet is required because
// the printer needs positional information that may be absent on
// synthetic expressions.
func formatExpr(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var buf bytes.Buffer
	fset := token.NewFileSet()
	if err := format.Node(&buf, fset, expr); err != nil {
		return fmt.Sprintf("%T", expr)
	}
	return buf.String()
}

// pruneImports scans the extracted symbols' Implementation, Signature,
// and field-type strings for substrings of the form "alias." and returns
// only those imports whose alias appears at least once. This is a
// textual heuristic — accurate enough for the import-presentation use
// case but not a substitute for type-checked unused-import detection.
//
// Called only when the input requested specific symbols; whole-package
// exploration returns all imports verbatim so the agent sees the
// package's full external surface area. The pruning saves ~500 tokens
// per turn in import-heavy packages where only one or two symbols are
// requested.
func pruneImports(symbols map[string]lang.SymbolMetadata, aliases map[string]string) map[string]bool {
	used := make(map[string]bool)

	for _, sym := range symbols {
		// Scan all text fields where import references can appear
		texts := []string{sym.Implementation, sym.Signature}
		for _, f := range sym.Fields {
			texts = append(texts, f.Type)
		}

		combined := strings.Join(texts, " ")
		for alias, importPath := range aliases {
			// Check for "alias." pattern (e.g. "fmt." in "fmt.Sprintf")
			if strings.Contains(combined, alias+".") {
				used[importPath] = true
			}
		}
	}

	return used
}

// receiverBase strips leading `*` characters from a method receiver
// type expression, mapping "*Person" → "Person". The result is used to
// key methods by their owning type name regardless of pointer-receiver
// vs value-receiver convention — both forms attach to the same struct
// in SymbolMetadata.Methods.
func receiverBase(recv string) string {
	return strings.TrimLeft(recv, "*")
}
