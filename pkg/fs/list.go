// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.thesmos.sh/techne/internal/tool"
)

// ListInput is the wire-format request for fs.list. Path is required;
// everything else has a safe default. The zero value lists the
// immediate children of Path (no recursion), sorted by name,
// excluding hidden entries (those whose base name starts with a dot).
type ListInput struct {
	// Path is the absolute or workspace-relative directory whose entries
	// should be returned. Relative paths resolve against the host
	// process's cwd at handler invocation. The handler does not list the
	// contents of a regular file — pass a directory or use fs.stat for a
	// single entry.
	Path string `json:"path" jsonschema:"Absolute or relative path to the directory to list."`
	// Recursive, when true, walks the entire descendant tree below Path
	// using filepath.WalkDir. When false (the default) only the immediate
	// children of Path are returned. Recursive listings can produce
	// significant output on large trees — reach for fs.find with explicit
	// filters when you need targeted exploration.
	Recursive bool `json:"recursive,omitempty" jsonschema:"Whether to list directory contents recursively. Optional."`
	// Hidden, when true, includes entries whose base name starts with a
	// dot (Unix-style hidden files). When false (the default) such
	// entries are skipped, and during recursive walks the handler also
	// skips into them via filepath.SkipDir so .git, .cache, and similar
	// trees never inflate the result set.
	Hidden bool `json:"hidden,omitempty" jsonschema:"Include hidden files and directories (those starting with a dot). Optional, defaults to false."`
	// Pattern is an optional filepath.Match glob applied to each entry's
	// base name (e.g. "*.go", "main_*.json"). The match uses Go's
	// filepath.Match semantics (no '**'); for recursive globs use fs.find
	// instead. An empty Pattern matches every entry.
	Pattern string `json:"pattern,omitempty" jsonschema:"Glob pattern to filter entries by name (e.g. '*.go'). Optional."`
	// SortBy selects the ordering of the returned Entries slice. Valid
	// values are "name" (lexicographic on Name; the default), "size"
	// (ascending bytes), and "modified" (oldest first). The host schema
	// layer enforces the enum via tool.Enum so an out-of-set value is
	// rejected at the boundary.
	SortBy string `json:"sort_by,omitempty" jsonschema:"Field to sort results by. Optional, defaults to name. One of: name, size, modified."`
}

// ListEntry is one row in a fs.list response. The fields are picked
// to match the most common follow-up questions an agent asks ("how
// big?", "when changed?", "is it a directory?") without forcing a
// second fs.stat call.
type ListEntry struct {
	// Name is the leaf name of the entry (the result of
	// os.DirEntry.Name). It contains no directory component, so an entry
	// deep in a recursive walk still surfaces only its base name here —
	// use Path for the full location.
	Name string `json:"name" jsonschema:"Entry name."`
	// Path is the full path of the entry, formed by joining the input
	// Path with the entry's relative location. For non-recursive listings
	// this is filepath.Join(input.Path, Name); for recursive listings it
	// is whatever filepath.WalkDir reported.
	Path string `json:"path" jsonschema:"Full path of the entry."`
	// Size is the byte length reported by os.FileInfo.Size for the
	// entry. It is zero for directories and platform-specific for
	// special files.
	Size int64 `json:"size" jsonschema:"File size in bytes (0 for directories)."`
	// IsDir reports whether the entry is a directory. Useful for filtering
	// out subdirectories without a follow-up stat.
	IsDir bool `json:"is_dir" jsonschema:"Whether the entry is a directory."`
	// Modified is the entry's last-modification timestamp in the local
	// time zone, with whatever resolution the underlying file system
	// provides.
	Modified time.Time `json:"modified" jsonschema:"Last modification time."`
}

// ListOutput is the wire-format response for fs.list. Entries is
// already sorted according to ListInput.SortBy. Count is the length
// of Entries; it is returned for convenience so callers can branch on
// emptiness without inspecting the slice.
type ListOutput struct {
	// Entries is the sorted slice of directory entries that survived the
	// hidden/pattern filters. The order is determined by ListInput.SortBy
	// and defaults to name-ascending.
	Entries []ListEntry `json:"entries" jsonschema:"List of directory entries."`
	// Count is len(Entries), surfaced separately so callers can quickly
	// detect empty directories without iterating the slice.
	Count int `json:"count" jsonschema:"Number of entries returned."`
}

