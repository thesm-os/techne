// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package cli provides the CLI presenter for techne.
//
// The presenter materialises a cobra command tree from the flat list of
// [tool.Tool] and [tool.Group] values supplied at startup. Every leaf
// command, every flag, every default, help string, enum-value shell
// completion and required-ness check is derived from the tool's
// Name(), Description() and InputSchema() — no per-tool boilerplate
// needs to be written.
//
// Flag mapping rules in summary:
//
//   - JSON schema 'snake_case' is normalised to '--kebab-case' on input.
//   - Nested object properties are flattened with dashed prefixes
//     ('auth.user' becomes '--auth-user').
//   - Arrays of scalars are exposed as comma-separated StringSlice flags.
//   - Arrays of objects (which cobra cannot encode) fall back to the
//     universal '--input' flag, which reads a complete JSON payload from
//     a file path or '-' for stdin.
//
// The single exported entry point is [Register], invoked from the
// internal/app package during startup.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"go.thesmos.sh/techne/internal/tool"
)

// flagInfo tracks the mapping between a cobra flag and its JSON path.
type flagInfo struct {
	jsonPath []string // e.g. ["auth", "user"] for nested, ["path"] for flat
	schema   *jsonschema.Schema
	required bool
}

// Register builds a hierarchical cobra command tree from a flat list of
// tools and groups, attaching the resulting subcommand tree to root. It is
// the entire integration contract between the tool registry and the CLI
// presenter — no per-tool boilerplate is required; each leaf command is
// derived from the tool's Name(), Description() and InputSchema()
// methods on the [tool.Tool] interface.
//
// Command-tree construction:
//
//   - A tool whose dotted Name() is, e.g., 'lang.go.verify' becomes a leaf
//     command 'verify' nested under groups 'lang' and 'go'.
//   - Intermediate group nodes are materialised on demand via an internal
//     findOrCreate cache so two tools sharing a prefix reuse the same
//     parent. Group metadata (Description, Aliases) is overlaid from the
//     groups slice when the path matches — callers can register groups
//     in any order relative to the tools they contain.
//   - Each leaf's input schema is walked recursively (see addFlags),
//     flattening nested objects into dotted/dashed flag names so a JSON
//     property like 'auth.user' becomes '--auth-user'.
//
// Flag normalization: JSON schema properties use snake_case
// ('include_private') while cobra flags conventionally use kebab-case
// ('--include-private'). Register installs a global normalizer on root
// that maps any input flag containing underscores to its kebab-case form,
// so agents reading the JSON schema can pass either form interchangeably.
//
// Universal escape hatch: every leaf gets an '--input' flag accepting a
// file path or '-' for stdin. When set, the entire JSON input is read
// verbatim, bypassing individual flags. This is the workaround for input
// shapes cobra cannot express directly — most notably arrays of objects
// like 'patches[]' or 'add_params[]' — and is also the safest way for
// agents to drive tools with deeply-nested input.
//
// Register is intended to be called once at startup. Calling it twice on
// the same root with overlapping tool names will attach duplicate
// subcommands and cobra will report an error at first invocation.
func Register(root *cobra.Command, tools []tool.Tool, groups []tool.Group) {
	if root.PersistentFlags().Lookup("output") == nil {
		root.PersistentFlags().String("output", "json", "Output format: json or text")
	}

	root.SetGlobalNormalizationFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if strings.Contains(name, "_") {
			return pflag.NormalizedName(strings.ReplaceAll(name, "_", "-"))
		}
		return pflag.NormalizedName(name)
	})

	nodeMap := map[string]*cobra.Command{}

	var findOrCreate func(parts []string) *cobra.Command
	findOrCreate = func(parts []string) *cobra.Command {
		key := strings.Join(parts, ".")
		if cmd, ok := nodeMap[key]; ok {
			return cmd
		}
		name := parts[len(parts)-1]
		cmd := &cobra.Command{Use: name}
		if len(parts) == 1 {
			root.AddCommand(cmd)
		} else {
			parent := findOrCreate(parts[:len(parts)-1])
			parent.AddCommand(cmd)
		}
		nodeMap[key] = cmd
		return cmd
	}

	for _, g := range groups {
		parts := strings.Split(g.Path, ".")
		cmd := findOrCreate(parts)
		if g.Description != "" {
			cmd.Short = g.Description
		}
		if len(g.Aliases) > 0 {
			cmd.Aliases = g.Aliases
		}
	}

	for _, t := range tools {
		parts := strings.Split(t.Name(), ".")
		leafName := parts[len(parts)-1]

		var parentCmd *cobra.Command
		if len(parts) == 1 {
			parentCmd = root
		} else {
			parentCmd = findOrCreate(parts[:len(parts)-1])
		}

		leaf := &cobra.Command{
			Use:   leafName,
			Short: t.Description(),
		}

		schema := t.InputSchema()
		flags := map[string]*flagInfo{}

		if schema != nil {
			addFlags(leaf, schema, nil, true, flags)
		}

		// Universal escape hatch: any tool can be invoked with the full
		// JSON input via --input (file path) or --input - (stdin). This
		// is the workaround for tool inputs that contain array-of-object
		// fields, which cobra's StringSlice flag cannot encode.
		leaf.Flags().
			String("input", "", "Read full JSON input from file (or '-' for stdin). Bypasses individual flags. Useful for inputs with array-of-object fields like patches[] or add_params[].")

		leaf.RunE = makeRunE(t, schema, flags)
		parentCmd.AddCommand(leaf)
	}
}

