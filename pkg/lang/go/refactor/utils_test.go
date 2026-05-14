// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// TestReplaceBytes covers the byte-splice primitive across example,
// boundary, and property-based cases. ReplaceBytes is the foundation of
// every refactor's edit pipeline; an off-by-one here corrupts every
// downstream action.
func TestReplaceBytes(t *testing.T) {
	t.Run("examples", func(t *testing.T) {
		cases := []struct {
			name        string
			src         string
			start, end  int
			replacement string
			want        string
		}{
			{"basic mid-string replace", "hello world", 6, 11, "Go", "hello Go"},
			{"replace at start", "hello world", 0, 5, "goodbye", "goodbye world"},
			{"replace at end", "hello world", 6, 11, "Go", "hello Go"},
			{"empty replacement deletes", "hello world", 5, 11, "", "hello"},
			{"larger replacement extends", "abc", 1, 2, "XXXXX", "aXXXXXc"},
			{"smaller replacement shrinks", "abcdefgh", 1, 6, "X", "aXgh"},
			{"start > end is no-op", "hello", 3, 1, "X", "hello"},
			{"negative start is no-op", "hello", -1, 3, "X", "hello"},
			{"end beyond length is no-op", "hello", 0, 6, "X", "hello"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := ReplaceBytes([]byte(tc.src), tc.start, tc.end, []byte(tc.replacement))
				if string(got) != tc.want {
					t.Errorf("ReplaceBytes(%q, %d, %d, %q) = %q; want %q",
						tc.src, tc.start, tc.end, tc.replacement, got, tc.want)
				}
			})
		}
	})

	t.Run("property: round-trip restores original", func(t *testing.T) {
		// Applying a replace then reversing it (replace [start, start+len(repl))
		// with the original slice) must yield the original bytes.
		rng := rand.New(rand.NewPCG(1, 2))
		for range 200 {
			size := 1 + rng.IntN(64)
			src := make([]byte, size)
			for j := range src {
				src[j] = byte('a' + rng.IntN(26))
			}
			start := rng.IntN(size + 1)
			end := start + rng.IntN(size+1-start)
			replacement := make([]byte, rng.IntN(8))
			for j := range replacement {
				replacement[j] = byte('A' + rng.IntN(26))
			}
			original := append([]byte(nil), src[start:end]...)
			modified := ReplaceBytes(src, start, end, replacement)
			restored := ReplaceBytes(modified, start, start+len(replacement), original)
			if !bytes.Equal(restored, src) {
				t.Errorf("round-trip failed:\n  src=%q\n  start=%d end=%d repl=%q\n  modified=%q\n  restored=%q",
					src, start, end, replacement, modified, restored)
			}
		}
	})

	t.Run("property: length invariant", func(t *testing.T) {
		// len(out) == len(src) - (end-start) + len(replacement) for every
		// valid input. Catches any future off-by-one in the size calculation.
		rng := rand.New(rand.NewPCG(3, 4))
		for range 200 {
			size := 1 + rng.IntN(128)
			src := bytes.Repeat([]byte{'x'}, size)
			start := rng.IntN(size + 1)
			end := start + rng.IntN(size+1-start)
			repl := bytes.Repeat([]byte{'y'}, rng.IntN(16))
			got := ReplaceBytes(src, start, end, repl)
			want := len(src) - (end - start) + len(repl)
			if len(got) != want {
				t.Errorf("len mismatch: src=%d start=%d end=%d repl=%d -> got=%d want=%d",
					len(src), start, end, len(repl), len(got), want)
			}
		}
	})

	t.Run("property: invalid ranges are no-ops without panic", func(t *testing.T) {
		src := []byte("hello world")
		invalid := []struct {
			name       string
			start, end int
		}{
			{"negative start", -1, 5},
			{"end past length", 0, 100},
			{"start > end", 5, 2},
			{"both negative", -3, -1},
		}
		for _, tc := range invalid {
			t.Run(tc.name, func(t *testing.T) {
				got := ReplaceBytes(src, tc.start, tc.end, []byte("X"))
				if !bytes.Equal(got, src) {
					t.Errorf("expected src unchanged on invalid range; got %q", got)
				}
			})
		}
	})
}

// TestFindLineStart walks backwards from an offset to find the start of
// its containing line.
func TestFindLineStart(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		offset int
		want   int
	}{
		{"start of file", "first line\nsecond line\n", 4, 0},
		{"middle line", "first\nsecond\nthird\n", 9, 6},
		{"at newline returns previous line start", "first\nsecond\n", 5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindLineStart([]byte(tc.src), tc.offset); got != tc.want {
				t.Errorf("FindLineStart(%q, %d) = %d; want %d", tc.src, tc.offset, got, tc.want)
			}
		})
	}
}

// TestFindLineEnd walks forward from an offset to the position after
// the next newline (or len(src) if none).
func TestFindLineEnd(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		offset int
		want   int
	}{
		{"end of file when no newline", "no newline here", 0, len("no newline here")},
		{"middle line", "first\nsecond\nthird\n", 6, 13},
		{"at newline returns position after", "first\nsecond\n", 5, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindLineEnd([]byte(tc.src), tc.offset); got != tc.want {
				t.Errorf("FindLineEnd(%q, %d) = %d; want %d", tc.src, tc.offset, got, tc.want)
			}
		})
	}
}

