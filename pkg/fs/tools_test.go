// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs_test

import (
	"testing"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/fs"
)

func TestAllToolsContract(t *testing.T) {
	tool.AssertTools(t, "fs", fs.Tools)
}
