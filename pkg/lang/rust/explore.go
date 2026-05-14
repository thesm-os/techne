// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package rust

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	sitterrust "github.com/smacker/go-tree-sitter/rust"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// Explore is the lang.rust.explore tool. It walks a Rust crate (or any
// directory of .rs files) using the smacker/go-tree-sitter binding and
// returns a structured inventory of the syntactic items declared there
// — functions, structs, enums, traits, impl blocks, consts, statics,
// and type aliases — each annotated with its source location, doc
// comment, signature, and (in code mode) full body.
//
// WHY tree-sitter (not rust-analyzer): parsing is purely lexical, so the
// tool runs in microseconds per file, works on uncompilable code
// (individual file parse errors are swallowed and the file is skipped
// rather than failing the run), and does not require dependencies to be
// fetched or a build to have succeeded. A 500-file crate is typically
// parsed in well under a second. The trade-off is that macro expansions,
// generic instantiations, and trait-method resolution are invisible —
// use [Deps] (when implemented) or rust-analyzer for those.
//
// Mode controls the verbosity and token cost of the response, via the
// shared Mode field on [lang.ExploreInput]:
//
//   - lang.ModeDocs: doc comments only — cheapest, ~50 tokens/symbol.
//   - lang.ModeSkeleton: doc + signature + struct fields / enum variants,
//     no bodies — ~150 tokens/symbol.
//   - lang.ModeCode (default when Symbols is set): doc + signature + the
//     item's full source body, useful when an agent needs to read the
//     implementation without slurping every file end-to-end.
//
// IncludePrivate (default: false) controls whether non-`pub` items are
// included. Visibility is detected by scanning for a `visibility_modifier`
// child node beginning with the literal `pub` — `pub(crate)`,
// `pub(super)`, and `pub(in path)` all count as public for surfacing.
//
// Doc-comment extraction recognises three Rust idioms: `///` outer line
// doc comments (most common), `/** ... */` outer block doc comments,
// and `//!` inner doc comments at the top of a file (folded into the
// PackageDoc field of [lang.ExploreOutput]). Plain `//` and `/* */`
// comments are ignored.
//
// impl blocks are flattened: each method becomes its own symbol keyed
// as "Receiver.Method" with the receiver type recorded on its
// [lang.SymbolMetadata] entry, and the method name is appended to the
// receiver type's Methods list when that type is also surfaced. The
// receiver is the last type identifier before the declaration_list —
// for `impl Greeter for Person` that is Person, not Greeter, which
// matches the behaviour expected by callers used to the Go equivalent.
//
// The walker descends recursively from the input package directory,
// skipping hidden directories, `target/`, and `node_modules/`. Sibling
// crates in a Cargo workspace require separate invocations — there is
// no workspace member discovery.
var Explore = tool.New[lang.ExploreInput, lang.ExploreOutput](
	"lang.rust.explore",
	"Extracts precise Rust AST blocks (fns, structs, traits, impls) using tree-sitter. Use this instead of read_file when analyzing Rust source. Supports docs/skeleton/code verbosity modes for token control.",
	exploreHandler,
	tool.Enum("mode", lang.ModeDocs, lang.ModeCode, lang.ModeSkeleton),
	tool.WithShortDescription("Extract Rust AST symbols (fns, structs, traits, impls) via tree-sitter"),
)

