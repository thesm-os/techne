// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"testing"

	"go.thesmos.sh/techne/internal/tool"
	golang "go.thesmos.sh/techne/pkg/lang/go"
)

func TestAllToolsContract(t *testing.T) {
	tool.AssertTools(t, "lang.go", golang.Tools)
}
