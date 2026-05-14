// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package rust_test

import (
	"testing"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang/rust"
)

func TestAllToolsContract(t *testing.T) {
	tool.AssertTools(t, "lang.rust", rust.Tools)
}