func exploreHandler(ctx context.Context, input lang.ExploreInput) (lang.ExploreOutput, error) {
	dir := input.Package
	if dir == "" {
		dir = "."
	}

	mode := input.Mode
	if mode == "" {
		mode = lang.ModeCode
	}

	files, err := findRustFiles(dir)
	if err != nil {
		return lang.ExploreOutput{}, fmt.Errorf("rust.explore: walk %q: %w", dir, err)
	}
	if len(files) == 0 {
		return lang.ExploreOutput{}, fmt.Errorf("rust.explore: no .rs files found in %q", dir)
	}

	// Build symbol filter set.
	wantSymbol := make(map[string]bool, len(input.Symbols))
	for _, s := range input.Symbols {
		wantSymbol[s] = true
	}

	parser := sitter.NewParser()
	parser.SetLanguage(sitterrust.GetLanguage())

	out := lang.ExploreOutput{
		Package: input.Package,
		Symbols: make(map[string]lang.SymbolMetadata),
	}

	// Collect imports across all files.
	importSet := make(map[string]bool)

	// Track methods per type (for struct/trait Methods field).
	typeMethods := make(map[string][]string)

	var ordered []lang.PendingSymbol

	sortedFiles := make([]string, len(files))
	copy(sortedFiles, files)
	sort.Strings(sortedFiles)

	for _, filePath := range sortedFiles {
		src, readErr := readFileBytes(filePath)
		if readErr != nil {
			continue
		}

		tree, parseErr := parser.ParseCtx(ctx, nil, src)
		if parseErr != nil {
			continue
		}
		root := tree.RootNode()

		// Record file in output.
		out.Files = append(out.Files, filepath.Base(filePath))

		// Collect use declarations for the imports list.
		collectUseDecls(root, src, importSet)

		// Extract package-level doc (//! inner doc comments at the top of the file).
		if out.PackageDoc == "" {
			out.PackageDoc = extractInnerDoc(root, src)
		}

		// Walk top-level children.
		for i := 0; i < int(root.ChildCount()); i++ {
			node := root.Child(i)
			syms := extractNode(node, src, filePath, mode, input.IncludePrivate)
			for _, sym := range syms {
				if len(wantSymbol) > 0 && !wantSymbol[sym.Key] {
					continue
				}
				ordered = append(ordered, sym)
				// Track methods for impl blocks.
				if sym.Meta.Kind == lang.KindMethod && sym.Meta.Receiver != "" {
					typeMethods[sym.Meta.Receiver] = append(
						typeMethods[sym.Meta.Receiver],
						sym.Key[len(sym.Meta.Receiver)+1:],
					)
				}
			}
		}
	}

	// Build final output.
	seen := make(map[string]bool)
	for _, sym := range ordered {
		if seen[sym.Key] {
			continue
		}
		seen[sym.Key] = true
		out.SymbolOrder = append(out.SymbolOrder, sym.Key)
		meta := sym.Meta
		// Attach methods list to structs and traits.
		if meta.Kind == lang.KindStruct || meta.Kind == lang.KindInterface {
			if methods, ok := typeMethods[sym.Key]; ok {
				meta.Methods = methods
			}
		}
		out.Symbols[sym.Key] = meta
	}

	// Deduplicate and sort files.
	fileSet := make(map[string]bool, len(out.Files))
	var dedupFiles []string
	for _, f := range out.Files {
		if !fileSet[f] {
			fileSet[f] = true
			dedupFiles = append(dedupFiles, f)
		}
	}
	sort.Strings(dedupFiles)
	out.Files = dedupFiles

	// Populate imports.
	for imp := range importSet {
		out.Imports = append(out.Imports, imp)
	}
	sort.Strings(out.Imports)

	// Apply token budget.
	if input.MaxOutputTokens > 0 {
		lang.ApplyTokenBudget(&out, input.MaxOutputTokens, "lang.rust.explore")
	}

	return out, nil
}

// extractNode dispatches to the appropriate extractor based on node type.
func extractNode(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	switch node.Type() {
	case "function_item":
		return extractFunction(node, src, filePath, mode, includePrivate)
	case "struct_item":
		return extractStruct(node, src, filePath, mode, includePrivate)
	case "enum_item":
		return extractEnum(node, src, filePath, mode, includePrivate)
	case "trait_item":
		return extractTrait(node, src, filePath, mode, includePrivate)
	case "impl_item":
		return extractImpl(node, src, filePath, mode, includePrivate)
	case "const_item":
		return extractConst(node, src, filePath, mode, includePrivate)
	case "static_item":
		return extractStatic(node, src, filePath, mode, includePrivate)
	case "type_item":
		return extractTypeAlias(node, src, filePath, mode, includePrivate)
	}
	return nil
}

