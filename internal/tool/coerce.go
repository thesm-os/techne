// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package tool

import (
	"encoding/json"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"
)

// coerceJSONToSchema relaxes common type mismatches in raw input JSON
// before strict unmarshalling into a typed struct.
//
// Specifically, when the schema declares a primitive type but the input
// supplies a stringified form, the value is coerced:
//
//   - schema "boolean": "true" / "false" / "1" / "0" → bool
//   - schema "integer": "42" → int (decimal)
//   - schema "number":  "3.14" → float64
//
// Nested objects and arrays are walked recursively so coercion applies
// at any depth. Unknown / unrecognised mismatches are left intact —
// strict json.Unmarshal will surface them with the original type error.
//
// Why: hand-authored JSON inputs (CLI --input=file.json, plus some
// agent serializations of tool calls) routinely quote primitives. The
// strict default treats {"dry_run": "true"} as a type error, hiding
// what the caller obviously meant. Coercing at the boundary keeps the
// internal types strict while making the surface forgiving.
func coerceJSONToSchema(raw []byte, schema *jsonschema.Schema) []byte {
	if schema == nil || len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	coerced := coerceValue(v, schema)
	out, err := json.Marshal(coerced)
	if err != nil {
		return raw
	}
	return out
}

// primaryType returns the schema's effective type. Handles both the
// single-type form (`Type: "boolean"`) and the multi-type form
// (`Types: ["null", "boolean"]`) that jsonschema-go uses for nullable
// fields like maps, slices, and pointers.
func primaryType(schema *jsonschema.Schema) string {
	if schema == nil {
		return ""
	}
	if schema.Type != "" {
		return schema.Type
	}
	for _, t := range schema.Types {
		if t != "null" {
			return t
		}
	}
	return ""
}

func coerceValue(v any, schema *jsonschema.Schema) any {
	if schema == nil {
		return v
	}
	switch primaryType(schema) {
	case "boolean":
		if s, ok := v.(string); ok {
			switch s {
			case "true", "True", "TRUE", "1":
				return true
			case "false", "False", "FALSE", "0":
				return false
			}
		}
	case "integer":
		if s, ok := v.(string); ok {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n
			}
		}
	case "number":
		if s, ok := v.(string); ok {
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				return n
			}
		}
	case "object":
		if m, ok := v.(map[string]any); ok && schema.Properties != nil {
			for key, val := range m {
				if propSchema, exists := schema.Properties[key]; exists {
					m[key] = coerceValue(val, propSchema)
				}
			}
		}
	case "array":
		if arr, ok := v.([]any); ok && schema.Items != nil {
			for i := range arr {
				arr[i] = coerceValue(arr[i], schema.Items)
			}
		}
	}
	return v
}
