// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bytes"
	"sort"
)

// ReplaceBytes returns a new slice with src[start:end) substituted by
// replacement, leaving src unmodified. Out-of-range arguments (negative start,
// end past len(src), or start > end) return src unchanged so a buggy caller
// cannot corrupt the buffer mid-refactor.
//
// The slice is freshly allocated with capacity sized for the result, so the
// input src can be reused or discarded immediately after. Most actions in this
// package compose ReplaceBytes by sorting offsets descending and applying edits
// back-to-front; see [SortAndDedup].
func ReplaceBytes(src []byte, start, end int, replacement []byte) []byte {
	if start < 0 || end > len(src) || start > end {
		return src
	}
	res := make([]byte, 0, len(src)-(end-start)+len(replacement))
	res = append(res, src[:start]...)
	res = append(res, replacement...)
	res = append(res, src[end:]...)
	return res
}

// FindLineStart returns the byte offset of the start of the line containing
// offset (i.e. the position immediately after the previous '\n', or 0 if offset
// lies on the first line). The returned offset is suitable for slicing the
// indentation prefix off a statement, or for replacing a whole line via
// [ReplaceBytes].
func FindLineStart(src []byte, offset int) int {
	for i := offset - 1; i >= 0; i-- {
		if src[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// FindLineEnd returns the byte offset just past the newline terminating the
// line containing offset. If no newline is found between offset and the end of
// src, len(src) is returned. Paired with [FindLineStart] this gives the
// half-open [start, end) range that covers the whole line, including its
// trailing newline — the form expected by [ReplaceBytes].
func FindLineEnd(src []byte, offset int) int {
	for i := offset; i < len(src); i++ {
		if src[i] == '\n' {
			return i + 1
		}
	}
	return len(src)
}

// DetectIndent returns the leading whitespace (tabs and spaces) of the line
// starting at lineStart. Callers obtain lineStart from [FindLineStart]. Used by
// extraction-style actions to keep generated code aligned with its caller's
// indentation — preserves tabs vs spaces and counts verbatim so the result
// composes correctly with gofmt.
func DetectIndent(src []byte, lineStart int) string {
	var indent bytes.Buffer
	for i := lineStart; i < len(src); i++ {
		if src[i] != ' ' && src[i] != '\t' {
			break
		}
		indent.WriteByte(src[i])
	}
	return indent.String()
}

// SortAndDedup removes duplicate byte offsets and returns them sorted
// descending. This is the canonical ordering for byte-level patchers in this
// package: edits are applied back-to-front so earlier offsets remain valid as
// later splices shrink or grow the buffer.
//
// The input slice's backing array is reused for the output, so the caller must
// not retain the original slice after this returns.
func SortAndDedup(offsets []int) []int {
	seen := make(map[int]struct{}, len(offsets))
	deduped := offsets[:0]
	for _, o := range offsets {
		if _, dup := seen[o]; !dup {
			seen[o] = struct{}{}
			deduped = append(deduped, o)
		}
	}
	sort.Slice(deduped, func(i, j int) bool { return deduped[i] > deduped[j] })
	return deduped
}