func extractFunction(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	if !includePrivate && !isPublic(src, node) {
		return nil
	}
	name := extractItemName(node, src)
	if name == "" {
		return nil
	}

	location := fmt.Sprintf("%s:%d", filepath.Base(filePath), node.StartPoint().Row+1)
	doc := extractRustDoc(src, node)

	meta := lang.SymbolMetadata{
		Kind:     lang.KindFunc,
		Location: location,
		Docblock: doc,
	}

	if mode == lang.ModeSkeleton || mode == lang.ModeCode {
		meta.Signature = extractFunctionSignature(node, src)
	}

	if mode == lang.ModeCode {
		impl := nodeText(src, node)
		meta.Implementation = impl
	}

	return []lang.PendingSymbol{{Key: name, Meta: meta}}
}

func extractStruct(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	if !includePrivate && !isPublic(src, node) {
		return nil
	}
	name := extractItemName(node, src)
	if name == "" {
		return nil
	}

	location := fmt.Sprintf("%s:%d", filepath.Base(filePath), node.StartPoint().Row+1)
	doc := extractRustDoc(src, node)

	meta := lang.SymbolMetadata{
		Kind:     lang.KindStruct,
		Location: location,
		Docblock: doc,
	}

	if mode == lang.ModeSkeleton || mode == lang.ModeCode {
		meta.Signature = "struct " + name + " {"
		meta.Fields = extractStructFields(node, src, includePrivate)
	}

	if mode == lang.ModeCode {
		impl := nodeText(src, node)
		meta.Implementation = impl
	}

	return []lang.PendingSymbol{{Key: name, Meta: meta}}
}

func extractEnum(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	if !includePrivate && !isPublic(src, node) {
		return nil
	}
	name := extractItemName(node, src)
	if name == "" {
		return nil
	}

	location := fmt.Sprintf("%s:%d", filepath.Base(filePath), node.StartPoint().Row+1)
	doc := extractRustDoc(src, node)

	meta := lang.SymbolMetadata{
		Kind:     lang.KindType,
		Location: location,
		Docblock: doc,
	}

	if mode == lang.ModeSkeleton || mode == lang.ModeCode {
		meta.Signature = "enum " + name + " {"
		// Enumerate variants as fields.
		meta.Fields = extractEnumVariants(node, src)
	}

	if mode == lang.ModeCode {
		impl := nodeText(src, node)
		meta.Implementation = impl
	}

	return []lang.PendingSymbol{{Key: name, Meta: meta}}
}

func extractTrait(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	if !includePrivate && !isPublic(src, node) {
		return nil
	}
	name := extractItemName(node, src)
	if name == "" {
		return nil
	}

	location := fmt.Sprintf("%s:%d", filepath.Base(filePath), node.StartPoint().Row+1)
	doc := extractRustDoc(src, node)

	meta := lang.SymbolMetadata{
		Kind:     lang.KindInterface,
		Location: location,
		Docblock: doc,
	}

	// Extract trait methods as the Methods list.
	var methods []string
	body := namedChildByType(node, "declaration_list")
	if body != nil {
		for i := 0; i < int(body.NamedChildCount()); i++ {
			child := body.NamedChild(i)
			if child.Type() == "function_signature_item" || child.Type() == "function_item" {
				if mname := extractItemName(child, src); mname != "" {
					methods = append(methods, mname)
				}
			}
		}
	}
	meta.Methods = methods

	if mode == lang.ModeSkeleton || mode == lang.ModeCode {
		meta.Signature = "trait " + name + " {"
	}

	if mode == lang.ModeCode {
		impl := nodeText(src, node)
		meta.Implementation = impl
	}

	return []lang.PendingSymbol{{Key: name, Meta: meta}}
}

func extractImpl(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	// Find the type being implemented.
	// impl_item grammar: "impl" [type_parameters] type_identifier ["for" type_identifier] declaration_list
	// We want the receiver type name.
	receiverType := extractImplType(node, src)
	if receiverType == "" {
		return nil
	}

	var results []lang.PendingSymbol

	// Walk declaration_list for methods.
	body := namedChildByType(node, "declaration_list")
	if body == nil {
		return nil
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		if child.Type() != "function_item" {
			continue
		}
		// Check pub visibility for method.
		if !includePrivate && !isPublic(src, child) {
			continue
		}
		mname := extractItemName(child, src)
		if mname == "" {
			continue
		}

		key := receiverType + "." + mname
		location := fmt.Sprintf("%s:%d", filepath.Base(filePath), child.StartPoint().Row+1)
		doc := extractRustDoc(src, child)

		meta := lang.SymbolMetadata{
			Kind:     lang.KindMethod,
			Location: location,
			Docblock: doc,
			Receiver: receiverType,
		}

		if mode == lang.ModeSkeleton || mode == lang.ModeCode {
			meta.Signature = extractFunctionSignature(child, src)
		}

		if mode == lang.ModeCode {
			impl := nodeText(src, child)
			meta.Implementation = impl
		}

		results = append(results, lang.PendingSymbol{Key: key, Meta: meta})
	}

	return results
}

