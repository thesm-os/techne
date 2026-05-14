// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ApplyEdits walks edits in order and applies each one as a literal
// find-and-replace against content. The first edit operates on the
// original content; each subsequent edit operates on the result of
// the previous one. This makes order significant — two edits whose
// OldStrings overlap in the original will not both apply.
//
// The function is the single point of truth for fs.patch's literal-
// edit semantics: every PatchEdit eventually flows through here,
// whether it came from the agent directly or was synthesised from a
// PatternEdit by ExpandPatternEdits.
//
// Returns an error and stops at the first edit whose OldString does
// not appear in the current content. The error message includes the
// first 80 characters of the missing string so the agent can adjust
// without a follow-up fs.read — a deliberate trade-off between
// error-message size and the cost of repeated tool calls.
//
// ReplaceAll on a PatchEdit toggles between strings.Replace (n=1)
// and strings.ReplaceAll. The single-replace default exists so that
// the agent's edits stay surgical: a typo or context-blind edit fails
// loudly rather than silently rewriting every matching occurrence.
func ApplyEdits(content string, edits []PatchEdit) (string, error) {
	for _, edit := range edits {
		if !strings.Contains(content, edit.OldString) {
			snippet := edit.OldString
			if len(snippet) > 80 {
				snippet = snippet[:80]
			}
			return "", fmt.Errorf("old_string not found (first 80 chars): %q", snippet)
		}
		if edit.ReplaceAll {
			content = strings.ReplaceAll(content, edit.OldString, edit.NewString)
		} else {
			content = strings.Replace(content, edit.OldString, edit.NewString, 1)
		}
	}
	return content, nil
}

// GenerateDiff produces a unified diff (the standard "--- a/..." /
// "+++ b/..." / "@@ -x,y +x,y @@" format with three context lines)
// between old and new byte content for the named file. It is the
// function that turns every fs.patch write into a receipt the agent
// can compare against its intent.
//
// oldBytes may be nil to indicate that the file did not exist before
// the operation, in which case the diff renders as an all-addition
// hunk — useful for file-creation rows in a PatchOutput. A binary
// input (detected by scanning for a NUL byte) short-circuits to a
// one-line "Binary file ... changed" notice rather than dumping
// undecipherable bytes.
//
// The underlying algorithm is an LCS-based edit script; cost is
// O(m*n) in lines, which is fine for source files but pathological
// for multi-megabyte logs. Hunks are emitted only for regions that
// contain changes plus the configured context window.
func GenerateDiff(filename string, old, new []byte) string {
	if isBinary(old) || isBinary(new) {
		return fmt.Sprintf("Binary file %s changed", filename)
	}

	oldLines := splitLines(old)
	newLines := splitLines(new)

	hunks := diffHunks(oldLines, newLines, 3)
	if len(hunks) == 0 {
		return ""
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "--- a/%s\n", filename)
	fmt.Fprintf(&b, "+++ b/%s\n", filename)
	for _, h := range hunks {
		b.WriteString(h)
	}
	return b.String()
}

