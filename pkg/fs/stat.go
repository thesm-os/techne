// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.thesmos.sh/techne/internal/tool"
)

// StatInput is the wire-format request for fs.stat. Path is required;
// FollowSymlinks switches between Lstat (default, link metadata) and
// Stat (link target metadata). The split matters for tools that need
// to distinguish a symlink from its target — leaving FollowSymlinks
// false is the conservative choice.
type StatInput struct {
	// Path is the absolute or workspace-relative path of the entry to
	// inspect. The handler does not require Path to exist as a regular
	// file; directories, symlinks, and named pipes are all stat-able. A
	// non-existent path returns an os.ErrNotExist-wrapped error.
	Path string `json:"path" jsonschema:"Absolute or relative path to the file or directory."`
	// FollowSymlinks selects whether the call resolves symbolic links
	// before reading metadata. When false (the default) the handler uses
	// os.Lstat and reports the link itself: Size is the byte length of the
	// link target string, Mode includes ModeSymlink, and IsSymlink is
	// true. When true the handler uses os.Stat and reports the resolved
	// target instead, which may fail for dangling links.
	FollowSymlinks bool `json:"follow_symlinks,omitempty" jsonschema:"Follow symbolic links and report metadata of the target rather than the link itself. Optional, defaults to false."`
}

// StatOutput is the wire-format response for fs.stat. Every field is
// always populated regardless of FollowSymlinks; IsSymlink reflects
// the pre-resolution type and stays true for symlink inputs even when
// the target was followed.
type StatOutput struct {
	// Name is the final path component of the entry (the leaf name
	// returned by os.FileInfo.Name). It does not include any directory
	// portion of the original Path.
	Name string `json:"name" jsonschema:"Base name of the file or directory."`
	// Path echoes the input Path verbatim. The handler does not normalise
	// or absolutise it, so a relative input yields a relative Path —
	// useful for round-tripping the same value to a follow-up tool call.
	Path string `json:"path" jsonschema:"Full path."`
	// Size is the byte length of the entry as reported by the OS. For
	// regular files this is the file body size; for directories the
	// platform-specific directory entry size; for symbolic links (when
	// FollowSymlinks is false) the byte length of the link target string.
	Size int64 `json:"size" jsonschema:"Size in bytes."`
	// Mode is the permission portion of the entry's file mode formatted
	// as a four-digit zero-padded octal string (for example "0644",
	// "0755"). Only the lower nine permission bits and the sticky/setuid
	// bits are included — the type bits (directory, symlink) are exposed
	// via IsDir and IsSymlink instead. This format round-trips cleanly
	// through strconv.ParseUint with base 8.
	Mode string `json:"mode" jsonschema:"File permission bits as an octal string."`
	// Modified is the last-modification timestamp reported by the OS, in
	// the local time zone, with whatever resolution the underlying file
	// system provides (typically nanoseconds on Linux, seconds on FAT).
	Modified time.Time `json:"modified" jsonschema:"Last modification time."`
	// IsDir reports whether the entry is a directory. When
	// FollowSymlinks=true and Path is a symlink to a directory, this is
	// true because the call inspects the target.
	IsDir bool `json:"is_dir" jsonschema:"Whether the entry is a directory."`
	// IsSymlink reports whether the entry itself (before any link
	// resolution) is a symbolic link. The value is independent of
	// FollowSymlinks: even when the target was resolved, IsSymlink stays
	// true if the path supplied was a link.
	IsSymlink bool `json:"is_symlink" jsonschema:"Whether the entry is a symbolic link."`
}

// Stat is the fs.stat tool entry point. It returns a compact metadata
// record for a single path — name, size, mode, mtime, and type bits —
// and is the cheapest way for an agent to confirm a file's existence,
// size, or type before deciding what to do next.
//
// Design rationale: file inspection in agent loops often boils down to
// "does this exist?" or "is this a directory?" — questions that do not
// need a full directory listing. Exposing Stat as a first-class tool
// lets the agent get a yes/no in one structured call instead of
// parsing an fs.list response.
//
// The FollowSymlinks toggle is the only non-trivial knob. Default
// (false) reports the link itself, which is right for inventory and
// verification flows; setting it true is right when the agent intends
// to operate on the resolved target (e.g. copying contents). Dangling
// symlinks succeed under FollowSymlinks=false and fail with
// os.ErrNotExist under FollowSymlinks=true.
var Stat = tool.New[StatInput, StatOutput](
	"fs.stat",
	"Returns file metadata (size, permissions, timestamps).",
	statHandler,
)

// statHandler implements fs.stat. It dispatches to os.Stat or
// os.Lstat based on FollowSymlinks, then maps the resulting
// os.FileInfo into the wire-format StatOutput. The Mode field is
// rendered with %04o so it always has four digits, matching the form
// agents commonly see in unix tooling.
func statHandler(_ context.Context, input StatInput) (StatOutput, error) {
	if input.Path == "" {
		return StatOutput{}, fmt.Errorf("fs.stat: path is required")
	}

	var info os.FileInfo
	var err error
	if input.FollowSymlinks {
		info, err = os.Stat(input.Path)
	} else {
		info, err = os.Lstat(input.Path)
	}
	if err != nil {
		return StatOutput{}, fmt.Errorf("fs.stat: %w", err)
	}

	isSymlink := info.Mode()&os.ModeSymlink != 0

	return StatOutput{
		Name:      info.Name(),
		Path:      input.Path,
		Size:      info.Size(),
		Mode:      fmt.Sprintf("%04o", info.Mode().Perm()),
		Modified:  info.ModTime(),
		IsDir:     info.IsDir(),
		IsSymlink: isSymlink,
	}, nil
}
