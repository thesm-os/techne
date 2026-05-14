// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package rust

import (
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// findRustFiles walks dir recursively and returns all .rs file paths.
func findRustFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if info.IsDir() {
			// Skip hidden directories and common non-source dirs.
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "target" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".rs") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// extractRustDoc scans backwards from node looking for consecutive /// line_comment
// or /** */ block_comment sibling nodes and assembles the doc comment text.
func extractRustDoc(src []byte, node *sitter.Node) string {
	parent := node.Parent()
	if parent == nil {
		return ""
	}

	// Find the index of this node among its siblings.
	nodeIdx := -1
	for i := 0; i < int(parent.ChildCount()); i++ {
		if parent.Child(i) == node {
			nodeIdx = i
			break
		}
	}
	if nodeIdx <= 0 {
		return ""
	}

	// Collect doc comment lines scanning backwards.
	var docLines []string
	for i := nodeIdx - 1; i >= 0; i-- {
		sibling := parent.Child(i)
		t := sibling.Type()
		if t != "line_comment" && t != "block_comment" {
			break
		}
		text := string(src[sibling.StartByte():sibling.EndByte()])
		if t == "line_comment" {
			// Only include outer doc comments (/// ...).
			after, ok := strings.CutPrefix(text, "///")
			if !ok {
				// Non-doc comment — stop collecting.
				break
			}
			docLines = append([]string{strings.TrimSpace(after)}, docLines...)
		} else if t == "block_comment" {
			// Only include /** ... */ block docs.
			after, ok := strings.CutPrefix(text, "/**")
			if !ok {
				break
			}
			inner := strings.TrimSuffix(after, "*/")
			docLines = append([]string{strings.TrimSpace(inner)}, docLines...)
		}
	}

	return strings.Join(docLines, "\n")
}

// isPublic returns true if the node starts with the pub keyword by checking
// for a "visibility_modifier" or "pub" child node.
func isPublic(src []byte, node *sitter.Node) bool {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		t := child.Type()
		if t == "visibility_modifier" {
			text := string(src[child.StartByte():child.EndByte()])
			return strings.HasPrefix(text, "pub")
		}
		// Some grammars expose pub directly as a child.
		if t == "pub" {
			return true
		}
	}
	return false
}

// nodeText returns the source text of a node.
func nodeText(src []byte, node *sitter.Node) string {
	if node == nil {
		return ""
	}
	start := node.StartByte()
	end := node.EndByte()
	if start > end || end > uint32(len(src)) {
		return ""
	}
	return string(src[start:end])
}

// childByType returns the first child of node with the given type, or nil.
func childByType(node *sitter.Node, typ string) *sitter.Node {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c.Type() == typ {
			return c
		}
	}
	return nil
}

// namedChildByType returns the first named child of node with the given type, or nil.
func namedChildByType(node *sitter.Node, typ string) *sitter.Node {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		c := node.NamedChild(i)
		if c.Type() == typ {
			return c
		}
	}
	return nil
}
