// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"go.thesmos.sh/techne/internal/tool"
)

// WriteInput is the wire-format request for fs.write. The Path and
// Content fields are required; everything else has a safe default. The
// zero value writes Content to Path with mode 0644, truncating any
// existing file and refusing to create missing parent directories.
//
// This tool is intentionally low-level: it does not snapshot, diff, or
// verify. Agents modifying existing files should reach for fs.patch
// instead, which provides atomicity, rollback, and a unified-diff
// receipt.
type WriteInput struct {
	// Path is the absolute or workspace-relative path of the destination
	// file. The handler does not validate that Path lies inside any
	// particular root — sandboxing is the host's responsibility. If a
	// parent directory is missing the call fails unless CreateDirs is set.
	Path string `json:"path" jsonschema:"Absolute or relative path to the file to write. Example: '/var/home/user/project/config.json'."`
	// Content is the data to write. By default it overwrites any existing
	// file contents in their entirety; with Append=true it is concatenated
	// to whatever is already on disk. The string is written verbatim with
	// no transformation — line endings, BOMs, and trailing whitespace are
	// preserved exactly.
	Content string `json:"content" jsonschema:"The full content to write to the file. Overwrites existing content unless append=true."`
	// CreateDirs, when true, materialises any missing ancestor directories
	// of Path with mode 0755 before opening the file. When false (the
	// default) a missing parent directory is a hard error, which is the
	// safer choice when the agent might mistype a path — typos surface
	// immediately instead of silently creating a stray tree.
	CreateDirs bool `json:"create_dirs,omitempty" jsonschema:"Create any missing parent directories before writing. Optional, defaults to false."`
	// Mode is the destination file's permission bits expressed as an octal
	// string, for example "0644" or "0755". An empty value selects the
	// default "0644". The Mode is applied at create time and, for append
	// writes, only takes effect when the file is being created — an
	// existing file keeps its on-disk permissions.
	Mode string `json:"mode,omitempty" jsonschema:"File permission bits in octal notation (e.g. '0644'). Optional, defaults to 0644."`
	// Append, when true, opens the file in O_APPEND mode and writes
	// Content at the end of any existing data. When false (the default)
	// the file is truncated to zero length first, so Content fully
	// replaces what was there. Both modes create the file if it is
	// missing, subject to the CreateDirs constraint on parent directories.
	Append bool `json:"append,omitempty" jsonschema:"Append content to the file instead of overwriting it. Optional, defaults to false."`
}

// WriteOutput is the wire-format response for fs.write. BytesWritten
// lets the caller confirm that the entire payload reached disk without
// having to read the file back — an important property for unattended
// agents that need a deterministic success signal.
type WriteOutput struct {
	// Path echoes the destination that was written, identical to
	// WriteInput.Path. Returned for symmetry with other fs.* tools and so
	// that callers chaining tool calls do not have to retain the input.
	Path string `json:"path" jsonschema:"Path of the file that was written."`
	// BytesWritten is the number of bytes that were successfully written
	// to the file. For truncating writes this should equal len(Content);
	// for append writes it is the number of bytes appended, not the total
	// file size after the operation. A short write is reported as a handler
	// error, not via a lower BytesWritten.
	BytesWritten int `json:"bytes_written" jsonschema:"Number of bytes written."`
}

// Write is the fs.write tool entry point. It writes a string payload
// to a file in one of two modes — truncating (default) or appending —
// and returns a byte-accurate write count.
//
// When to use it: creating a brand-new file that does not yet exist,
// or capturing append-only output (log files, transcripts). For any
// modification to existing source files, prefer fs.patch instead: it
// is atomic, computes a unified-diff receipt, and supports a verify
// step that rolls every change back on build failure. Write has none
// of those guarantees — a half-completed write leaves the file in an
// indeterminate state.
//
// Edge cases worth knowing: write does not check that Path is within
// any sandbox; relative paths resolve against the host's cwd; missing
// parent directories fail unless CreateDirs is set; Mode is applied
// only at creation, not on subsequent overwrites; and an explicit
// empty Content with Append=false is a valid request to truncate the
// file to zero length.
var Write = tool.New[WriteInput, WriteOutput](
	"fs.write",
	"Writes content to a file. Prefer fs.patch for modifications to existing files — it provides atomicity, diff receipts, and rollback.",
	writeHandler,
)

// writeHandler implements fs.write. It optionally creates parent
// directories, parses the requested file mode, then either appends to
// or truncates the destination depending on the Append flag.
//
// The append path opens with O_APPEND|O_CREATE|O_WRONLY and writes
// directly, returning the byte count of the appended payload. The
// truncating path delegates to os.WriteFile, which atomically opens,
// truncates, writes, and closes. Note that os.WriteFile honours Mode
// only when the file is being created; an existing file keeps its
// on-disk permissions either way.
func writeHandler(_ context.Context, input WriteInput) (WriteOutput, error) {
	if input.Path == "" {
		return WriteOutput{}, fmt.Errorf("fs.write: path is required")
	}

	if input.CreateDirs {
		dir := filepath.Dir(input.Path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return WriteOutput{}, fmt.Errorf("fs.write: creating directories for %q: %w", input.Path, err)
			}
		}
	}

	// Parse mode
	mode := os.FileMode(0o644)
	if input.Mode != "" {
		parsed, err := strconv.ParseUint(input.Mode, 8, 32)
		if err != nil {
			return WriteOutput{}, fmt.Errorf("fs.write: invalid mode %q: %w", input.Mode, err)
		}
		mode = os.FileMode(parsed)
	}

	data := []byte(input.Content)

	if input.Append {
		f, err := os.OpenFile(input.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, mode)
		if err != nil {
			return WriteOutput{}, fmt.Errorf("fs.write: opening %q for append: %w", input.Path, err)
		}
		defer f.Close()
		n, err := f.Write(data)
		if err != nil {
			return WriteOutput{}, fmt.Errorf("fs.write: writing to %q: %w", input.Path, err)
		}
		return WriteOutput{Path: input.Path, BytesWritten: n}, nil
	}

	if err := os.WriteFile(input.Path, data, mode); err != nil {
		return WriteOutput{}, fmt.Errorf("fs.write: %w", err)
	}
	return WriteOutput{Path: input.Path, BytesWritten: len(data)}, nil
}
