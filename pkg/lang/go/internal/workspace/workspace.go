// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package workspace provides Go workspace and module discovery for the lang/go tools.
//
// Discover walks upward from a starting directory to find the outermost Go
// boundary: a go.work file (workspace mode) is preferred, falling back to the
// closest go.mod (single-module mode). Tools route package loads through
// Workspace.Load so go.work expansion happens automatically.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

// Module describes a single Go module discovered inside a [Workspace]. In
// single-module mode the workspace contains exactly one Module; in go.work
// mode it contains one entry per 'use' directive.
//
// Path and Dir together identify the module unambiguously: Path is the
// logical import root (what other modules import to reach this code) and
// Dir is its physical filesystem anchor (the directory containing go.mod).
// Most callers want Dir, since loading or scanning sources requires a path
// on disk; Path is needed when constructing fully-qualified import paths
// for symbols inside the module.
type Module struct {
	// Path is the import path declared by the module's go.mod
	// ('module <path>').
	Path string // import path from the module's go.mod
	// Dir is the absolute filesystem path of the module root — the directory
	// containing go.mod.
	Dir string // absolute filesystem path of the module root
}

// Workspace is the unified abstraction over a Go module or a multi-module
// go.work setup. It exists so the lang.go.* tools can treat both layouts
// uniformly: a single module is a workspace containing exactly one module,
// and callers never have to branch on the layout to perform package loads.
//
// A Workspace is obtained from [Discover]. The discovery result — parsed
// go.mod / go.work plus the resolved module list — is computed once and
// cached process-wide by canonical root path, so tools invoked in the same
// MCP session share the same in-memory Workspace and its package cache.
//
// A Workspace also caches the most recent [Workspace.Load] result, keyed
// by (mode, sorted patterns). The cache is validated against an mtime-
// plus-file-count fingerprint over every .go file under every module,
// including the mtimes of go.mod / go.sum / go.work. Any edit, addition,
// deletion, or manifest change invalidates the entry and forces a fresh
// packages.Load. The cache is therefore safe across long-lived sessions
// where code is being refactored between calls.
//
// Concurrency: Workspace is safe for concurrent use. Cache reads and
// writes are serialised through a sync.Mutex held only across the
// fingerprint comparison and map access — never across the underlying
// packages.Load call. Two goroutines invoking Load on the same Workspace
// may therefore run packages.Load in parallel; in the steady state, both
// complete and the loser of the cache-write race simply overwrites the
// winner's entry with an equivalent result.
type Workspace struct {
	root     string
	modules  []Module
	isGoWork bool

	// Load cache. Keyed by (mode, sorted patterns); invalidated when any
	// .go file under the workspace has a newer mtime than the recorded
	// fingerprint. Concurrent-safe.
	cacheMu sync.Mutex
	cache   map[string]*loadCacheEntry
}

// loadCacheEntry records a single Load result and the fingerprint captured
// at load time. A subsequent Load with the same key reuses pkgs only if the
// fingerprint hasn't moved.
type loadCacheEntry struct {
	fingerprint fingerprint
	pkgs        []*packages.Package
}

// fingerprint summarizes the workspace state for cache-validity checks.
// Captures any change a refactor or external edit might make:
//   - maxGoMtime: catches existing .go file modifications and additions
//   - fileCount: catches deletions (max mtime alone misses these)
//   - manifestMtime: catches go.mod/go.sum/go.work changes
type fingerprint struct {
	maxGoMtime    time.Time
	fileCount     int
	manifestMtime time.Time
}

// equal reports whether two fingerprints describe the same workspace state.
func (f fingerprint) equal(o fingerprint) bool {
	return f.maxGoMtime.Equal(o.maxGoMtime) &&
		f.fileCount == o.fileCount &&
		f.manifestMtime.Equal(o.manifestMtime)
}

