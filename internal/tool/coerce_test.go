// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package tool

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestCoerceJSONToSchema(t *testing.T) {
	type sample struct {
		DryRun bool    `json:"dry_run"`
		Limit  int     `json:"limit"`
		Ratio  float64 `json:"ratio"`
		Name   string  `json:"name"`
		Nested struct {
			Enabled bool `json:"enabled"`
		} `json:"nested"`
		Tags []bool `json:"tags"`
	}
	schema, err := jsonschema.For[sample](nil)
	if err != nil {
		t.Fatalf("infer schema: %v", err)
	}

	cases := []struct {
		name string
		in   string
		want sample
	}{
		{
			name: "all native types unchanged",
			in:   `{"dry_run": true, "limit": 5, "ratio": 1.5, "name": "n", "nested": {"enabled": false}, "tags": [true, false]}`,
			want: sample{DryRun: true, Limit: 5, Ratio: 1.5, Name: "n", Tags: []bool{true, false}},
		},
		{
			name: "stringified bool true",
			in:   `{"dry_run": "true"}`,
			want: sample{DryRun: true},
		},
		{
			name: "stringified bool false",
			in:   `{"dry_run": "false"}`,
			want: sample{DryRun: false},
		},
		{
			name: "stringified bool TRUE/FALSE",
			in:   `{"dry_run": "TRUE"}`,
			want: sample{DryRun: true},
		},
		{
			name: "stringified bool numeric",
			in:   `{"dry_run": "1"}`,
			want: sample{DryRun: true},
		},
		{
			name: "stringified integer",
			in:   `{"limit": "42"}`,
			want: sample{Limit: 42},
		},
		{
			name: "stringified number",
			in:   `{"ratio": "3.14"}`,
			want: sample{Ratio: 3.14},
		},
		{
			name: "nested object coercion",
			in:   `{"nested": {"enabled": "true"}}`,
			want: sample{Nested: struct {
				Enabled bool `json:"enabled"`
			}{Enabled: true}},
		},
		{
			name: "array element coercion",
			in:   `{"tags": ["true", "0", true]}`,
			want: sample{Tags: []bool{true, false, true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coerced := coerceJSONToSchema([]byte(tc.in), schema)
			var got sample
			if err := json.Unmarshal(coerced, &got); err != nil {
				t.Fatalf("unmarshal coerced: %v\n  raw=%s\n  coerced=%s", err, tc.in, coerced)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v; want %+v\n  raw=%s\n  coerced=%s", got, tc.want, tc.in, coerced)
			}
		})
	}
}

func TestCoerceJSONToSchema_LeavesUnrecognisedAlone(t *testing.T) {
	type sample struct {
		Flag bool `json:"flag"`
	}
	schema, err := jsonschema.For[sample](nil)
	if err != nil {
		t.Fatalf("infer schema: %v", err)
	}
	// Unknown bool string — leave it; downstream Unmarshal will error.
	coerced := coerceJSONToSchema([]byte(`{"flag": "maybe"}`), schema)
	var got sample
	if err := json.Unmarshal(coerced, &got); err == nil {
		t.Fatal("expected unmarshal error for unrecognised string; got nil")
	}
}

func TestCoerceJSONToSchema_NilSchema(t *testing.T) {
	in := []byte(`{"flag": true}`)
	got := coerceJSONToSchema(in, nil)
	if string(got) != string(in) {
		t.Errorf("nil schema must passthrough; got %s", got)
	}
}

func TestCoerceJSONToSchema_MalformedJSON(t *testing.T) {
	type sample struct{}
	schema, _ := jsonschema.For[sample](nil)
	in := []byte(`{not json`)
	got := coerceJSONToSchema(in, schema)
	if string(got) != string(in) {
		t.Errorf("malformed JSON must passthrough; got %s", got)
	}
}
