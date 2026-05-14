// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package app wires together configuration, the tool registry, and the
// three presenters (CLI, MCP, TUI) behind a single cobra command tree.
//
// The package is the only place that imports every domain package for
// side-effects: each blank import below pulls in a package whose init()
// registers a [tool.ToolSet] with the central registry. Adding a new
// tool domain therefore requires exactly one new blank import here and
// the corresponding init() in the new package; no other code in app/
// or in the presenters has to change.
//
// The single exported function, [Run], constructs the root cobra
// command, attaches the 'serve' (MCP), 'tui', and per-tool leaf
// subcommands, then hands control to cobra. Configuration is loaded
// lazily inside each subcommand's RunE so a malformed config does not
// prevent the user from reading '--help'.
package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"go.thesmos.sh/techne/internal/config"
	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/internal/version"
	"go.thesmos.sh/techne/presenter/cli"
	"go.thesmos.sh/techne/presenter/mcp"
	"go.thesmos.sh/techne/presenter/tui"

	// Blank imports so every domain package's init() runs and registers its
	// ToolSet with the tool registry.
	_ "go.thesmos.sh/techne/pkg/fs"
	_ "go.thesmos.sh/techne/pkg/lang"
	_ "go.thesmos.sh/techne/pkg/lang/go"
	_ "go.thesmos.sh/techne/pkg/lang/javascript"
	_ "go.thesmos.sh/techne/pkg/lang/python"
	_ "go.thesmos.sh/techne/pkg/lang/rust"
	_ "go.thesmos.sh/techne/pkg/lang/typescript"
)

// buildTools uses the tool registry to instantiate all registered tools,
// wrapping each with validation.
func buildTools(cfgFile string) ([]tool.Tool, error) {
	v, err := config.LoadViper(cfgFile)
	if err != nil {
		return nil, err
	}

	var tools []tool.Tool
	for _, ts := range tool.Registry() {
		decode := func(dst any) error {
			return v.UnmarshalKey(ts.ConfigKey, dst)
		}
		tools = append(tools, ts.Factory(decode)...)
	}
	return tools, nil
}

// filterTools returns only tools whose dotted name starts with one of the
// comma-separated prefixes. For example, "fs,lang.go" matches "fs.read",
// "fs.write", "lang.go.explore", "lang.go.verify", but not "lang.rust.explore".
func filterTools(tools []tool.Tool, filter string) []tool.Tool {
	prefixes := strings.Split(filter, ",")
	for i := range prefixes {
		prefixes[i] = strings.TrimSpace(prefixes[i])
	}

	var filtered []tool.Tool
	for _, t := range tools {
		name := t.Name()
		for _, prefix := range prefixes {
			if prefix == "" {
				continue
			}
			// Match exact prefix: "fs" matches "fs.read" but not "fstool.x"
			if name == prefix || strings.HasPrefix(name, prefix+".") {
				filtered = append(filtered, t)
				break
			}
		}
	}
	return filtered
}

// Run is the single entry point for the techne binary. It wires the
// configuration loader, the tool registry, and all three presenters
// (CLI, MCP, TUI) behind a cobra command tree and dispatches to whichever
// subcommand the user invoked. It returns the error from
// cobra.Command.Execute so main can map it to a non-zero exit code.
//
// Command surface:
//
//   - The root command 'techne' carries a persistent '--config' flag
//     pointing at the YAML config file consumed by the config package.
//   - 'techne serve' launches the MCP presenter over stdio. Its '--tools'
//     flag accepts a comma-separated list of tool-name prefixes (e.g.
//     'fs,lang.go') and is applied via filterTools. Omitting the flag
//     exposes everything in the registry.
//   - 'techne tui' launches the bubbletea-based interactive UI.
//   - Every registered tool is also exposed as a leaf CLI subcommand by
//     [cli.Register]; the CLI presenter is registered eagerly at startup
//     so '--help' lists all tools without requiring a subcommand-specific
//     config load.
//
// Startup error handling: the CLI presenter's tool list is built
// eagerly so '--help' is informative. If buildTools fails at that
// point (e.g., malformed config), a warning is written to stderr and the
// CLI tree is built empty; the serve and tui subcommands still build
// tools lazily and will surface the same error if invoked. This keeps
// 'techne --help' usable even when config is broken.
func Run() error {
	var cfgFile string

	root := &cobra.Command{
		Use:     "techne",
		Short:   "Techne — a collection of developer tools exposed over MCP, TUI, and CLI",
		Version: version.Full(),
	}

	root.PersistentFlags().
		StringVar(&cfgFile, "config", "", "path to config file (default: ~/.config/techne/config.yaml)")

	// serve subcommand — MCP presenter
	var toolFilter string
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server",
		Long: `Start the MCP server over stdio.

Use --tools to expose only specific tool groups:
  techne serve --tools=fs,lang.go
  techne serve --tools=lang
  techne serve --tools=fs,lang.go,lang.rust

Without --tools, all registered tools are exposed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tools, err := buildTools(cfgFile)
			if err != nil {
				return fmt.Errorf("serve: failed to load config: %w", err)
			}
			if toolFilter != "" {
				tools = filterTools(tools, toolFilter)
			}
			server := mcp.NewServer(tools)
			return server.Run(cmd.Context())
		},
	}
	serveCmd.Flags().
		StringVar(&toolFilter, "tools", "", "comma-separated tool groups to expose (e.g. fs,lang.go). Without this flag, all tools are exposed.")

	// tui subcommand — TUI presenter
	tuiCmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive terminal UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			tools, err := buildTools(cfgFile)
			if err != nil {
				return fmt.Errorf("tui: failed to load config: %w", err)
			}
			app := tui.NewApp(tools)
			return app.Run(cmd.Context())
		},
	}

	// Register CLI tools under the root command.
	cliTools, err := buildTools(cfgFile)
	if err != nil {
		// Non-fatal at startup — CLI tools registration is best-effort.
		_, _ = fmt.Fprintf(os.Stderr, "warning: could not load config for CLI tools: %v\n", err)
		cliTools = nil
	}
	cli.Register(root, cliTools, tool.Groups())

	root.AddCommand(serveCmd, tuiCmd)

	return root.Execute()
}