// ExpandPatternEdits turns each PatternEdit into a list of equivalent
// FilePatch records by globbing the file system, scanning each match,
// and projecting the regex into the explicit PatchEdit form that the
// rest of the pipeline understands.
//
// The rationale is locality: agents writing bulk fixes (e.g. "replace
// 50 errcheck violations with one regex") send a single PatternEdit
// rather than fifty hand-written FilePatch entries, and the handler
// resolves the expansion deterministically so the result is still a
// literal, reviewable, rollback-able patch. The literal form is what
// fs.patch's snapshot/apply/verify machinery rolls forward, so the
// pattern path inherits exactly the same atomicity guarantees.
//
// Matches inside one file are processed in reverse order so that
// later byte offsets are not invalidated by earlier replacements; the
// emitted edits are then reversed back so callers see them in forward
// order. Replacement templates honour the literal escape sequences
// \n and \t the agent typically wants when constructing multi-line
// replacements over a JSON wire.
//
// Returns three values: the expanded literal patches, a slice of
// per-file failure results when something goes wrong (currently used
// only for invalid regex or glob errors), and a final error. On any
// error the patches slice is nil and the caller should treat the
// failure as a partial_failure for the enclosing patch operation.
func ExpandPatternEdits(patterns []PatternEdit) ([]FilePatch, []PatchFileResult, error) {
	var patches []FilePatch
	var results []PatchFileResult

	for _, pe := range patterns {
		re, err := regexp.Compile(pe.OldRegex)
		if err != nil {
			results = append(results, PatchFileResult{
				FilePath: pe.FileGlob,
				Status:   PatchFileFailure,
				Error:    fmt.Sprintf("invalid regex %q: %v", pe.OldRegex, err),
			})
			return nil, results, fmt.Errorf("invalid regex: %w", err)
		}

		files, err := GlobFiles(pe.FileGlob)
		if err != nil {
			results = append(results, PatchFileResult{
				FilePath: pe.FileGlob,
				Status:   PatchFileFailure,
				Error:    fmt.Sprintf("glob error: %v", err),
			})
			return nil, results, fmt.Errorf("glob error: %w", err)
		}

		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			content := string(data)

			matches := re.FindAllStringIndex(content, -1)
			if len(matches) == 0 {
				continue
			}

			limit := len(matches)
			if pe.MaxReplacements > 0 && pe.MaxReplacements < limit {
				limit = pe.MaxReplacements
			}

			// Build literal edits from regex matches (process in reverse
			// order so byte offsets don't shift).
			var edits []PatchEdit
			for i := limit - 1; i >= 0; i-- {
				matchText := content[matches[i][0]:matches[i][1]]
				replacement := re.ReplaceAllString(matchText, pe.NewTemplate)
				replacement = strings.ReplaceAll(replacement, `\n`, "\n")
				replacement = strings.ReplaceAll(replacement, `\t`, "\t")
				edits = append(edits, PatchEdit{
					OldString: matchText,
					NewString: replacement,
				})
			}

			// Reverse edits back to forward order for the literal patcher.
			for l, r := 0, len(edits)-1; l < r; l, r = l+1, r-1 {
				edits[l], edits[r] = edits[r], edits[l]
			}

			patches = append(patches, FilePatch{
				FilePath: file,
				Edits:    edits,
			})
		}
	}

	return patches, nil, nil
}

// GlobFiles returns the file paths matching pattern. It is a thin
// wrapper around filepath.Glob extended with one piece of
// doublestar-like behaviour: a pattern containing "**" walks the
// file system below the literal prefix and filters by the suffix's
// base-name match, so "core/**/*.go" finds every .go file beneath
// core/.
//
// This is not full doublestar support: the suffix is matched only
// against the file's base name (no nested separators), and only one
// "**" segment is honoured. The implementation is deliberately
// minimal because the only consumer is ExpandPatternEdits, which
// needs enough power to find files-under-a-tree without dragging in
// an external dependency.
//
// Returns the matched paths in walk order along with any walk error.
// Directories are excluded; only regular files surface.
func GlobFiles(pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(pattern)
	}

	parts := strings.SplitN(pattern, "**", 2)
	root := parts[0]
	if root == "" {
		root = "."
	}
	root = strings.TrimRight(root, "/")
	suffix := strings.TrimLeft(parts[1], "/")

	var matched []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		relPath := path
		if root != "." {
			relPath = strings.TrimPrefix(path, root+"/")
		}
		ok, matchErr := filepath.Match(suffix, filepath.Base(relPath))
		if matchErr != nil {
			return nil
		}
		if ok {
			matched = append(matched, path)
		}
		return nil
	})
	return matched, err
}

// AtomicWrite writes data to path using the temp-file-plus-rename
// idiom: the bytes go to a sibling ".patch-tmp-*" file in the same
// directory, the temp file is closed, then os.Rename swaps it into
// place. Renames within a single filesystem are atomic on every
// platform fs.patch targets, so a reader either sees the old content
// or the new content — never a partial write.
//
// This is the function that gives fs.patch its mid-operation crash
// safety. If the process is killed between writes, the on-disk state
// is still self-consistent because each file is either fully
// updated or untouched. Combined with the snapshot-before-apply
// design of patchHandler, that means agents can treat a multi-file
// patch as transactional even though Unix does not offer multi-file
// transactions natively.
//
// Failures while writing the temp file delete it before returning;
// failures during rename leave the original in place. Cross-
// filesystem renames will fail with EXDEV — a non-issue in practice
// because the temp file is created in the same directory as the
// target.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".patch-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// isBinary heuristically reports whether data looks like binary
// content by checking for the presence of a NUL byte. The check is
// intentionally crude: it costs O(n) and produces no false positives
// on valid UTF-8 / ASCII text, which is the only signal GenerateDiff
// needs to avoid producing line-by-line diffs of an opaque blob.
func isBinary(data []byte) bool {
	return slices.Contains(data, 0)
}

