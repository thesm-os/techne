// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"go.thesmos.sh/techne/internal/tool"
)

// FindInput is the wire-format request for fs.find. Root and Pattern
// are required; every other field narrows the result set. The zero
// value for all optional fields means "no filter", so a bare call
// returns every entry under Root whose base name matches Pattern.
//
// Design note: filters operate on the base name (filepath.Match
// semantics), not the full relative path, so a pattern like "*.go"
// works intuitively regardless of how deep the file lives in the
// tree.
type FindInput struct {
	// Root is the absolute or workspace-relative directory at which the
	// walk begins. The walk descends from Root subject to MaxDepth and
	// Ignore; the Root entry itself is never returned, only its
	// descendants. Relative paths resolve against the host process's cwd.
	Root string `json:"root" jsonschema:"Root directory to search from."`
	// Pattern is a filepath.Match glob applied to each entry's base name
	// ("*.go", "main_*.json", "README*"). The match is on the leaf name
	// only — use a recursive walk plus Type/Ignore/path filters to scope
	// by directory rather than embedding directory segments into the
	// pattern. Invalid patterns surface as a handler error during the
	// walk.
	Pattern string `json:"pattern" jsonschema:"Glob pattern to match file names against."`
	// Type restricts results by entry kind. Valid values are "file",
	// "dir", and "any" (the default; an empty string is treated as
	// "any"). The host schema layer enforces the enum via tool.Enum.
	// Combine with Pattern to find e.g. only directories named
	// "vendor" (Type="dir", Pattern="vendor").
	Type string `json:"type,omitempty" jsonschema:"Restrict matches to files, directories, or both. Optional, defaults to any. One of: file, dir, any."`
	// MaxDepth caps how many directory levels below Root are descended.
	// A value of zero (the default) means unlimited. Depth is computed
	// as the number of filepath separators in the path relative to Root,
	// so MaxDepth=1 returns only direct children of Root and MaxDepth=2
	// includes their children, and so on.
	MaxDepth int `json:"max_depth,omitempty" jsonschema:"Maximum directory depth to descend into. Optional, unlimited by default."`
	// Ignore is a comma-separated list of filepath.Match globs applied to
	// each entry's base name. Matching entries are skipped, and matching
	// directories are pruned entirely from the walk via filepath.SkipDir.
	// Useful for excluding noisy trees: ".git,node_modules,vendor".
	// Patterns are trimmed of surrounding whitespace; empty patterns are
	// ignored.
	Ignore string `json:"ignore,omitempty" jsonschema:"Comma-separated glob patterns of paths to exclude from the search (e.g. '.git,node_modules'). Optional."`
	// Hidden, when true, includes entries whose base name starts with a
	// dot. When false (the default) such entries are skipped, and during
	// the walk hidden directories are pruned via filepath.SkipDir so
	// their contents never appear. The Root itself is exempt from the
	// hidden filter so a hidden root directory can still be walked
	// explicitly.
	Hidden bool `json:"hidden,omitempty" jsonschema:"Include hidden files and directories (those starting with a dot). Optional, defaults to false."`
	// MaxResults stops the walk after this many entries have been
	// collected (returning filepath.SkipAll). Zero (the default) means
	// unlimited. Pair with a precise Pattern to bound how much work the
	// handler does on large trees.
	MaxResults int `json:"max_results,omitempty" jsonschema:"Stop after returning this many matches. Optional, unlimited by default."`
	// ModifiedAfter restricts results to entries with mtime strictly
	// after the parsed timestamp. Accepts RFC 3339 ("2026-01-02T15:04:05Z")
	// or a bare date ("2026-01-02"); bare dates parse at midnight UTC. An
	// empty string disables the filter.
	ModifiedAfter string `json:"modified_after,omitempty" jsonschema:"Only include entries modified after this date (RFC 3339 or YYYY-MM-DD). Optional."`
	// ModifiedBefore restricts results to entries with mtime strictly
	// before the parsed timestamp, using the same formats as
	// ModifiedAfter. Combine the two for a window. The strict-inequality
	// semantics on both ends mean exact boundary matches are excluded —
	// use a slightly wider window if exact matches matter.
	ModifiedBefore string `json:"modified_before,omitempty" jsonschema:"Only include entries modified before this date (RFC 3339 or YYYY-MM-DD). Optional."`
	// MinSize restricts results to files at least this large. Accepts
	// human-readable units ("1KB", "10MB", "500B", "2TB") with binary
	// multipliers (KB = 2^10, MB = 2^20, etc.) or a bare decimal byte
	// count. Empty disables the filter.
	MinSize string `json:"min_size,omitempty" jsonschema:"Only include files at least this large (e.g. '10KB', '1MB'). Optional."`
	// MaxSize restricts results to files at most this large, using the
	// same format as MinSize. Combine with MinSize to find files in a
	// size band. Both filters operate on file size as reported by
	// os.FileInfo.Size and have no effect on directories (which always
	// report zero).
	MaxSize string `json:"max_size,omitempty" jsonschema:"Only include files at most this large (e.g. '10KB', '1MB'). Optional."`
}

