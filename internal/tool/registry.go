// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package tool

import "sync"

// ToolSet bundles a group of related tools with the Viper key path
// that carries their domain-specific configuration. Each domain
// package (fs, lang.go, lang.rust, ...) registers exactly one
// ToolSet from its init function via [RegisterTools]; the
// application entry point iterates [Registry] at startup and asks
// every ToolSet to instantiate its concrete [Tool] values.
//
// The indirection through Factory keeps the registry decoupled from
// config parsing: domain packages do not import Viper directly and
// do not know the full Config shape. They simply receive a decode
// function scoped to their ConfigKey and unmarshal whatever struct
// they need. This lets the application boot without a config file
// (decode is still called, just against a zero Viper tree) and lets
// tests construct synthetic decoders without touching the real
// config loader.
type ToolSet struct {
	// ConfigKey is the Viper key path under which this group's
	// configuration is unmarshalled, written in dotted form
	// (for example, 'fs', 'lang.go', 'lang.rust'). The application
	// calls v.UnmarshalKey(ConfigKey, &cfg) when invoking Factory.
	ConfigKey string

	// Factory builds the concrete [Tool] values for this group when the
	// application starts. The supplied decode function, when called
	// with a pointer to a typed config struct, unmarshals the sub-tree
	// at ConfigKey into that struct; missing keys are not errors.
	// Factory may capture the decoded config in closures held by the
	// returned tools.
	Factory func(decode func(any) error) []Tool
}

// Group is the descriptive metadata for a logical tool grouping
// (for example, 'fs' or 'lang.go'). Groups are surfaced by the CLI
// in --help output and by the TUI in its catalogue; they do not
// participate in tool resolution at request time — tool routing is
// strictly by the dotted Name of each [Tool].
//
// Groups are registered via [RegisterGroup] from domain package
// init functions, independently of the corresponding [ToolSet], so
// a group can declare aliases and example invocations without
// forcing a circular dependency between the registry and the
// presenter packages that render them.
type Group struct {
	// Path is the canonical dotted identifier for the group, matching
	// the prefix of every tool name within it (for example, 'lang.go'
	// for 'lang.go.rename', 'lang.go.verify', and so on).
	Path string
	// Description is the human-readable summary of what the group
	// provides, surfaced verbatim by the CLI and TUI in tool listings.
	Description string
	// Aliases are alternative names that resolve to the same group,
	// useful for shorter CLI invocations or backward compatibility
	// after a rename. The aliases must not collide with another
	// registered Group.Path.
	Aliases []string
	// Examples are sample invocations or usage snippets shown in help
	// output to demonstrate the group's intended use. Each entry is a
	// free-form string rendered as-is by the presenter.
	Examples []string
}

var (
	registryMu sync.RWMutex
	registry   []ToolSet
	groups     []Group
)

// Register appends ts to the package-level [ToolSet] registry.
//
// Typically invoked from a domain package's init function so that a
// blank import of the package (see internal/app for the canonical
// import list) is sufficient to surface its tools at startup.
//
// Thread-safe: the underlying slice is guarded by a sync.RWMutex.
// The registry has no notion of identity — calling Register twice
// with the same ConfigKey is permitted and results in both
// ToolSets running their Factory at startup. Most callers should
// prefer [RegisterTools], which constructs the ToolSet for them.
func Register(ts ToolSet) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, ts)
}

// Registry returns a defensive copy of every registered [ToolSet]
// in the order they were registered. Callers may iterate, sort, or
// filter the result freely without holding the registry mutex.
//
// Used at application startup to build the live tool list: the app
// calls Registry, invokes each Factory with a Viper-backed decoder,
// and collates the resulting [Tool] values. Safe for concurrent
// use, but the cost of the copy means it is not suited to hot
// paths.
func Registry() []ToolSet {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]ToolSet, len(registry))
	copy(out, registry)
	return out
}

// RegisterGroup appends g to the package-level [Group] registry,
// making it visible to presenters via [Groups].
//
// Intended to be called from domain package init functions in
// tandem with [RegisterTools]. Thread-safe via the shared registry
// mutex. The registry is intentionally permissive: duplicate Paths
// are accepted (last-write-wins is not enforced), and presenters
// should treat the slice as best-effort metadata.
func RegisterGroup(g Group) {
	registryMu.Lock()
	defer registryMu.Unlock()
	groups = append(groups, g)
}

// Groups returns a defensive copy of every registered [Group] in
// registration order. Used by the CLI to render tool catalogues
// and by the TUI to populate group panes. The copy makes the
// result safe to retain and mutate without affecting future
// registrations or concurrent readers.
func Groups() []Group {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Group, len(groups))
	copy(out, groups)
	return out
}

// RegisterTools is the ergonomic wrapper around [Register] used by
// almost every domain package. It constructs a [ToolSet] with
// ConfigKey set to groupPath and Factory set to the supplied
// callback, then registers it.
//
// Use this from an init function alongside [RegisterGroup]: the
// groupPath argument should match Group.Path so that the Viper
// sub-tree the Factory decodes is co-located with the group the
// tools belong to. Thread-safe; idempotency is the caller's
// responsibility (calling RegisterTools twice with the same
// groupPath registers both Factories).
func RegisterTools(groupPath string, factory func(decode func(any) error) []Tool) {
	Register(ToolSet{
		ConfigKey: groupPath,
		Factory:   factory,
	})
}
