// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package typescript_test

import (
	"testing"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang/typescript"
)

func TestAllToolsContract(t *testing.T) {
	tool.AssertTools(t, "lang.typescript", typescript.Tools)
}