func extractConst(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	if !includePrivate && !isPublic(src, node) {
		return nil
	}
	name := extractItemName(node, src)
	if name == "" {
		return nil
	}

	location := fmt.Sprintf("%s:%d", filepath.Base(filePath), node.StartPoint().Row+1)
	doc := extractRustDoc(src, node)

	meta := lang.SymbolMetadata{
		Kind:     lang.KindConst,
		Location: location,
		Docblock: doc,
	}

	if mode == lang.ModeSkeleton || mode == lang.ModeCode {
		meta.Signature = extractConstSignature(node, src)
	}

	if mode == lang.ModeCode {
		impl := nodeText(src, node)
		meta.Implementation = impl
	}

	return []lang.PendingSymbol{{Key: name, Meta: meta}}
}

func extractStatic(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	if !includePrivate && !isPublic(src, node) {
		return nil
	}
	name := extractItemName(node, src)
	if name == "" {
		return nil
	}

	location := fmt.Sprintf("%s:%d", filepath.Base(filePath), node.StartPoint().Row+1)
	doc := extractRustDoc(src, node)

	meta := lang.SymbolMetadata{
		Kind:     lang.KindVar,
		Location: location,
		Docblock: doc,
	}

	if mode == lang.ModeSkeleton || mode == lang.ModeCode {
		meta.Signature = nodeText(src, node)
		// Trim body if any.
		if idx := strings.Index(meta.Signature, "="); idx != -1 {
			meta.Signature = strings.TrimSpace(meta.Signature[:idx])
		}
	}

	if mode == lang.ModeCode {
		impl := nodeText(src, node)
		meta.Implementation = impl
	}

	return []lang.PendingSymbol{{Key: name, Meta: meta}}
}

func extractTypeAlias(node *sitter.Node, src []byte, filePath, mode string, includePrivate bool) []lang.PendingSymbol {
	if !includePrivate && !isPublic(src, node) {
		return nil
	}
	name := extractItemName(node, src)
	if name == "" {
		return nil
	}

	location := fmt.Sprintf("%s:%d", filepath.Base(filePath), node.StartPoint().Row+1)
	doc := extractRustDoc(src, node)

	meta := lang.SymbolMetadata{
		Kind:     lang.KindType,
		Location: location,
		Docblock: doc,
	}

	if mode == lang.ModeSkeleton || mode == lang.ModeCode {
		meta.Signature = nodeText(src, node)
	}

	if mode == lang.ModeCode {
		impl := nodeText(src, node)
		meta.Implementation = impl
	}

	return []lang.PendingSymbol{{Key: name, Meta: meta}}
}

// extractItemName finds the identifier name child of a declaration node.
func extractItemName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		t := child.Type()
		if t == "identifier" || t == "type_identifier" {
			return nodeText(src, child)
		}
	}
	return ""
}

// extractImplType extracts the receiver type name from an impl_item node.
// For "impl Foo" returns "Foo". For "impl Greeter for Person" returns "Person".
func extractImplType(node *sitter.Node, src []byte) string {
	// The grammar for impl_item:
	// "impl" type_parameters? type ["for" type] declaration_list
	// We want the last type_identifier before declaration_list.
	var last string
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		t := child.Type()
		if t == "declaration_list" {
			break
		}
		if t == "type_identifier" || t == "generic_type" {
			// For generic types, grab the identifier inside.
			if t == "generic_type" {
				inner := childByType(child, "type_identifier")
				if inner != nil {
					last = nodeText(src, inner)
					continue
				}
			}
			last = nodeText(src, child)
		}
	}
	return last
}

