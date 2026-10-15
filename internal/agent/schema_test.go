package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_modelSchema(t *testing.T) {
	tests := map[string]struct {
		input    string
		expected string
	}{
		"nullable array": {
			input:    `{"type":"object","properties":{"daemonsets":{"type":["null","array"],"items":{"type":"object"}}}}`,
			expected: `{"type":"object","properties":{"daemonsets":{"type":"array","items":{"type":"object"}}}}`,
		},
		"null listed last": {
			input:    `{"type":["string","null"]}`,
			expected: `{"type":"string"}`,
		},
		"nested in items and defs": {
			input:    `{"items":{"properties":{"pods":{"type":["null","array"]}}},"$defs":{"ref":{"type":["null","object"]}}}`,
			expected: `{"items":{"properties":{"pods":{"type":"array"}}},"$defs":{"ref":{"type":"object"}}}`,
		},
		"inside anyOf": {
			input:    `{"anyOf":[{"type":["null","integer"]},{"type":"string"}]}`,
			expected: `{"anyOf":[{"type":"integer"},{"type":"string"}]}`,
		},
		"several non-null types keep a list": {
			input:    `{"type":["null","string","integer"]}`,
			expected: `{"type":["string","integer"]}`,
		},
		"only null is kept": {
			input:    `{"type":["null"]}`,
			expected: `{"type":["null"]}`,
		},
		"property named type is a schema": {
			input:    `{"properties":{"type":{"type":["null","string"]}},"required":["type"]}`,
			expected: `{"properties":{"type":{"type":"string"}},"required":["type"]}`,
		},
		"enum keeps null": {
			input:    `{"enum":["null","a"]}`,
			expected: `{"enum":["null","a"]}`,
		},
		"literal values unchanged": {
			input:    `{"properties":{"config":{"type":"object","default":{"type":["null","array"]},"const":{"type":["null"]},"examples":[{"type":["null","string"]}]}}}`,
			expected: `{"properties":{"config":{"type":"object","default":{"type":["null","array"]},"const":{"type":["null"]},"examples":[{"type":["null","string"]}]}}}`,
		},
		"property named default is a schema": {
			input:    `{"properties":{"default":{"type":["null","string"]}}}`,
			expected: `{"properties":{"default":{"type":"string"}}}`,
		},
		"every subschema keyword": {
			input:    `{"additionalProperties":{"type":["null","string"]},"patternProperties":{"^x":{"type":["null","string"]}},"definitions":{"d":{"type":["null","string"]}},"prefixItems":[{"type":["null","string"]}],"oneOf":[{"type":["null","string"]}],"allOf":[{"type":["null","string"]}],"not":{"type":["null","string"]},"if":{"type":["null","string"]},"then":{"type":["null","string"]},"else":{"type":["null","string"]}}`,
			expected: `{"additionalProperties":{"type":"string"},"patternProperties":{"^x":{"type":"string"}},"definitions":{"d":{"type":"string"}},"prefixItems":[{"type":"string"}],"oneOf":[{"type":"string"}],"allOf":[{"type":"string"}],"not":{"type":"string"},"if":{"type":"string"},"then":{"type":"string"},"else":{"type":"string"}}`,
		},
		"boolean subschemas unchanged": {
			input:    `{"additionalProperties":false,"items":true}`,
			expected: `{"additionalProperties":false,"items":true}`,
		},
		"plain types unchanged": {
			input:    `{"type":"object","properties":{"name":{"type":"string"}}}`,
			expected: `{"type":"object","properties":{"name":{"type":"string"}}}`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.JSONEq(t, tc.expected, string(modelSchema(json.RawMessage(tc.input))))
		})
	}
}

func Test_modelSchema_passthrough(t *testing.T) {
	tests := map[string]string{
		"empty":   ``,
		"invalid": `{"type":`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, input, string(modelSchema(json.RawMessage(input))))
		})
	}
}