// FindEntry is one row in a fs.find response. It mirrors ListEntry
// but is populated from a filepath.WalkDir traversal so Path is the
// full path joined from Root, not a leaf-relative path.
type FindEntry struct {
	// Path is the full path of the matching entry, as reported by
	// filepath.WalkDir during the traversal of Root.
	Path string `json:"path" jsonschema:"Full path of the entry."`
	// Name is the leaf component of the entry, equal to filepath.Base of
	// Path.
	Name string `json:"name" jsonschema:"Base name of the entry."`
	// Size is the byte length of the file. Always zero for directories.
	Size int64 `json:"size" jsonschema:"Size in bytes."`
	// IsDir reports whether the entry is a directory.
	IsDir bool `json:"is_dir" jsonschema:"Whether the entry is a directory."`
	// Modified is the entry's last-modification timestamp in the local
	// time zone.
	Modified time.Time `json:"modified" jsonschema:"Last modification time."`
}

// FindOutput is the wire-format response for fs.find. Entries is in
// walk order — effectively depth-first, alphabetically within each
// directory — not a sort imposed by the handler. Count is len(Entries),
// surfaced separately so callers can branch on emptiness without
// inspecting the slice.
type FindOutput struct {
	// Entries is the ordered slice of matching entries. The list is
	// truncated to MaxResults when that limit is exceeded; an empty slice
	// means no entry matched. The slice is always non-nil, even when
	// empty, so JSON marshalling produces "[]" rather than "null".
	Entries []FindEntry `json:"entries" jsonschema:"List of matching entries."`
	// Count is len(Entries), provided for quick emptiness checks.
	Count int `json:"count" jsonschema:"Number of matches found."`
}

// Find is the fs.find tool entry point. It walks a directory tree
// from Root and returns every entry whose base name matches Pattern,
// further filtered by type, depth, ignore globs, hidden status,
// result cap, mtime window, and size band.
//
// Use Find when the question is selective — "locate every Go file
// modified in the last week" — and reach for fs.list when you just
// want to browse a single directory's contents. For locating Go
// symbols (functions, types, methods) prefer lang.go.search, which
// understands the language; fs.find can only match by filename.
//
// Key behaviours: Pattern matches against the base name only (no
// '**'); Ignore patterns prune both individual entries and entire
// directory subtrees; depth is measured in path separators relative to
// Root; date strings accept RFC 3339 or YYYY-MM-DD; size strings
// accept B/KB/MB/GB/TB with binary multipliers; the walk is
// single-threaded and proceeds in lexicographic order within each
// directory.
var Find = tool.New[FindInput, FindOutput](
	"fs.find",
	"Finds files by glob pattern with size/date filters. For Go symbol locations, prefer lang.go.search.",
	findHandler,
	tool.Enum("type", "file", "dir", "any"),
	tool.WithShortDescription("Find files by glob with size, date, depth, and ignore filters"),
)

