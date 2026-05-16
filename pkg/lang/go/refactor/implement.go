// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bytes"
	"context"
	"fmt"
	"go/types"
	"os"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ImplementInterfaceAction generates method stubs on a target struct so it
// satisfies a named interface. Only the methods MISSING from the struct's
// current method set are emitted — already-implemented methods (value- or
// pointer-receiver) are left alone, so calling the action twice is idempotent.
//
// Interface resolution covers three cases via [implFindInterface]:
//
//  1. Local interfaces in any loaded package.
//  2. Cross-package interfaces in the workspace's transitive imports (BFS
//     walked from the loaded set).
//  3. Stdlib / third-party interfaces not transitively imported by user code —
//     loaded on demand from the package path hint (e.g. `io.Reader` is
//     recognised even if the target package doesn't import io).
//
// Stub bodies default to `panic("not implemented")`; the caller can override
// via input.StubBody (e.g. `return nil, nil` for handlers that should compile
// silently). The receiver is `<lowercase first letter> *<TargetStruct>`,
// matching idiomatic Go method-receiver naming.
//
// When the struct already satisfies the interface, the action records a
// [StatusSkipped] FileResult and returns successfully — no file is touched.
// This lets agents call the action speculatively without worrying about wasted
// edits.
type ImplementInterfaceAction struct{}

// Name implements [RefactorStrategy] and returns [ActionImplementInterface].
func (*ImplementInterfaceAction) Name() string { return ActionImplementInterface }

func init() { RegisterAction(&ImplementInterfaceAction{}) }

// Execute is the [RefactorStrategy] entry point. It locates the struct,
// resolves the interface, diffs the method sets, and appends stubs for any
// missing methods to the struct's defining file.
//
// Failure modes:
//
//   - input.TargetStruct or input.Interface is empty — early error.
//   - The struct is not found — error.
//   - The interface is not found (after local, BFS, and on-demand load) —
//     error.
//   - The struct is not a named type — error.
//   - The struct's source file cannot be located — error.
//   - The build gate fails after stub insertion (e.g., the body references
//     symbols that aren't in scope) — rollback.
//
// The success-with-no-work path records a [StatusSkipped] result and returns
// nil, so callers can rely on a non-nil return only when something went wrong.
func (*ImplementInterfaceAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.TargetStruct == "" || input.Interface == "" {
		return fmt.Errorf("target_struct and interface are required")
	}

	stubBody := input.StubBody
	if stubBody == "" {
		stubBody = `panic("not implemented")`
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	// 1. Locate the struct type object.
	structObj := FindSymbolObject(pkgs, input.TargetStruct, input.Package, "", 0)
	if structObj == nil {
		return fmt.Errorf("struct %q not found", input.TargetStruct)
	}

	// 2. Resolve the interface type — supports "pkg.Iface" and "Iface" forms.
	ifaceName := input.Interface
	ifacePkgHint := ""
	if idx := strings.LastIndex(ifaceName, "."); idx != -1 {
		ifacePkgHint = ifaceName[:idx]
		ifaceName = ifaceName[idx+1:]
	}

	ifaceType := implFindInterface(ctx, pkgs, ws.ModRoot(), ifaceName, ifacePkgHint)
	if ifaceType == nil {
		return fmt.Errorf("interface %q not found", input.Interface)
	}

	structNamed, ok := structObj.Type().(*types.Named)
	if !ok {
		return fmt.Errorf("%q is not a named type", input.TargetStruct)
	}

	// 3. Diff method sets: collect methods already provided (direct + via pointer).
	existing := make(map[string]bool)
	for method := range structNamed.Methods() {
		existing[method.Name()] = true
	}
	ptrMS := types.NewMethodSet(types.NewPointer(structNamed))
	for method := range ptrMS.Methods() {
		existing[method.Obj().Name()] = true
	}

	var missing []*types.Func
	for m := range ifaceType.Methods() {
		if !existing[m.Name()] {
			missing = append(missing, m)
		}
	}

	// 4. Find the source file where the struct is defined.
	structFilePath := ""
	var structPkg *packages.Package
	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		pos := pkg.Fset.Position(structObj.Pos())
		if pos.Filename != "" {
			structFilePath = pos.Filename
			structPkg = pkg
			break
		}
	}

	if structFilePath == "" {
		return fmt.Errorf("cannot locate source file for %q", input.TargetStruct)
	}

	// 5. If nothing is missing, record a skipped result and return cleanly.
	if len(missing) == 0 {
		ws.AddSkipped(
			structFilePath,
			fmt.Sprintf("%s already fully implements %s", input.TargetStruct, input.Interface),
		)
		return nil
	}

	// 6. Read source, generate stubs, append.
	original, err := os.ReadFile(structFilePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	qual := implQualifierForPkg(structPkg)
	receiverName := strings.ToLower(string([]rune(input.TargetStruct)[:1]))
	receiver := receiverName + " *" + input.TargetStruct

	var stubsBuf bytes.Buffer
	var methodNames []string
	for _, m := range missing {
		sig := m.Type().(*types.Signature)
		stub := implGenerateMethodStub(receiver, m.Name(), sig, stubBody, qual)
		stubsBuf.WriteString("\n")
		stubsBuf.WriteString(stub)
		stubsBuf.WriteString("\n")
		methodNames = append(methodNames, m.Name())
	}

	modified := slices.Concat(original, stubsBuf.Bytes())
	msg := fmt.Sprintf("generated %d method stub(s): %s", len(missing), strings.Join(methodNames, ", "))
	return ws.AddChange(structFilePath, original, modified, msg)
}

// implQualifierForPkg returns a types.Qualifier that omits the qualifier for
// the struct's own package and uses the short name for all others.
func implQualifierForPkg(pkg *packages.Package) types.Qualifier {
	if pkg == nil {
		return nil
	}
	return func(other *types.Package) string {
		if pkg.Types != nil && other == pkg.Types {
			return ""
		}
		return other.Name()
	}
}

// implGenerateMethodStub builds a single method stub string.
func implGenerateMethodStub(receiver, name string, sig *types.Signature, body string, qual types.Qualifier) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "func (%s) %s(", receiver, name)

	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		if i > 0 {
			buf.WriteString(", ")
		}
		p := params.At(i)
		pname := p.Name()
		if pname == "" {
			pname = fmt.Sprintf("arg%d", i)
		}
		fmt.Fprintf(&buf, "%s %s", pname, types.TypeString(p.Type(), qual))
	}
	buf.WriteByte(')')

	results := sig.Results()
	if results.Len() > 0 {
		buf.WriteByte(' ')
		if results.Len() == 1 && results.At(0).Name() == "" {
			buf.WriteString(types.TypeString(results.At(0).Type(), qual))
		} else {
			buf.WriteByte('(')
			for i := 0; i < results.Len(); i++ {
				if i > 0 {
					buf.WriteString(", ")
				}
				r := results.At(i)
				if r.Name() != "" {
					buf.WriteString(r.Name() + " ")
				}
				buf.WriteString(types.TypeString(r.Type(), qual))
			}
			buf.WriteByte(')')
		}
	}

	buf.WriteString(" {\n\t")
	buf.WriteString(body)
	buf.WriteString("\n}")
	return buf.String()
}

