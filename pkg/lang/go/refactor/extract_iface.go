// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bytes"
	"context"
	"fmt"
	"go/types"
	"os"
	"sort"

	"golang.org/x/tools/go/packages"
)

// ExtractInterfaceAction derives an interface from the exported method set of a
// struct and writes it into the workspace. The generated interface lists every
// exported method (value-receiver and pointer-receiver, deduplicated by name),
// each preserving its parameter and result types verbatim from the struct's
// signatures.
//
// Key behaviors:
//
//   - Method-set union: value-receiver methods come from the named type's
//     direct method set; pointer-only methods are picked up from
//     types.NewMethodSet(*T). Names are deduplicated so a method defined on
//     both value and pointer receivers does not appear twice.
//   - Deterministic ordering: methods are sorted alphabetically by name before
//     emission so the output is stable across runs and the generated file diffs
//     cleanly.
//   - Qualification: types from the target package are emitted unqualified;
//     everything else uses its package's short name. Tests subjecting the
//     generated source to gofmt confirm the qualifier is correct for the
//     destination's import set.
//   - Target file: when TargetFile is supplied, the interface is appended there
//     (creating the file with a `package` clause if missing); otherwise it
//     lands in the struct's own source file.
//
// The action does NOT rewrite call sites to use the new interface — that is
// left to follow-up rename or change_type calls. The intent here is to PRODUCE
// the interface; substituting it for the concrete type at use-sites is a
// separate decision and a separate transaction.
type ExtractInterfaceAction struct{}

// Name implements [RefactorStrategy] and returns [ActionExtractInterface].
func (*ExtractInterfaceAction) Name() string { return ActionExtractInterface }

func init() { RegisterAction(&ExtractInterfaceAction{}) }

// Execute is the [RefactorStrategy] entry point. It validates required inputs,
// looks up the struct, collects its exported methods, renders the interface
// text, and stages a single file change on ws.
//
// Failure modes:
//
//   - input.TargetStruct or input.NewInterfaceName is empty — early error.
//   - The struct is not found in any loaded package — error naming the struct.
//   - The resolved symbol is not a named type — error.
//   - The struct has no exported methods — error rather than emit an empty
//     interface.
//   - The build gate fails after the file is appended — full rollback.
//
// When TargetFile is empty the interface is appended to the struct's defining
// file. When TargetFile points at a non-existent path, a package clause
// matching the struct's package is synthesized before the interface body.
func (*ExtractInterfaceAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.TargetStruct == "" || input.NewInterfaceName == "" {
		return fmt.Errorf("target_struct and new_interface_name are required")
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	structObj := FindSymbolObject(pkgs, input.TargetStruct, input.Package, "", 0)
	if structObj == nil {
		return fmt.Errorf("struct %q not found", input.TargetStruct)
	}

	structNamed, ok := structObj.Type().(*types.Named)
	if !ok {
		return fmt.Errorf("%q is not a named type", input.TargetStruct)
	}

	// Collect all exported methods (value + pointer receivers, deduplicated).
	seen := make(map[string]bool)
	var publicMethods []*types.Func

	// Value receiver methods.
	for m := range structNamed.Methods() {
		if m.Exported() && !seen[m.Name()] {
			seen[m.Name()] = true
			publicMethods = append(publicMethods, m)
		}
	}

	// Pointer receiver methods (may add pointer-only methods).
	ptrMS := types.NewMethodSet(types.NewPointer(structNamed))
	for sel := range ptrMS.Methods() {
		mObj, ok := sel.Obj().(*types.Func)
		if !ok || !mObj.Exported() || seen[mObj.Name()] {
			continue
		}
		seen[mObj.Name()] = true
		publicMethods = append(publicMethods, mObj)
	}

	if len(publicMethods) == 0 {
		return fmt.Errorf("no exported methods found on %q to extract", input.TargetStruct)
	}

	// Sort for deterministic output.
	sort.Slice(publicMethods, func(i, j int) bool {
		return publicMethods[i].Name() < publicMethods[j].Name()
	})

	// Resolve target package and struct file.
	var targetPkg *packages.Package
	structFilePath := ""
	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		pos := pkg.Fset.Position(structObj.Pos())
		if pos.Filename != "" {
			structFilePath = pos.Filename
			targetPkg = pkg
			break
		}
	}

	targetFilePath := input.TargetFile
	if targetFilePath == "" {
		targetFilePath = structFilePath
	}

	qual := implQualifierForPkg(targetPkg)

	// Build interface source.
	var ifaceBuf bytes.Buffer
	fmt.Fprintf(&ifaceBuf, "\n// %s is automatically extracted from %s.\n", input.NewInterfaceName, input.TargetStruct)
	fmt.Fprintf(&ifaceBuf, "type %s interface {\n", input.NewInterfaceName)

	for _, m := range publicMethods {
		sig := m.Type().(*types.Signature)
		ifaceBuf.WriteString("\t" + m.Name() + "(")

		params := sig.Params()
		for i := 0; i < params.Len(); i++ {
			if i > 0 {
				ifaceBuf.WriteString(", ")
			}
			p := params.At(i)
			if p.Name() != "" {
				ifaceBuf.WriteString(p.Name() + " ")
			}
			ifaceBuf.WriteString(types.TypeString(p.Type(), qual))
		}
		ifaceBuf.WriteString(")")

		results := sig.Results()
		if results.Len() > 0 {
			ifaceBuf.WriteString(" ")
			if results.Len() == 1 && results.At(0).Name() == "" {
				ifaceBuf.WriteString(types.TypeString(results.At(0).Type(), qual))
			} else {
				ifaceBuf.WriteByte('(')
				for i := 0; i < results.Len(); i++ {
					if i > 0 {
						ifaceBuf.WriteString(", ")
					}
					r := results.At(i)
					if r.Name() != "" {
						ifaceBuf.WriteString(r.Name() + " ")
					}
					ifaceBuf.WriteString(types.TypeString(r.Type(), qual))
				}
				ifaceBuf.WriteByte(')')
			}
		}
		ifaceBuf.WriteString("\n")
	}
	ifaceBuf.WriteString("}\n")

	// Apply to workspace.
	original, _ := os.ReadFile(targetFilePath) // empty if new file
	var modified []byte

	if len(original) == 0 {
		pkgName := "main"
		if targetPkg != nil {
			pkgName = targetPkg.Name
		}
		modified = append(modified, fmt.Appendf(nil, "package %s\n\n", pkgName)...)
	} else {
		modified = append(modified, original...)
	}

	modified = append(modified, ifaceBuf.Bytes()...)

	msg := fmt.Sprintf("extracted interface %s with %d method(s)", input.NewInterfaceName, len(publicMethods))
	return ws.AddChange(targetFilePath, original, modified, msg)
}