// TestDetectIndent reads the whitespace prefix of a line.
func TestDetectIndent(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		lineStart int
		want      string
	}{
		{"tab indent", "\tfunc foo() {}", 0, "\t"},
		{"4 spaces indent", "    func foo() {}", 0, "    "},
		{"no indent", "func foo() {}", 0, ""},
		{"mid-file double tab", "func outer() {\n\t\tinner()\n}", 15, "\t\t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectIndent([]byte(tc.src), tc.lineStart); got != tc.want {
				t.Errorf("DetectIndent(%q, %d) = %q; want %q", tc.src, tc.lineStart, got, tc.want)
			}
		})
	}
}

// FuzzReplaceBytes catches any future change to ReplaceBytes that
// breaks the contract len(out) == len(src) - (end-start) + len(repl)
// for valid ranges, or the no-op contract for invalid ranges. The
// example-based and randomized property tests live in TestReplaceBytes
// above; this fuzz target adds adversarial coverage against the same
// contract. Go requires Fuzz* functions to be top-level, so this can't
// be a subtest.
//
// Run with: go test -fuzz=FuzzReplaceBytes ./pkg/lang/go/refactor/ -fuzztime=10s
func FuzzReplaceBytes(f *testing.F) {
	f.Add([]byte("hello"), 0, 5, []byte("x"))
	f.Add([]byte(""), 0, 0, []byte("y"))
	f.Add([]byte("abc"), 1, 2, []byte(""))
	f.Fuzz(func(t *testing.T, src []byte, start, end int, repl []byte) {
		got := ReplaceBytes(src, start, end, repl)
		if start < 0 || end > len(src) || start > end {
			if !bytes.Equal(got, src) {
				t.Errorf("invalid range must be no-op: src=%q start=%d end=%d -> %q",
					src, start, end, got)
			}
			return
		}
		want := len(src) - (end - start) + len(repl)
		if len(got) != want {
			t.Errorf("length invariant: src=%d start=%d end=%d repl=%d -> got=%d want=%d",
				len(src), start, end, len(repl), len(got), want)
		}
	})
}

// TestApplyEdits exercises the multi-edit splicer used by every action
// that produces a list of byte-range edits. The contract: applying N
// non-overlapping edits in any order yields the same result.
func TestApplyEdits(t *testing.T) {
	src := []byte("the quick brown fox jumps over the lazy dog")
	edits := []fileEdit{
		{start: 4, end: 9, replacement: []byte("SLOW")},   // "quick" → "SLOW"
		{start: 16, end: 19, replacement: []byte("CAT")},  // "fox" → "CAT"
		{start: 35, end: 39, replacement: []byte("LION")}, // "lazy" → "LION"
		{start: 0, end: 3, replacement: []byte("a")},      // "the" → "a"
	}
	expected := []byte("a SLOW brown CAT jumps over the LION dog")

	t.Run("input order", func(t *testing.T) {
		got := applyEdits(src, edits)
		if !bytes.Equal(got, expected) {
			t.Errorf("got=%q want=%q", got, expected)
		}
	})

	t.Run("property: order-independent on non-overlapping edits", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(5, 6))
		for i := range 50 {
			shuffled := make([]fileEdit, len(edits))
			copy(shuffled, edits)
			rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			got := applyEdits(src, shuffled)
			if !bytes.Equal(got, expected) {
				t.Errorf("order-dependent on shuffle %d:\n  shuffled=%+v\n  got=%q\n  want=%q",
					i, shuffled, got, expected)
			}
		}
	})
}

// TestSortAndDedup produces strictly-descending unique offsets, which
// is the precondition for back-to-front edit application.
func TestSortAndDedup(t *testing.T) {
	t.Run("examples", func(t *testing.T) {
		cases := []struct {
			name string
			in   []int
			want []int
		}{
			{"basic", []int{1, 3, 2}, []int{3, 2, 1}},
			{"with duplicates", []int{5, 3, 5, 1, 3}, []int{5, 3, 1}},
			{"empty", []int{}, []int{}},
			{"single element", []int{42}, []int{42}},
			{"already descending", []int{10, 7, 3}, []int{10, 7, 3}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := SortAndDedup(append([]int(nil), tc.in...))
				if len(got) != len(tc.want) {
					t.Fatalf("len mismatch: got %v, want %v", got, tc.want)
				}
				for i := range tc.want {
					if got[i] != tc.want[i] {
						t.Errorf("index %d: got %d, want %d (full got=%v)", i, got[i], tc.want[i], got)
					}
				}
			})
		}
	})

	t.Run("property: strictly descending and unique", func(t *testing.T) {
		// Output must be strictly descending with no duplicates, and every
		// unique input must appear exactly once.
		rng := rand.New(rand.NewPCG(7, 8))
		for range 100 {
			size := rng.IntN(50)
			offsets := make([]int, size)
			seen := make(map[int]bool)
			for j := range offsets {
				offsets[j] = rng.IntN(20) // small range → many dupes
				seen[offsets[j]] = true
			}
			out := SortAndDedup(append([]int(nil), offsets...))

			if len(out) != len(seen) {
				t.Errorf("dedupe count mismatch: input=%v unique=%d got=%d",
					offsets, len(seen), len(out))
			}
			for j := 1; j < len(out); j++ {
				if out[j-1] <= out[j] {
					t.Errorf("not strictly descending at idx %d: %v", j, out)
				}
			}
			gotSet := make(map[int]bool, len(out))
			for _, v := range out {
				gotSet[v] = true
			}
			for v := range seen {
				if !gotSet[v] {
					t.Errorf("input value %d missing from output %v", v, out)
				}
			}
		}
	})
}
