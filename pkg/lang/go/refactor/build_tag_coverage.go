// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// detectExcludedFiles walks each loaded package and returns one human-readable
// note per package that contains .go files excluded by the active host build
// tags. The notes are suitable for [Transaction.AddNote] (which dedupes), and
// give the user the exact filenames + their build constraint so they know which
// platform/tag variants the refactor did NOT cover.
//
// Why this matters: golang.org/x/tools/go/packages.Load only considers files
// whose build constraints satisfy the current GOOS/GOARCH/build-tag set. A
// rename or signature change against a symbol that has Linux + Windows variants
// (e.g. //go:build unix vs //go:build windows split files) silently misses the
// non-host variant on every run, producing a half-finished refactor that breaks
// the cross-platform build until the user notices.
//
// Detection strategy:
//   - Collect every loaded file from pkg.GoFiles and pkg.CompiledGoFiles.
//     pkg.IgnoredFiles contains exactly the files excluded by build tags, so
//     we surface those directly.
//   - For each ignored .go file (skipping _test.go), read the first ~50 lines
//     and extract the first //go:build or // +build line as the displayed
//     constraint, so the user can see what they need to set to cover it.
//   - Skip files that don't look like Go source (e.g. .syso, generated stubs).
//
// The returned slice is sorted for deterministic note ordering, and each entry
// is a fully-formed sentence ready to surface verbatim in Output.Notes.
func detectExcludedFiles(pkgs []*packages.Package) []string {
	type pkgExclusion struct {
		pkgPath string
		files   []excludedFile
	}

	seen := make(map[string]bool) // dedupe by absolute file path
	byPkg := make(map[string]*pkgExclusion)

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}
		for _, f := range pkg.IgnoredFiles {
			if seen[f] {
				continue
			}
			seen[f] = true

			base := filepath.Base(f)
			if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
				continue
			}

			constraint := readBuildConstraint(f)
			if constraint == "" {
				// No explicit constraint line — the filename-suffix convention
				// (foo_windows.go, foo_linux_amd64.go) is the constraint. Use
				// the filename itself to hint at it.
				constraint = filenameTagHint(base)
			}
			if constraint == "" {
				// Truly no build-tag signal — skip; this file isn't part of
				// the build-tag coverage gap.
				continue
			}

			key := pkg.PkgPath
			if key == "" {
				key = pkg.ID
			}
			ent, ok := byPkg[key]
			if !ok {
				ent = &pkgExclusion{pkgPath: key}
				byPkg[key] = ent
			}
			ent.files = append(ent.files, excludedFile{name: base, constraint: constraint})
		}
	}

	if len(byPkg) == 0 {
		return nil
	}

	keys := make([]string, 0, len(byPkg))
	for k := range byPkg {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	notes := make([]string, 0, len(keys))
	for _, k := range keys {
		ent := byPkg[k]
		sort.Slice(ent.files, func(i, j int) bool {
			return ent.files[i].name < ent.files[j].name
		})
		parts := make([]string, 0, len(ent.files))
		for _, f := range ent.files {
			parts = append(parts, fmt.Sprintf("%s (%s)", f.name, f.constraint))
		}
		notes = append(notes, fmt.Sprintf(
			"Build-tag warning: %d file(s) in %s were excluded by host build tags "+
				"(current: GOOS=%s GOARCH=%s). The refactor only covered the active-tag set. "+
				"Files excluded: %s. Rerun under each relevant tag set "+
				"(e.g. GOOS=windows go build) to verify, or use a build matrix in CI.",
			len(ent.files), ent.pkgPath, runtime.GOOS, runtime.GOARCH, strings.Join(parts, ", "),
		))
	}
	return notes
}

type excludedFile struct {
	name       string
	constraint string
}

// readBuildConstraint reads up to the first ~50 lines of path and returns the
// first //go:build or // +build line it encounters, trimmed. Empty when neither
// is present (or the file can't be read). Stops at the first blank line that
// follows a non-comment line, since build constraints must precede the package
// clause.
func readBuildConstraint(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for i := 0; i < 50 && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "//go:build ") {
			return line
		}
		if strings.HasPrefix(line, "// +build ") {
			return line
		}
		if strings.HasPrefix(line, "package ") {
			return ""
		}
	}
	return ""
}

// filenameTagHint inspects a filename for the GOOS/GOARCH suffix convention
// (foo_windows.go, foo_linux_amd64.go) and returns a human-readable hint like
// "filename suffix: _windows". Returns "" when the filename has no recognized
// platform suffix.
func filenameTagHint(name string) string {
	stem := strings.TrimSuffix(name, ".go")
	parts := strings.Split(stem, "_")
	if len(parts) < 2 {
		return ""
	}
	// Check the last 1-2 segments against known GOOS / GOARCH values.
	knownOS := map[string]bool{
		"aix": true, "android": true, "darwin": true, "dragonfly": true,
		"freebsd": true, "hurd": true, "illumos": true, "ios": true, "js": true,
		"linux": true, "netbsd": true, "openbsd": true, "plan9": true,
		"solaris": true, "wasip1": true, "windows": true, "unix": true,
	}
	knownArch := map[string]bool{
		"386": true, "amd64": true, "arm": true, "arm64": true, "loong64": true,
		"mips": true, "mips64": true, "mips64le": true, "mipsle": true,
		"ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true,
		"wasm": true,
	}
	last := parts[len(parts)-1]
	// Check OS_ARCH pair first (most specific) before falling back to single
	// suffix. Otherwise "foo_linux_amd64.go" reports just "_amd64" and the
	// user loses the OS information.
	if len(parts) >= 3 {
		penult := parts[len(parts)-2]
		if knownOS[penult] && knownArch[last] {
			return "filename suffix: _" + penult + "_" + last
		}
	}
	if knownOS[last] || knownArch[last] {
		return "filename suffix: _" + last
	}
	return ""
}