// Discover walks upward from startDir looking for the outermost Go
// boundary and returns a [*Workspace] rooted at it.
//
// Resolution order:
//   - A go.work file shadows every go.mod below it. If go.work exists in
//     startDir or any ancestor, the result is a multi-module workspace
//     rooted at the go.work directory, listing every 'use' entry.
//   - Otherwise the closest enclosing go.mod becomes a single-module
//     workspace rooted at that directory.
//   - If neither is found anywhere on the path to /, Discover returns an
//     error.
//
// Memoisation: results are cached process-wide, keyed by canonical root
// path. Two calls with startDirs resolving to the same root return the
// same *Workspace pointer, which is what allows the per-Workspace Load
// cache to amortise hits across tools invoked in the same MCP session.
// The cache lives for the lifetime of the binary; there is no explicit
// eviction. If you need a fresh discovery (e.g. after editing go.work),
// create a new process.
//
// A partially-broken go.work — one or more 'use' directories missing
// their go.mod — is tolerated as long as at least one module loads;
// failures are joined into the error chain returned only when no
// modules at all could be discovered.
func Discover(startDir string) (*Workspace, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve %q: %w", startDir, err)
	}
	workDir, modDir := walkUp(abs)

	var rootDir string
	switch {
	case workDir != "":
		rootDir = workDir
	case modDir != "":
		rootDir = modDir
	default:
		return nil, fmt.Errorf("workspace: no go.work or go.mod found above %s", abs)
	}

	discoverCacheMu.RLock()
	if w, ok := discoverCache[rootDir]; ok {
		discoverCacheMu.RUnlock()
		return w, nil
	}
	discoverCacheMu.RUnlock()

	var w *Workspace
	if workDir != "" {
		w, err = loadGoWork(workDir)
	} else {
		w, err = loadGoMod(modDir)
	}
	if err != nil {
		return nil, err
	}

	discoverCacheMu.Lock()
	if existing, ok := discoverCache[rootDir]; ok {
		// Lost the race; another goroutine populated the entry. Use it.
		discoverCacheMu.Unlock()
		return existing, nil
	}
	discoverCache[rootDir] = w
	discoverCacheMu.Unlock()
	return w, nil
}

// Process-scoped cache of *Workspace by canonical root path.
var (
	discoverCacheMu sync.RWMutex
	discoverCache   = map[string]*Workspace{}
)

// walkUp returns (firstGoWorkDir, firstGoModDir) encountered while walking
// from start to the filesystem root. Stops early once go.work is found, since
// go.work shadows any go.mod above it.
func walkUp(start string) (workDir, modDir string) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, modDir
		}
		if modDir == "" {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				modDir = dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", modDir
		}
		dir = parent
	}
}

func loadGoWork(dir string) (*Workspace, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.work"))
	if err != nil {
		return nil, fmt.Errorf("workspace: read go.work: %w", err)
	}
	wf, err := modfile.ParseWork("go.work", data, nil)
	if err != nil {
		return nil, fmt.Errorf("workspace: parse go.work: %w", err)
	}
	var modules []Module
	var loadErrs []error
	for _, use := range wf.Use {
		modDir := use.Path
		if !filepath.IsAbs(modDir) {
			modDir = filepath.Join(dir, modDir)
		}
		modPath, err := readModulePath(modDir)
		if err != nil {
			loadErrs = append(loadErrs, err)
			continue
		}
		modules = append(modules, Module{Path: modPath, Dir: modDir})
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("workspace: go.work at %s has no usable modules: %w", dir, errors.Join(loadErrs...))
	}
	return &Workspace{root: dir, modules: modules, isGoWork: true, cache: map[string]*loadCacheEntry{}}, nil
}

func loadGoMod(dir string) (*Workspace, error) {
	modPath, err := readModulePath(dir)
	if err != nil {
		return nil, err
	}
	return &Workspace{
		root:    dir,
		modules: []Module{{Path: modPath, Dir: dir}},
		cache:   map[string]*loadCacheEntry{},
	}, nil
}

func readModulePath(modDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(modDir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("workspace: read go.mod in %s: %w", modDir, err)
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return "", fmt.Errorf("workspace: parse go.mod in %s: %w", modDir, err)
	}
	if f.Module == nil {
		return "", fmt.Errorf("workspace: go.mod in %s has no module directive", modDir)
	}
	return f.Module.Mod.Path, nil
}

// Root returns the absolute path of the workspace root. In go.work mode
// that is the directory containing go.work; in single-module mode it is
// the single module's directory. It is suitable as a packages.Config.Dir.
func (w *Workspace) Root() string { return w.root }