// extractFunctionSignature returns the function signature without the body.
func extractFunctionSignature(node *sitter.Node, src []byte) string {
	// Find the block (body) node and take everything before it.
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "block" {
			// Everything before the block.
			end := child.StartByte()
			if end > node.StartByte() && end <= uint32(len(src)) {
				return strings.TrimSpace(string(src[node.StartByte():end]))
			}
		}
	}
	// No body (function signature item) — return full text.
	return strings.TrimSpace(nodeText(src, node))
}

// extractStructFields walks field_declaration_list and returns FieldInfo entries.
func extractStructFields(node *sitter.Node, src []byte, includePrivate bool) []lang.FieldInfo {
	var fields []lang.FieldInfo
	fieldList := namedChildByType(node, "field_declaration_list")
	if fieldList == nil {
		return fields
	}
	for i := 0; i < int(fieldList.NamedChildCount()); i++ {
		child := fieldList.NamedChild(i)
		if child.Type() != "field_declaration" {
			continue
		}
		// Check visibility.
		pub := isPublic(src, child)
		if !includePrivate && !pub {
			continue
		}
		// Name is the identifier child.
		nameNode := childByType(child, "identifier")
		if nameNode == nil {
			continue
		}
		fieldName := nodeText(src, nameNode)

		// Type is everything after ": ".
		typeStr := extractFieldType(child, src)
		doc := extractRustDoc(src, child)

		fields = append(fields, lang.FieldInfo{
			Name: fieldName,
			Type: typeStr,
			Doc:  doc,
		})
	}
	return fields
}

// extractFieldType extracts the type of a field_declaration node.
func extractFieldType(node *sitter.Node, src []byte) string {
	// field_declaration: visibility? identifier ":" type
	// The type is the last named child that isn't identifier or visibility_modifier.
	for i := int(node.NamedChildCount()) - 1; i >= 0; i-- {
		child := node.NamedChild(i)
		t := child.Type()
		if t != "identifier" && t != "visibility_modifier" {
			return nodeText(src, child)
		}
	}
	return ""
}

// extractEnumVariants returns enum variants as FieldInfo (name only).
func extractEnumVariants(node *sitter.Node, src []byte) []lang.FieldInfo {
	var variants []lang.FieldInfo
	body := namedChildByType(node, "enum_variant_list")
	if body == nil {
		return variants
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		if child.Type() != "enum_variant" {
			continue
		}
		nameNode := childByType(child, "identifier")
		if nameNode == nil {
			continue
		}
		variants = append(variants, lang.FieldInfo{
			Name: nodeText(src, nameNode),
		})
	}
	return variants
}

// extractConstSignature builds "const NAME: TYPE" from the node.
func extractConstSignature(node *sitter.Node, src []byte) string {
	full := nodeText(src, node)
	// Take everything up to and including the type, before "=".
	if before, _, ok := strings.Cut(full, "="); ok {
		return strings.TrimSpace(before)
	}
	// Trim trailing semicolon if no value.
	return strings.TrimSuffix(strings.TrimSpace(full), ";")
}

// collectUseDecls gathers use declarations from the AST into the importSet.
func collectUseDecls(root *sitter.Node, src []byte, importSet map[string]bool) {
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		if child.Type() == "use_declaration" {
			text := nodeText(src, child)
			// Strip "use " prefix and trailing ";".
			text = strings.TrimPrefix(text, "use ")
			text = strings.TrimSuffix(text, ";")
			text = strings.TrimSpace(text)
			if text != "" {
				importSet[text] = true
			}
		}
	}
}

// extractInnerDoc collects //! inner doc comments at the top of the file.
func extractInnerDoc(root *sitter.Node, src []byte) string {
	var lines []string
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		if child.Type() != "line_comment" && child.Type() != "block_comment" {
			continue
		}
		text := nodeText(src, child)
		after, ok := strings.CutPrefix(text, "//!")
		if !ok {
			break
		}
		lines = append(lines, strings.TrimSpace(after))
	}
	return strings.Join(lines, "\n")
}

// readFileBytes reads the bytes of a file, returning an error if it fails.
func readFileBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}