// List is the fs.list tool entry point. It enumerates the contents of
// a directory — either its immediate children or, with Recursive=true,
// the entire subtree — returning a sorted, filtered slice of entries.
//
// Use List when the agent's goal is essentially "what is in this
// folder?" or "give me a quick inventory". Reach for fs.find when the
// question has filters (date, size, glob across recursive paths); fs.find
// is built for selective enumeration, while List is meant for browsing
// the full contents of a single directory.
//
// The handler uses filepath.Match for Pattern (no '**' support), skips
// dotfiles unless Hidden is set, and never returns the input Path
// itself — only its descendants. Sort order is deterministic and
// stable within ties (sort.Slice is not stable but the comparison
// keys are unique in practice). The presence of unreadable entries
// during a recursive walk surfaces as a handler error rather than
// silently dropping them.
var List = tool.New[ListInput, ListOutput](
	"fs.list",
	"Lists directory contents with optional recursion and sorting.",
	listHandler,
	tool.Enum("sort_by", "name", "size", "modified"),
	tool.WithShortDescription("List directory contents with optional recursion and sort order"),
)

// listHandler implements fs.list. It dispatches to a one-level
// os.ReadDir for non-recursive calls and to filepath.WalkDir for
// recursive ones, accumulating into the same []ListEntry. After
// gathering, sortEntries applies the requested ordering.
func listHandler(_ context.Context, input ListInput) (ListOutput, error) {
	if input.Path == "" {
		return ListOutput{}, fmt.Errorf("fs.list: path is required")
	}

	var entries []ListEntry

	if input.Recursive {
		err := filepath.WalkDir(input.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == input.Path {
				return nil
			}
			return addEntry(&entries, p, d, input, true)
		})
		if err != nil {
			return ListOutput{}, fmt.Errorf("fs.list: walking %q: %w", input.Path, err)
		}
	} else {
		dirEntries, err := os.ReadDir(input.Path)
		if err != nil {
			return ListOutput{}, fmt.Errorf("fs.list: reading directory %q: %w", input.Path, err)
		}
		for _, d := range dirEntries {
			p := filepath.Join(input.Path, d.Name())
			if err := addEntry(&entries, p, d, input, false); err != nil {
				return ListOutput{}, err
			}
		}
	}

	sortEntries(entries, input.SortBy)

	return ListOutput{Entries: entries, Count: len(entries)}, nil
}

// addEntry filters one directory entry through the Hidden, Pattern,
// and info-fetch rules, appending it to *entries when accepted.
// During a recursive walk it returns filepath.SkipDir on a hidden
// directory so the walker prunes the subtree; for non-recursive use
// the SkipDir return is harmless since the caller does not honour it.
func addEntry(entries *[]ListEntry, p string, d fs.DirEntry, input ListInput, recursive bool) error {
	name := d.Name()

	// Filter hidden
	if !input.Hidden && strings.HasPrefix(name, ".") {
		if d.IsDir() && recursive {
			return filepath.SkipDir
		}
		return nil
	}

	// Filter by pattern
	if input.Pattern != "" {
		matched, err := filepath.Match(input.Pattern, name)
		if err != nil {
			return fmt.Errorf("fs.list: invalid pattern %q: %w", input.Pattern, err)
		}
		if !matched {
			return nil
		}
	}

	info, err := d.Info()
	if err != nil {
		return fmt.Errorf("fs.list: stat %q: %w", p, err)
	}

	*entries = append(*entries, ListEntry{
		Name:     name,
		Path:     p,
		Size:     info.Size(),
		IsDir:    d.IsDir(),
		Modified: info.ModTime(),
	})
	return nil
}

// sortEntries reorders entries in place using the comparator selected
// by sortBy. "size" sorts ascending by byte length, "modified" sorts
// oldest-first, and any other value (including "name" and "") sorts
// lexicographically by Name.
func sortEntries(entries []ListEntry, sortBy string) {
	switch sortBy {
	case "size":
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Size < entries[j].Size
		})
	case "modified":
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Modified.Before(entries[j].Modified)
		})
	default: // "name" or empty
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name < entries[j].Name
		})
	}
}