// splitLines splits data into lines for the diff algorithm,
// preserving the distinction between "the file ended with a newline"
// and "the file ended without one". The trailing empty string that
// strings.Split produces for newline-terminated input is dropped when
// the input ends in '\n' so subsequent line counts match the user's
// intuition; for non-terminated input the empty trailing element is
// retained as a true final line.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	raw := strings.Split(string(data), "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" && bytes.HasSuffix(data, []byte("\n")) {
		raw = raw[:len(raw)-1]
	}
	return raw
}

// lineEdit is one operation in a unified-diff edit script: a context
// line (' '), a removal ('-'), or an addition ('+'). It is the
// minimal internal representation produced by lcsEdits and consumed
// by diffHunks.
type lineEdit struct {
	// op is the diff operator for this line: ' ' for context, '-' for a
	// line present in old but not new, '+' for a line present in new but
	// not old.
	op byte // ' ', '-', '+'
	// val is the line text itself, without its trailing newline.
	val string
}

// diffHunks groups the line-by-line edit script produced by
// lcsEdits into unified-diff hunks of the form
// "@@ -oldStart,oldCount +newStart,newCount @@" with ctx lines of
// context around each cluster of changes. Adjacent change clusters
// that fall within 2*ctx lines of each other are merged into a
// single hunk because the trailing-context lines of one would
// otherwise overlap the leading-context lines of the next. Excess
// trailing context beyond ctx is trimmed before the hunk header is
// written so the counts stay accurate.
func diffHunks(oldLines, newLines []string, ctx int) []string {
	edits := lcsEdits(oldLines, newLines)

	var hunks []string
	n := len(edits)
	i := 0

	for i < n {
		if edits[i].op == ' ' {
			i++
			continue
		}

		hunkStart := max(i-ctx, 0)
		hunkOldLine := 1
		hunkNewLine := 1
		for k := range hunkStart {
			if edits[k].op != '+' {
				hunkOldLine++
			}
			if edits[k].op != '-' {
				hunkNewLine++
			}
		}

		hunkLines := []string{}
		oldCount := 0
		newCount := 0
		j := hunkStart
		lastChange := j
		for j < n {
			e := edits[j]
			switch e.op {
			case ' ':
				if j >= lastChange+ctx {
					goto hunkDone
				}
				hunkLines = append(hunkLines, " "+e.val)
				oldCount++
				newCount++
			case '-':
				hunkLines = append(hunkLines, "-"+e.val)
				oldCount++
				lastChange = j + 1
			case '+':
				hunkLines = append(hunkLines, "+"+e.val)
				newCount++
				lastChange = j + 1
			}
			j++
		}
	hunkDone:
		trailing := 0
		for _, v := range slices.Backward(hunkLines) {
			if v[0] != ' ' {
				break
			}
			trailing++
		}
		if trailing > ctx {
			excess := trailing - ctx
			hunkLines = hunkLines[:len(hunkLines)-excess]
			oldCount -= excess
			newCount -= excess
		}

		var hb bytes.Buffer
		fmt.Fprintf(&hb, "@@ -%d,%d +%d,%d @@\n", hunkOldLine, oldCount, hunkNewLine, newCount)
		for _, l := range hunkLines {
			hb.WriteString(l + "\n")
		}
		hunks = append(hunks, hb.String())

		i = j
	}

	return hunks
}

// lcsEdits computes a minimal-edit script between line sequences a
// and b using the classic longest-common-subsequence dynamic program.
// Returns an in-order slice of lineEdit values: context lines come
// from matches in the LCS, removals are lines in a not picked, and
// additions are lines in b not picked.
//
// Time and space complexity are both O(len(a)*len(b)). That is fine
// for source files but degrades on multi-megabyte inputs — those
// should go through a binary-aware tool anyway, which is why
// GenerateDiff bails out early on binary content. The script is
// emitted forward by reversing the back-tracked output.
func lcsEdits(a, b []string) []lineEdit {
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	var edits []lineEdit
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			edits = append(edits, lineEdit{' ', a[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			edits = append(edits, lineEdit{'+', b[j-1]})
			j--
		} else {
			edits = append(edits, lineEdit{'-', a[i-1]})
			i--
		}
	}

	for l, r := 0, len(edits)-1; l < r; l, r = l+1, r-1 {
		edits[l], edits[r] = edits[r], edits[l]
	}
	return edits
}