// implFindInterface searches for an interface named ifaceName in the provided
// packages. When ifacePkgHint is non-empty (e.g. "io"), it also does a BFS
// through transitive imports and, as a last resort, loads the target package
// directly so that stdlib interfaces like io.Reader can always be found even
// when the implementing package does not import the interface package.
func implFindInterface(
	ctx context.Context,
	pkgs []*packages.Package,
	modRoot, ifaceName, ifacePkgHint string,
) *types.Interface {
	lookupInPkg := func(p *packages.Package) *types.Interface {
		if p.Types == nil {
			return nil
		}
		if ifacePkgHint != "" && !strings.HasSuffix(p.PkgPath, ifacePkgHint) && p.Name != ifacePkgHint {
			return nil
		}
		obj := p.Types.Scope().Lookup(ifaceName)
		if obj == nil {
			return nil
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			return nil
		}
		iface, ok := named.Underlying().(*types.Interface)
		if !ok {
			return nil
		}
		return iface
	}

	// 1. Search top-level packages.
	for _, pkg := range pkgs {
		if iface := lookupInPkg(pkg); iface != nil {
			return iface
		}
	}

	// 2. BFS through all transitive imports.
	visited := make(map[string]bool)
	queue := make([]*packages.Package, 0, len(pkgs))
	for _, pkg := range pkgs {
		for _, imp := range pkg.Imports {
			if !visited[imp.PkgPath] {
				visited[imp.PkgPath] = true
				queue = append(queue, imp)
			}
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if iface := lookupInPkg(cur); iface != nil {
			return iface
		}
		for _, imp := range cur.Imports {
			if !visited[imp.PkgPath] {
				visited[imp.PkgPath] = true
				queue = append(queue, imp)
			}
		}
	}

	// 3. If the hint looks like a specific importable package (e.g. "io"),
	// load it directly. This handles external/stdlib packages that are not
	// transitively imported by the user's code.
	if ifacePkgHint == "" {
		return nil
	}
	cfg := &packages.Config{
		Context: ctx,
		Mode:    packages.NeedTypes | packages.NeedSyntax | packages.NeedName,
		Dir:     modRoot,
	}
	loaded, err := packages.Load(cfg, ifacePkgHint)
	if err != nil || len(loaded) == 0 {
		return nil
	}
	for _, pkg := range loaded {
		if iface := lookupInPkg(pkg); iface != nil {
			return iface
		}
	}
	return nil
}
