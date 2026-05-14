// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package fs provides filesystem operation tools (read, write, list, find,
// grep, patch, etc.) exposed via the tool framework.
package fs

// Config holds package-level limits for the fs.* tool family. It is
// decoded from the host application's configuration during init via
// [tool.RegisterTools] and applies uniformly to every tool in the package
// (read, write, list, find, grep, patch, replace, copy, move, delete).
//
// The zero value is valid: when both fields are zero the tools fall back
// to their built-in defaults, meaning the agent can apply unbounded reads
// and result sets. Production deployments should set non-zero values to
// bound tool output and prevent runaway resource use under adversarial or
// looping agent behaviour.
//
// Fields use mapstructure tags so values can be loaded from any backend
// supported by the host config layer (YAML, TOML, env, CLI flags).
type Config struct {
	// MaxFileSizeMB is the upper bound, in megabytes, on any single file
	// that the fs.* tools will read into memory or write to disk. Files
	// larger than this limit are rejected before any I/O occurs to protect
	// the host from out-of-memory conditions when an agent reads a large
	// binary, log, or generated artifact by mistake. A value of zero
	// disables the check.
	MaxFileSizeMB int `mapstructure:"max_file_size_mb"`
	// MaxResults is the default cap applied to listing and searching tools
	// (fs.list, fs.find, fs.grep) when the caller does not supply its own
	// MaxResults / MaxMatches input field. The per-call value, when
	// non-zero, always wins. A zero Config.MaxResults means there is no
	// implicit cap and tools return every match they find — fine for
	// interactive use, dangerous for unattended agents traversing large
	// trees.
	MaxResults int `mapstructure:"max_results"`
}