// Modules returns the modules belonging to this workspace. The slice is
// never empty: even a single-module workspace returns one entry whose Dir
// equals [Workspace.Root].
//
// The returned slice is shared with the Workspace and must not be
// mutated; treat it as read-only.
func (w *Workspace) Modules() []Module { return w.modules }

// IsGoWork reports whether the workspace was discovered from a go.work
// file (multi-module mode). Callers that need to behave differently for
// the two layouts — for example, when expanding the './...' pattern, which
// must be rewritten to one pattern per 'use' directory under go.work —
// can branch on this.
func (w *Workspace) IsGoWork() bool { return w.isGoWork }

// LoadOption configures a single [Workspace.Load] call by mutating the
// underlying packages.Config before invocation. Options compose; later
// options overwrite earlier ones for the same field.
//
// LoadOptions are read-only with respect to the Workspace itself: they
// affect only the in-flight load and never the cached state. Their
// presence does, however, bypass the Load cache (see [Workspace.Load]).
type LoadOption func(*packages.Config)

// WithTests instructs [Workspace.Load] to also load test packages
// (packages.Config.Tests = true), so _test.go files and their
// synthesised test-only packages appear in the result.
//
// Use this when the consuming tool needs to analyse test code — for
// example to rename a symbol used from a _test.go file, or to gather
// callers of a helper exercised only by tests.
func WithTests() LoadOption {
	return func(c *packages.Config) { c.Tests = true }
}

// WithFset attaches an explicit *token.FileSet to the load. By default
// packages.Load synthesises its own FileSet, which is fine for one-shot
// queries but inadequate when the caller wants to compose positions
// across multiple Loads or feed them to packages that already hold a
// FileSet reference. Passing your own FileSet keeps positions
// comparable across all loads that share it.
func WithFset(fset *token.FileSet) LoadOption {
	return func(c *packages.Config) { c.Fset = fset }
}

// WithBuildFlags sets the build flags passed to the underlying 'go list'
// invocation — typically build tags such as "-tags=integration". Flags
// are passed verbatim, joined with the Go tool's standard parsing.
//
// Providing build flags changes which files are included in each package,
// so it inherently produces a different result than the default load;
// Load consequently bypasses its cache when this option is present.
func WithBuildFlags(flags ...string) LoadOption {
	return func(c *packages.Config) { c.BuildFlags = flags }
}

// Load runs packages.Load against this workspace with the given mode and
// patterns, after applying any [LoadOption] values. It is the single
// entry point that every lang.go.* tool should use for package loading,
// because it transparently:
//
//   - sets packages.Config.Dir to [Workspace.Root] so relative patterns
//     resolve correctly even when called from elsewhere in the process;
//   - expands the './...' default to one pattern per 'use' directory in
//     go.work mode (Go's own pattern rules do not match anything under
//     './...' from a go.work root);
//   - caches results keyed by (mode, sorted patterns), validated against
//     an mtime + file-count fingerprint across every .go file under every
//     module plus the manifests, so identical follow-up queries within a
//     session are cheap.
//
// When patterns is nil or empty, Load substitutes './...' (which is then
// expanded per the rule above).
//
// Caching behaviour: calls with no LoadOptions consult the cache; calls
// with any LoadOption ([WithFset], [WithTests], [WithBuildFlags]) bypass
// the cache because the option may legitimately produce a different
// result for the same patterns. A cache hit returns the exact slice
// stored previously — callers must not mutate the returned
// *packages.Package values, since other callers may be reading them.
//
// Error handling: Load returns an error only when packages.Load itself
// fails (typically an I/O or environment problem). Package-level errors
// (syntax errors, type errors, missing imports) are reported per package
// in packages.Package.Errors; tools should walk that slice rather than
// relying on Load to surface them. A successful return therefore does
// not imply every package type-checked cleanly — it only means Go's
// loader produced a result.
//
// Context is forwarded to the loader; cancelling ctx cancels the
// in-flight load and any cache update from this call is skipped.
func (w *Workspace) Load(
	ctx context.Context,
	mode packages.LoadMode,
	patterns []string,
	opts ...LoadOption,
) ([]*packages.Package, error) {
	patterns = w.expandPatterns(patterns)

	if len(opts) == 0 {
		key := cacheKey(mode, patterns)
		if cached := w.lookupCache(key); cached != nil {
			return cached, nil
		}
		pkgs, err := w.runLoad(ctx, mode, patterns, nil)
		if err != nil {
			return nil, err
		}
		w.storeCache(key, pkgs)
		return pkgs, nil
	}

	return w.runLoad(ctx, mode, patterns, opts)
}