// addFlags recursively walks the schema and registers cobra flags.
// For nested object properties, it flattens them with a prefix.
// A flag is marked required only if all ancestors in the path are required.
func addFlags(
	cmd *cobra.Command,
	schema *jsonschema.Schema,
	prefix []string,
	parentRequired bool,
	flags map[string]*flagInfo,
) {
	requiredSet := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		requiredSet[r] = true
	}

	for propName, propSchema := range schema.Properties {
		jsonPath := append(append([]string{}, prefix...), propName)
		isRequired := parentRequired && requiredSet[propName]

		// Recurse into nested objects
		if resolveSchemaType(propSchema) == "object" && len(propSchema.Properties) > 0 {
			addFlags(cmd, propSchema, jsonPath, isRequired, flags)
			continue
		}

		flagName := toFlagName(jsonPath)
		fi := &flagInfo{jsonPath: jsonPath, schema: propSchema, required: isRequired}
		flags[flagName] = fi

		desc := propSchema.Description
		registerFlag(cmd, flagName, propSchema, desc)

		// Don't use cobra's MarkFlagRequired — it fires before RunE, which
		// blocks the --input escape hatch. Required-ness is enforced inside
		// makeRunE instead, only when --input isn't used.

		if len(propSchema.Enum) > 0 {
			registerEnumCompletion(cmd, flagName, propSchema.Enum)
		}
	}
}

// toFlagName converts a JSON path like ["auth", "key_file"] to "auth-key-file".
func toFlagName(path []string) string {
	return strings.ReplaceAll(strings.Join(path, "-"), "_", "-")
}

// resolveSchemaType returns the effective type for a schema property.
// jsonschema.For may set Type (single) or Types (multi, e.g. ["null","array"]
// for omitempty slices). This function normalises both forms.
func resolveSchemaType(schema *jsonschema.Schema) string {
	if schema.Type != "" {
		return schema.Type
	}
	// Check Types (plural) — pick the first non-null type.
	for _, t := range schema.Types {
		if t != "null" {
			return t
		}
	}
	// Fallback: if Items is set, it's an array.
	if schema.Items != nil {
		return "array"
	}
	return "string"
}

// registerFlag adds a typed cobra flag based on the schema type.
func registerFlag(cmd *cobra.Command, name string, schema *jsonschema.Schema, desc string) {
	var defaultStr string
	var defaultInt int
	var defaultFloat float64
	var defaultBool bool
	if len(schema.Default) > 0 {
		_ = json.Unmarshal(schema.Default, &defaultStr)
		_ = json.Unmarshal(schema.Default, &defaultInt)
		_ = json.Unmarshal(schema.Default, &defaultFloat)
		_ = json.Unmarshal(schema.Default, &defaultBool)
	}

	switch resolveSchemaType(schema) {
	case "string":
		cmd.Flags().String(name, defaultStr, desc)
	case "boolean":
		cmd.Flags().Bool(name, defaultBool, desc)
	case "integer":
		cmd.Flags().Int(name, defaultInt, desc)
	case "number":
		cmd.Flags().Float64(name, defaultFloat, desc)
	case "array":
		// Arrays of strings are represented as comma-separated values.
		// e.g. --suites lint,test --targets ./pkg/fs/...,./internal/tool/...
		cmd.Flags().StringSlice(name, nil, desc)
	default:
		cmd.Flags().String(name, defaultStr, desc)
	}
}

