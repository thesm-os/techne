// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"go.thesmos.sh/techne/pkg/lang"
)

// escapeLineRe matches the canonical "file.go:line:col: message" shape
// emitted by `go build -gcflags=-m -m` on stderr. Capture groups: file
// path, line number, message body (column is consumed but not
// captured — the consumer only needs line-precision for the report).
var escapeLineRe = regexp.MustCompile(`^(.+\.go):(\d+):\d+:\s+(.+)$`)

// escapeVarRe extracts the variable name that escapes from the start of
// an escape-analysis message like "x escapes to heap". Captures the
// first word so messages that lead with a verb ("moved to heap",
// "leaking param") are not matched here — they have their own
// extraction path.
var escapeVarRe = regexp.MustCompile(`^(\w+)\s+escapes`)

// RunEscapeAnalysis runs `go build -gcflags="-m -m"` on the given
// package and returns parsed escape info. If symbolFilter is non-empty,
// only escapes whose source file path or message mentions that symbol
// are included — useful for honing in on a single function's heap
// allocation behaviour.
//
// The build command's exit status is intentionally ignored: escape
// analysis is emitted on stderr by the compiler frontend and is
// available even when the build proper fails, so a build break does
// not prevent the agent from getting allocation info.
//
// Spawns a subprocess. Does NOT honor a context — callers that need
// cancellation must impose their own timeout via a deferred Process.Kill.
func RunEscapeAnalysis(pkg, symbolFilter string) ([]lang.EscapeInfo, error) {
	dir := "."
	pkgArg := pkg
	if strings.HasPrefix(pkg, "/") ||
		strings.HasPrefix(pkg, "./") ||
		strings.HasPrefix(pkg, "../") {
		dir = pkg
		pkgArg = "."
	}

	cmd := exec.Command("go", "build", "-gcflags=-m -m", pkgArg)
	cmd.Dir = dir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // escape analysis works even if build has issues

	return ParseEscapeOutput(stderr.String(), symbolFilter), nil
}

// ParseEscapeOutput parses the stderr output of
// `go build -gcflags="-m -m"` and returns one lang.EscapeInfo per
// escape event matching symbolFilter (no filter when empty).
//
// For each matching line it normalizes the cause ("passed to interface
// boundary", "captured by closure", etc.) and pairs it with a
// performance-oriented hint. When the compiler reports a literal
// escape without naming a variable (e.g. "&Foo{} escapes"), the
// parser opens the source line and infers the variable or expression
// that escaped — the agent should not have to guess what `&Foo{}
// escapes" means.
func ParseEscapeOutput(output, symbolFilter string) []lang.EscapeInfo {
	var escapes []lang.EscapeInfo

	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		m := escapeLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		filename := m[1]
		lineNum, _ := strconv.Atoi(m[2])
		msg := strings.TrimSpace(m[3])

		if symbolFilter != "" {
			if !strings.Contains(filename, symbolFilter) &&
				!strings.Contains(msg, symbolFilter) {
				continue
			}
		}

		if !isEscapeMessage(msg) {
			continue
		}

		variable := extractVariable(msg)

		// When the compiler reports a literal escape (e.g. struct literal,
		// slice literal) without naming the variable, read the source line
		// to extract the assigned variable or the expression itself.
		if variable == "" {
			variable = extractVariableFromSource(filename, lineNum)
		}

		escapes = append(escapes, lang.EscapeInfo{
			Line:     lineNum,
			Variable: variable,
			Cause:    normalizeCause(msg),
			Hint:     generateHint(normalizeCause(msg)),
		})
	}

	return escapes
}