func (w *Workspace) runLoad(
	ctx context.Context,
	mode packages.LoadMode,
	patterns []string,
	opts []LoadOption,
) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Context: ctx,
		Mode:    mode,
		Dir:     w.root,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return packages.Load(cfg, patterns...)
}

// cacheKey serializes (mode, patterns) into a stable map key.
func cacheKey(mode packages.LoadMode, patterns []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|", uint64(mode))
	for _, p := range patterns {
		b.WriteString(p)
		b.WriteByte('\x00')
	}
	return b.String()
}

func (w *Workspace) lookupCache(key string) []*packages.Package {
	w.cacheMu.Lock()
	defer w.cacheMu.Unlock()
	if w.cache == nil {
		return nil
	}
	entry, ok := w.cache[key]
	if !ok {
		return nil
	}
	current, err := w.computeFingerprint()
	if err != nil || !current.equal(entry.fingerprint) {
		// Stale or unknown — drop the entry to force a reload.
		delete(w.cache, key)
		return nil
	}
	return entry.pkgs
}

func (w *Workspace) storeCache(key string, pkgs []*packages.Package) {
	current, err := w.computeFingerprint()
	if err != nil {
		return // Refuse to cache when we can't fingerprint.
	}
	w.cacheMu.Lock()
	if w.cache == nil {
		w.cache = map[string]*loadCacheEntry{}
	}
	w.cache[key] = &loadCacheEntry{fingerprint: current, pkgs: pkgs}
	w.cacheMu.Unlock()
}

// computeFingerprint summarizes the workspace's source-of-truth state.
// Walks each module directory once, tracking the max mtime of any .go file
// AND the count of .go files (so deletions invalidate the cache even
// though they leave the max mtime unchanged). Includes the mtime of
// go.mod, go.sum, and go.work because those drive the module graph and
// can change package resolution without touching any .go file.
func (w *Workspace) computeFingerprint() (fingerprint, error) {
	var fp fingerprint

	// Manifest mtimes: go.mod and go.sum in each module, plus go.work at
	// the workspace root if present.
	for _, m := range w.modules {
		for _, name := range []string{"go.mod", "go.sum"} {
			if info, err := os.Stat(filepath.Join(m.Dir, name)); err == nil {
				if t := info.ModTime(); t.After(fp.manifestMtime) {
					fp.manifestMtime = t
				}
			}
		}
	}
	if w.isGoWork {
		if info, err := os.Stat(filepath.Join(w.root, "go.work")); err == nil {
			if t := info.ModTime(); t.After(fp.manifestMtime) {
				fp.manifestMtime = t
			}
		}
	}

	// Walk .go files.
	for _, m := range w.modules {
		walkErr := filepath.WalkDir(m.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable subtrees
			}
			if d.IsDir() {
				name := d.Name()
				if path != m.Dir && (name == "vendor" || name == "node_modules" ||
					(strings.HasPrefix(name, ".") && name != ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".go") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			fp.fileCount++
			if t := info.ModTime(); t.After(fp.maxGoMtime) {
				fp.maxGoMtime = t
			}
			return nil
		})
		if walkErr != nil {
			return fingerprint{}, walkErr
		}
	}
	return fp, nil
}

// expandPatterns rewrites "./..." (and the empty default) into per-module
// patterns when the workspace is a go.work workspace. Other patterns pass
// through unchanged.
func (w *Workspace) expandPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	if !w.isGoWork {
		return patterns
	}
	var out []string
	for _, p := range patterns {
		if p != "./..." {
			out = append(out, p)
			continue
		}
		for _, m := range w.modules {
			rel, err := filepath.Rel(w.root, m.Dir)
			if err != nil || rel == "." {
				out = append(out, "./...")
				continue
			}
			out = append(out, "./"+filepath.ToSlash(rel)+"/...")
		}
	}
	return out
}