// findHandler implements fs.find. It validates and parses the date
// and size filters up front, then drives a filepath.WalkDir over
// Root, applying depth, hidden, ignore, type, pattern, mtime, and
// size tests in that order. The walk short-circuits via
// filepath.SkipAll once MaxResults has been reached.
func findHandler(_ context.Context, input FindInput) (FindOutput, error) {
	if input.Root == "" {
		return FindOutput{}, fmt.Errorf("fs.find: root is required")
	}
	if input.Pattern == "" {
		return FindOutput{}, fmt.Errorf("fs.find: pattern is required")
	}

	// Parse ignore patterns
	var ignorePatterns []string
	if input.Ignore != "" {
		for p := range strings.SplitSeq(input.Ignore, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				ignorePatterns = append(ignorePatterns, p)
			}
		}
	}

	typeFilter := input.Type
	if typeFilter == "" {
		typeFilter = "any"
	}

	// Parse time filters
	var modAfter, modBefore time.Time
	if input.ModifiedAfter != "" {
		t, err := parseDate(input.ModifiedAfter)
		if err != nil {
			return FindOutput{}, fmt.Errorf("fs.find: invalid modified_after %q: %w", input.ModifiedAfter, err)
		}
		modAfter = t
	}
	if input.ModifiedBefore != "" {
		t, err := parseDate(input.ModifiedBefore)
		if err != nil {
			return FindOutput{}, fmt.Errorf("fs.find: invalid modified_before %q: %w", input.ModifiedBefore, err)
		}
		modBefore = t
	}

	// Parse size filters
	var minSize, maxSize int64 = -1, -1
	if input.MinSize != "" {
		s, err := parseSize(input.MinSize)
		if err != nil {
			return FindOutput{}, fmt.Errorf("fs.find: invalid min_size %q: %w", input.MinSize, err)
		}
		minSize = s
	}
	if input.MaxSize != "" {
		s, err := parseSize(input.MaxSize)
		if err != nil {
			return FindOutput{}, fmt.Errorf("fs.find: invalid max_size %q: %w", input.MaxSize, err)
		}
		maxSize = s
	}

	var entries []FindEntry

	err := filepath.WalkDir(input.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}

		name := d.Name()

		// Check depth
		if input.MaxDepth > 0 {
			rel, relErr := filepath.Rel(input.Root, p)
			if relErr == nil {
				depth := strings.Count(rel, string(filepath.Separator))
				if depth > input.MaxDepth {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}

		// Skip hidden
		if !input.Hidden && strings.HasPrefix(name, ".") && p != input.Root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Check ignore patterns
		for _, pat := range ignorePatterns {
			matched, _ := filepath.Match(pat, name)
			if matched {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		if p == input.Root {
			return nil
		}

		// Type filter
		switch typeFilter {
		case "file":
			if d.IsDir() {
				return nil
			}
		case "dir":
			if !d.IsDir() {
				return nil
			}
		}

		// Pattern match against base name
		matched, err := filepath.Match(input.Pattern, name)
		if err != nil {
			return fmt.Errorf("fs.find: invalid pattern %q: %w", input.Pattern, err)
		}
		if !matched {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		modTime := info.ModTime()
		size := info.Size()

		// Date filters
		if !modAfter.IsZero() && !modTime.After(modAfter) {
			return nil
		}
		if !modBefore.IsZero() && !modTime.Before(modBefore) {
			return nil
		}

		// Size filters
		if minSize >= 0 && size < minSize {
			return nil
		}
		if maxSize >= 0 && size > maxSize {
			return nil
		}

		entries = append(entries, FindEntry{
			Path:     p,
			Name:     name,
			Size:     size,
			IsDir:    d.IsDir(),
			Modified: modTime,
		})

		if input.MaxResults > 0 && len(entries) >= input.MaxResults {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return FindOutput{}, fmt.Errorf("fs.find: walking %q: %w", input.Root, err)
	}

	if entries == nil {
		entries = []FindEntry{}
	}

	return FindOutput{Entries: entries, Count: len(entries)}, nil
}

// parseDate accepts either RFC 3339 (with timezone) or the bare
// "2006-01-02" date form, in that order. Bare dates resolve to
// midnight UTC. Used by findHandler for the ModifiedAfter and
// ModifiedBefore filters.
func parseDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// parseSize converts a human-readable size string ("10KB", "1.5MB",
// "500B", "2TB") into a byte count. Suffixes use binary multipliers
// (KB = 2^10, MB = 2^20, GB = 2^30, TB = 2^40) consistent with most
// Unix tooling. A bare decimal integer with no suffix is treated as
// a byte count. Whitespace is trimmed; unrecognised forms return an
// error used by findHandler to surface invalid filter input.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	multipliers := []struct {
		suffix string
		factor int64
	}{
		{"TB", 1 << 40},
		{"GB", 1 << 30},
		{"MB", 1 << 20},
		{"KB", 1 << 10},
		{"B", 1},
	}

	upper := strings.ToUpper(s)
	for _, m := range multipliers {
		if strings.HasSuffix(upper, m.suffix) {
			numStr := strings.TrimSpace(s[:len(s)-len(m.suffix)])
			var n float64
			if _, err := fmt.Sscanf(numStr, "%f", &n); err != nil {
				return 0, fmt.Errorf("invalid size number in %q", s)
			}
			return int64(n * float64(m.factor)), nil
		}
	}

	// Try bare number
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("unrecognised size format %q", s)
	}
	return n, nil
}
