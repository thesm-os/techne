// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package config provides top-level configuration loading for techne.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"

	"go.thesmos.sh/techne/pkg/fs"
)

// MCPConfig captures user-tunable settings for the MCP presenter.
// It is loaded from the 'mcp' sub-tree of the techne YAML
// configuration file via [Load] and is consumed by the MCP server
// bootstrap in internal/app.
//
// Most fields default to the zero value when the config file is
// absent or omits the key, which matches techne's no-config-file
// startup story (the binary must remain usable without any user
// configuration). Add new fields with both a mapstructure tag for
// Viper decoding and a sensible zero-value semantics.
type MCPConfig struct {
	// Address is the network address the MCP server binds to when
	// running in network mode (for example, ':8080' or '127.0.0.1:0').
	// Leave empty to use the default stdio transport — techne's MCP
	// presenter currently runs over stdio only, so this field is
	// reserved for future network-mode support.
	Address string `mapstructure:"address"`
}

// Config is the top-level configuration tree for techne, unmarshalled
// from the YAML config file (default: ~/.config/techne/config.yaml)
// by [Load]. Each top-level field corresponds to one Viper sub-tree
// consumed by a specific subsystem.
//
// The domain tool packages (fs, lang.*) do not unmarshal into Config
// directly; instead they register a [tool.ToolSet] with a ConfigKey
// and receive their own typed sub-tree via the decoder callback at
// startup. This Config struct only carries settings used by code
// that already imports the config package (notably internal/app).
type Config struct {
	// MCP holds configuration for the MCP presenter (see [MCPConfig]).
	MCP MCPConfig `mapstructure:"mcp"`
	// FS holds configuration for the filesystem tools. It mirrors the
	// fs.Config struct so that filesystem tool settings can be edited
	// from the top-level config file without the domain package having
	// to import this package.
	FS fs.Config `mapstructure:"fs"`
}

// Load reads the techne YAML configuration file at cfgFile and
// returns the populated [Config].
//
// When cfgFile is empty, the default path is
// ~/.config/techne/config.yaml (resolved via os.UserHomeDir). A
// missing config file is intentionally not an error: techne must
// remain usable with no user configuration at all, and the returned
// Config in that case is the zero value. Read failures other than
// 'file not found' are surfaced verbatim, wrapped with 'config:'
// for attribution; unmarshalling failures are likewise wrapped.
//
// Load is the high-level entry point used when the caller wants a
// typed Config back. Callers that need to decode arbitrary sub-trees
// (notably the tool registry, which hands every ToolSet its own
// Viper-backed decoder) should call [LoadViper] instead and operate
// on the raw Viper instance.
//
// Load is not concurrency-safe with respect to the returned Viper
// instance internally constructed by [LoadViper], but the returned
// Config value is a plain struct and is safe to share.
func Load(cfgFile string) (Config, error) {
	v, err := LoadViper(cfgFile)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: unmarshal: %w", err)
	}
	return cfg, nil
}

// LoadViper reads the techne YAML configuration file at cfgFile and
// returns the raw [github.com/spf13/viper.Viper] instance so that
// callers can decode arbitrary sub-trees on demand. This is the
// lower-level counterpart to [Load], used by the tool registry to
// give each domain package access to only its own configuration
// section.
//
// When cfgFile is empty, the default path is
// ~/.config/techne/config.yaml. The function performs three steps:
//
//  1. Resolve cfgFile (or compute the default) and call
//     v.SetConfigFile.
//  2. Pin the format to YAML via v.SetConfigType so the file
//     extension is not load-bearing.
//  3. Read the file via v.ReadInConfig. A
//     viper.ConfigFileNotFoundError or os.IsNotExist error is
//     swallowed and a fresh, empty Viper is returned; any other
//     read error is returned wrapped with 'config:' for
//     attribution.
//
// Returning a usable Viper even on missing-file lets the application
// boot without any user configuration: every tool registry decoder
// still runs, just against an empty tree, and every Unmarshal call
// populates struct zero values.
//
// The returned Viper is fresh (not the package-level Viper
// singleton) and is not safe for concurrent mutation; treat it as
// owned by the caller.
func LoadViper(cfgFile string) (*viper.Viper, error) {
	v := viper.New()

	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return v, fmt.Errorf("config: cannot determine home directory: %w", err)
		}
		v.SetConfigFile(filepath.Join(home, ".config", "techne", "config.yaml"))
	}

	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		var configFileNotFoundError viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFoundError) {
			return v, nil
		}
		if os.IsNotExist(err) {
			return v, nil
		}
		return nil, fmt.Errorf("config: read error: %w", err)
	}

	return v, nil
}