// registerEnumCompletion adds shell completion for enum values.
func registerEnumCompletion(cmd *cobra.Command, flagName string, enum []any) {
	vals := make([]string, 0, len(enum))
	for _, v := range enum {
		vals = append(vals, fmt.Sprintf("%v", v))
	}
	_ = cmd.RegisterFlagCompletionFunc(
		flagName,
		func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return vals, cobra.ShellCompDirectiveNoFileComp
		},
	)
}

// makeRunE creates the RunE function for a leaf command.
func makeRunE(t tool.Tool, _ *jsonschema.Schema, flags map[string]*flagInfo) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		// --input lets the user provide the entire JSON input either from a
		// file path or from stdin (with "-"). This is the escape hatch for
		// fields that cobra's StringSlice flag can't represent — most
		// notably arrays-of-objects like patches[] and add_params[].
		if inputPath, _ := cmd.Flags().GetString("input"); inputPath != "" {
			raw, err := readInputJSON(inputPath)
			if err != nil {
				return err
			}
			return runWithJSON(cmd, t, raw)
		}

		params := map[string]any{}
		var missing []string

		for flagName, fi := range flags {
			f := cmd.Flags().Lookup(flagName)
			if f == nil || !f.Changed {
				if fi.required {
					missing = append(missing, "--"+flagName)
				}
				continue
			}

			val, err := getFlagValue(cmd, flagName, fi.schema)
			if err != nil {
				return err
			}

			// Validate enum
			if len(fi.schema.Enum) > 0 {
				if err := validateEnum(flagName, val, fi.schema.Enum); err != nil {
					return err
				}
			}

			setNested(params, fi.jsonPath, val)
		}

		if len(missing) > 0 {
			return fmt.Errorf(
				"required flag(s) not set: %s (or use --input to pass full JSON)",
				strings.Join(missing, ", "),
			)
		}

		inputJSON, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal input: %w", err)
		}

		result, err := t.Execute(context.Background(), inputJSON)
		if err != nil {
			return err
		}

		out, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal output: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}
}

// readInputJSON reads JSON from a file path or stdin (when path == "-").
func readInputJSON(path string) ([]byte, error) {
	if path == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return raw, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input %s: %w", path, err)
	}
	return raw, nil
}

// runWithJSON executes the tool with raw JSON input (bypassing flag parsing).
func runWithJSON(cmd *cobra.Command, t tool.Tool, raw []byte) error {
	// Validate the JSON parses — better error than passing junk to Execute.
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("parse --input JSON: %w", err)
	}
	result, err := t.Execute(context.Background(), raw)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(out))
	return nil
}

// getFlagValue reads a typed value from a cobra flag.
func getFlagValue(cmd *cobra.Command, name string, schema *jsonschema.Schema) (any, error) {
	switch resolveSchemaType(schema) {
	case "string":
		return cmd.Flags().GetString(name)
	case "boolean":
		return cmd.Flags().GetBool(name)
	case "integer":
		return cmd.Flags().GetInt(name)
	case "number":
		return cmd.Flags().GetFloat64(name)
	case "array":
		return cmd.Flags().GetStringSlice(name)
	default:
		return cmd.Flags().GetString(name)
	}
}

// validateEnum checks that a value is one of the allowed enum values.
// For slices ([]string from array flags), each element is validated individually.
func validateEnum(flagName string, val any, enum []any) error {
	allowed := make([]string, 0, len(enum))
	for _, e := range enum {
		allowed = append(allowed, fmt.Sprintf("%v", e))
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = true
	}

	// Handle slice values (from array flags).
	if slice, ok := val.([]string); ok {
		for _, item := range slice {
			if !allowedSet[item] {
				return fmt.Errorf(
					"invalid value %q for --%s: must be one of [%s]",
					item,
					flagName,
					strings.Join(allowed, ", "),
				)
			}
		}
		return nil
	}

	// Scalar value.
	valStr := fmt.Sprintf("%v", val)
	if !allowedSet[valStr] {
		return fmt.Errorf(
			"invalid value %q for --%s: must be one of [%s]",
			valStr,
			flagName,
			strings.Join(allowed, ", "),
		)
	}
	return nil
}

// setNested sets a value at a nested path in a map.
// e.g. setNested(m, ["auth", "user"], "deploy") → m["auth"]["user"] = "deploy"
func setNested(m map[string]any, path []string, value any) {
	for i := 0; i < len(path)-1; i++ {
		sub, ok := m[path[i]].(map[string]any)
		if !ok {
			sub = map[string]any{}
			m[path[i]] = sub
		}
		m = sub
	}
	m[path[len(path)-1]] = value
}