// extractVariableFromSource reads the given source file line and
// extracts the variable name or expression that's likely escaping,
// handling three canonical shapes:
//
//	x := &Foo{}      → "x"
//	return &Foo{}    → "&Foo{} (return)"
//	y = make([]int)  → "y"
//
// For multi-LHS assignments ("x, y := f()") only the first identifier
// is returned. The (return) suffix is added so the agent can
// distinguish "x escapes because it's returned" from "x escapes because
// it's stored" without further source reading.
func extractVariableFromSource(filename string, lineNum int) string {
	data, err := os.ReadFile(filename)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if lineNum < 1 || lineNum > len(lines) {
		return ""
	}
	line := strings.TrimSpace(lines[lineNum-1])

	// Assignment: "x := expr" or "x = expr"
	if idx := strings.Index(line, ":="); idx > 0 {
		lhs := strings.TrimSpace(line[:idx])
		// Could be "x, y := ..." — take first identifier
		parts := strings.Split(lhs, ",")
		return strings.TrimSpace(parts[0])
	}
	if idx := strings.Index(line, " = "); idx > 0 {
		lhs := strings.TrimSpace(line[:idx])
		parts := strings.Split(lhs, ",")
		return strings.TrimSpace(parts[0])
	}

	// Return statement: "return &Foo{}"
	if strings.HasPrefix(line, "return ") {
		expr := strings.TrimSpace(strings.TrimPrefix(line, "return"))
		if len(expr) > 40 {
			expr = expr[:40] + "..."
		}
		return expr + " (return)"
	}

	// Fallback: return the trimmed line itself, capped
	if len(line) > 50 {
		line = line[:50] + "..."
	}
	return line
}

// isEscapeMessage reports whether a compiler diagnostic message is an
// escape-analysis event we want to surface. The keyword check is case
// insensitive and broad on purpose — the compiler emits many variants
// ("escapes to heap", "moved to heap", "leaking param") and we want
// them all.
func isEscapeMessage(msg string) bool {
	keywords := []string{
		"escapes to heap",
		"escape",
		"moved to heap",
		"leaking param",
		"too large for stack",
	}
	lower := strings.ToLower(msg)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// extractVariable returns the variable name associated with an escape
// message. Handles two compiler-message shapes: "<name> escapes…"
// via escapeVarRe, and "moved to heap: <name>" via a string-cut
// fallback. Returns the empty string when neither pattern matches; the
// caller then falls back to extractVariableFromSource.
func extractVariable(msg string) string {
	m := escapeVarRe.FindStringSubmatch(msg)
	if len(m) >= 2 {
		return m[1]
	}
	if _, after, ok := strings.Cut(msg, "moved to heap:"); ok {
		rest := strings.TrimSpace(after)
		parts := strings.Fields(rest)
		if len(parts) > 0 {
			return parts[0]
		}
	}
	return ""
}

// normalizeCause folds the variety of escape-analysis messages into
// a small set of canonical causes that downstream tooling can switch
// on. The original verbose message survives as the fallback case so a
// future compiler version that emits something new is not silently
// dropped — the agent sees the raw text and can flag it.
func normalizeCause(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "passed to interface"):
		return "passed to interface boundary"
	case strings.Contains(lower, "captured by closure") || strings.Contains(lower, "reference to"):
		return "captured by closure"
	case strings.Contains(lower, "too large for stack"):
		return "too large for stack"
	case strings.Contains(lower, "leaking param"):
		return "parameter leaks to heap"
	case strings.Contains(lower, "moved to heap"):
		return "moved to heap"
	case strings.Contains(lower, "escapes to heap"):
		return "escapes to heap"
	default:
		return msg
	}
}

// generateHint maps a normalized escape cause (from normalizeCause)
// to a one-line performance suggestion that an agent can act on: prefer
// concrete types over interfaces, pass by parameter instead of capture,
// pre-allocate or pool, etc. The fallback hint is intentionally generic
// so unrecognized causes still get a directional nudge.
func generateHint(cause string) string {
	switch cause {
	case "passed to interface boundary":
		return "Consider using a concrete type instead of an interface to avoid allocation"
	case "captured by closure":
		return "Consider passing the variable as a parameter instead of capturing it in the closure"
	case "too large for stack":
		return "Consider using a pointer or reducing the struct size to fit on the stack"
	case "parameter leaks to heap":
		return "Review whether the parameter needs to be returned or stored; consider using value semantics"
	case "moved to heap", "escapes to heap":
		return "Consider pre-allocating or using sync.Pool to reduce GC pressure"
	default:
		return "Review the escape cause and consider restructuring to avoid heap allocation"
	}
}
