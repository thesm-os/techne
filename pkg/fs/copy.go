// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"go.thesmos.sh/techne/internal/tool"
)

// CopyInput is the wire-format request for fs.copy. Src and Dst are
// both required. Copying a directory requires Recursive=true; copying
// over an existing destination requires Force=true. These two flags
// are independent safety rails that the agent must opt into
// explicitly.
type CopyInput struct {
	// Src is the absolute or workspace-relative path of the source entry.
	// It may be a regular file or, with Recursive=true, a directory tree.
	// Symlinks under Src are not followed: the bytes of the link target
	// are copied as a regular file because the underlying os.Open call
	// resolves through the link.
	Src string `json:"src" jsonschema:"Source path of the file or directory to copy."`
	// Dst is the destination path. For a file copy, Dst is the new file's
	// path (not a containing directory). For a directory copy, Dst becomes
	// the root of the copied tree and is created (with the source
	// directory's mode) if missing. Parent directories of Dst must already
	// exist.
	Dst string `json:"dst" jsonschema:"Destination path."`
	// Recursive must be true when Src is a directory; otherwise the
	// handler returns an error rather than silently copying only the
	// top-level directory entry. The flag has no effect when Src is a
	// regular file. The default is false, which keeps directory copies
	// from happening by accident.
	Recursive bool `json:"recursive,omitempty" jsonschema:"Copy directories and their contents recursively. Optional, defaults to false."`
	// Force, when true, overwrites any existing file at the destination.
	// When false (the default) a pre-existing destination file causes an
	// error, preserving the original. Force applies to every file written
	// during a recursive directory copy, not just the top-level entry.
	Force bool `json:"force,omitempty" jsonschema:"Overwrite the destination if it already exists. Optional, defaults to false."`
}

// CopyOutput is the wire-format response for fs.copy. BytesCopied is
// the aggregate across every file written during a recursive copy, so
// the caller can confirm the magnitude of the operation without
// walking the destination.
type CopyOutput struct {
	// Src echoes the source path so callers chaining tool calls do not
	// need to retain the input.
	Src string `json:"src" jsonschema:"Source path."`
	// Dst echoes the destination path the data was written to.
	Dst string `json:"dst" jsonschema:"Destination path."`
	// BytesCopied is the total number of bytes written across every file
	// that the copy produced. Directory entries contribute zero. The count
	// is a useful signal for verifying that a non-empty source actually
	// delivered non-empty bytes — a CopyOutput with BytesCopied=0 on a
	// recursive copy means the source tree contained only empty files or
	// directories.
	BytesCopied int64 `json:"bytes_copied" jsonschema:"Total bytes copied."`
}

// Copy is the fs.copy tool entry point. It duplicates a file or, with
// Recursive=true, an entire directory tree to a new location while
// preserving file modes.
//
// Non-atomicity is the key property to understand: a recursive copy
// writes destination files one at a time and does not roll back on
// failure. If the call errors halfway through a directory tree, a
// partial copy is left at Dst. Agents needing transactional semantics
// should either delete Dst on failure or use fs.patch with CreateFiles
// for small multi-file payloads.
//
// File modes are preserved from the source (the destination opens
// with srcInfo.Mode()); timestamps and ownership are not. Symlinks
// are dereferenced — the target's bytes are copied as a regular
// file, not the link itself — which differs from POSIX cp default
// behaviour and is worth keeping in mind when copying source trees
// that use symlinks intentionally.
var Copy = tool.New[CopyInput, CopyOutput](
	"fs.copy",
	"Copies a file or directory.",
	copyHandler,
	tool.WithShortDescription("Copy a file or directory tree, preserving file modes"),
)

// copyHandler implements fs.copy. It stats the source to decide
// between copyFile (a regular-file path) and copyDir (a recursive
// walk that mirrors the source structure under Dst). Both paths
// aggregate written bytes into the returned CopyOutput.BytesCopied.
func copyHandler(_ context.Context, input CopyInput) (CopyOutput, error) {
	if input.Src == "" {
		return CopyOutput{}, fmt.Errorf("fs.copy: src is required")
	}
	if input.Dst == "" {
		return CopyOutput{}, fmt.Errorf("fs.copy: dst is required")
	}

	info, err := os.Stat(input.Src)
	if err != nil {
		return CopyOutput{}, fmt.Errorf("fs.copy: stat src %q: %w", input.Src, err)
	}

	var totalBytes int64

	if info.IsDir() {
		if !input.Recursive {
			return CopyOutput{}, fmt.Errorf(
				"fs.copy: %q is a directory; use recursive=true to copy directories",
				input.Src,
			)
		}
		if err := copyDir(input.Src, input.Dst, input.Force, &totalBytes); err != nil {
			return CopyOutput{}, fmt.Errorf("fs.copy: %w", err)
		}
	} else {
		n, err := copyFile(input.Src, input.Dst, input.Force)
		if err != nil {
			return CopyOutput{}, fmt.Errorf("fs.copy: %w", err)
		}
		totalBytes = n
	}

	return CopyOutput{Src: input.Src, Dst: input.Dst, BytesCopied: totalBytes}, nil
}

// copyFile duplicates a single file from src to dst, honouring the
// Force flag for pre-existing destinations. The destination is opened
// with the source's mode so permissions carry across. Returns the
// byte count from io.Copy so the recursive walker can accumulate it.
func copyFile(src, dst string, force bool) (int64, error) {
	if !force {
		if _, err := os.Stat(dst); err == nil {
			return 0, fmt.Errorf("destination %q already exists; use force=true to overwrite", dst)
		}
	}

	in, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("opening source %q: %w", src, err)
	}
	defer in.Close()

	srcInfo, err := in.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat source %q: %w", src, err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return 0, fmt.Errorf("creating destination %q: %w", dst, err)
	}
	defer out.Close()

	n, err := io.Copy(out, in)
	if err != nil {
		return n, fmt.Errorf("copying %q to %q: %w", src, dst, err)
	}
	return n, nil
}

// copyDir replicates a directory tree rooted at src under dst,
// creating directory entries with their source modes and delegating
// regular files to copyFile. Walks via filepath.WalkDir, computing
// relative paths so the destination preserves the source layout. The
// totalBytes pointer accumulates the byte count from every file
// written so the caller can report a final total.
func copyDir(src, dst string, force bool, totalBytes *int64) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source dir %q: %w", src, err)
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return fmt.Errorf("creating destination dir %q: %w", dst, err)
	}

	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)

		if d.IsDir() {
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			return os.MkdirAll(dstPath, info.Mode())
		}

		n, err := copyFile(path, dstPath, force)
		if err != nil {
			return err
		}
		*totalBytes += n
		return nil
	})
}
