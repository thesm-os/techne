// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package python_test

import (
	"testing"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang/python"
)

func TestAllToolsContract(t *testing.T) {
	tool.AssertTools(t, "lang.python", python.Tools)
}
