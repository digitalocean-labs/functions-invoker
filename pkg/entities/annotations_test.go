package entities

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnnotationsMarshalUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input Annotations
		want  string
	}{{
		name:  "empty",
		input: Annotations{},
		want:  `[]`,
	}, {
		name:  "single annotation",
		input: Annotations{"foo": "bar"},
		want:  `[{"key":"foo","value":"bar"}]`,
	}, {
		name: "multiple annotations with different types",
		input: Annotations{
			"str":   `a "tricky" string`,
			"bool":  false,
			"num":   4.5,
			"obj":   map[string]interface{}{"this is": "a struct"},
			"array": []interface{}{"str", 8.7, false},
		},
		want: `[{"key":"array","value":["str",8.7,false]},{"key":"bool","value":false},{"key":"num","value":4.5},{"key":"obj","value":{"this is":"a struct"}},{"key":"str","value":"a \"tricky\" string"}]`,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.input)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))

			var got2 Annotations
			err = json.Unmarshal(got, &got2)
			require.NoError(t, err)
			require.Equal(t, tt.input, got2)
		})
	}
}

func TestAnnotationsIsTruthy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input Annotations

		key                 string
		valueForNonExistent bool
		want                bool
	}{{
		name:                "empty default true",
		input:               Annotations{},
		key:                 "foo",
		valueForNonExistent: true,
		want:                true,
	}, {
		name:                "empty default false",
		input:               Annotations{},
		key:                 "foo",
		valueForNonExistent: false,
		want:                false,
	}, {
		name:  "bool true",
		input: Annotations{"foo": true},
		key:   "foo",
		want:  true,
	}, {
		name:  "bool false",
		input: Annotations{"foo": false},
		key:   "foo",
		want:  false,
	}, {
		name:  "float64 true",
		input: Annotations{"foo": 1.0},
		key:   "foo",
		want:  true,
	}, {
		name:  "float64 false",
		input: Annotations{"foo": 0.0},
		key:   "foo",
		want:  false,
	}, {
		name:  "string true",
		input: Annotations{"foo": "false"},
		key:   "foo",
		want:  true,
	}, {
		name:  "string false",
		input: Annotations{"foo": ""},
		key:   "foo",
		want:  false,
	}, {
		name:  "nil",
		input: Annotations{"foo": nil},
		key:   "foo",
		want:  false,
	}, {
		name:  "anything else is true",
		input: Annotations{"foo": struct{}{}},
		key:   "foo",
		want:  true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.input.IsTruthy(tt.key, tt.valueForNonExistent))
		})
	}
}
