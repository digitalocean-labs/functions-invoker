package exec

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContainerInitPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input ContainerInitPayload
		want  string
	}{{
		name: "full example",
		input: ContainerInitPayload{
			Name:       "testfunction",
			Entrypoint: "main",
			Code:       "testcode",
			IsBinary:   true,
			Environment: InvocationEnvironment{
				ActivationID:    "testaid",
				TransactionID:   "testtid",
				Namespace:       "testnamespace",
				FunctionName:    "testfunction",
				FunctionVersion: "testversion",
				Deadline:        time.UnixMilli(100),
				APIKey:          "testkey",
			},
			// We expect these to be marshalled into strings correctly.
			Parameters: map[string]json.RawMessage{
				"str":   []byte(`"a \"tricky\" string"`),
				"bool":  []byte(`false`),
				"num":   []byte(`4.5`),
				"obj":   []byte(`{"this is": "a struct"}`),
				"array": []byte(`["str", 8.7, false]`),
			},
		},
		want: `{
			"value": {
				"name": "testfunction",
				"main": "main",
				"code": "testcode",
				"binary": true,
				"env": {
					"str": "a \"tricky\" string",
					"bool": "false",
					"num": "4.5",
					"obj": "{\"this is\": \"a struct\"}",
					"array": "[\"str\", 8.7, false]",
					"__OW_ACTIVATION_ID": "testaid",
					"__OW_TRANSACTION_ID": "testtid",
					"__OW_NAMESPACE": "testnamespace",
					"__OW_ACTION_NAME": "testfunction",
					"__OW_ACTION_VERSION": "testversion",
					"__OW_DEADLINE": "100",
					"__OW_API_KEY": "testkey"
				}
			}
		}`,
	}, {
		name: "no api key",
		input: ContainerInitPayload{
			Name:       "testfunction",
			Entrypoint: "main",
			Code:       "testcode",
			IsBinary:   false,
			Environment: InvocationEnvironment{
				ActivationID:    "testaid",
				TransactionID:   "testtid",
				Namespace:       "testnamespace",
				FunctionName:    "testfunction",
				FunctionVersion: "testversion",
				Deadline:        time.UnixMilli(100),
			},
		},
		want: `{
			"value": {
				"name": "testfunction",
				"main": "main",
				"code": "testcode",
				"binary": false,
				"env": {
					"__OW_ACTIVATION_ID": "testaid",
					"__OW_TRANSACTION_ID": "testtid",
					"__OW_NAMESPACE": "testnamespace",
					"__OW_ACTION_NAME": "testfunction",
					"__OW_ACTION_VERSION": "testversion",
					"__OW_DEADLINE": "100"
				}
			}
		}`,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.input)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestContainerRunPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input ContainerRunPayload
		want  string
	}{{
		name: "full example",
		input: ContainerRunPayload{
			Environment: InvocationEnvironment{
				ActivationID:    "testaid",
				TransactionID:   "testtid",
				Namespace:       "testnamespace",
				FunctionName:    "testfunction",
				FunctionVersion: "testversion",
				Deadline:        time.UnixMilli(100),
				APIKey:          "testkey",
			},
			// The "types" of these should remain intact in the passed JSON.
			Parameters: map[string]json.RawMessage{
				"str":   []byte(`"a \"tricky\" string"`),
				"bool":  []byte(`false`),
				"num":   []byte(`4.5`),
				"obj":   []byte(`{"this is": "a struct"}`),
				"array": []byte(`["str", 8.7, false]`),
			},
		},
		want: `{
			"activation_id": "testaid",
			"transaction_id": "testtid",
			"namespace": "testnamespace",
			"action_name": "testfunction",
			"action_version": "testversion",
			"deadline": "100",
			"api_key": "testkey",
			"value": {
				"str": "a \"tricky\" string",
				"bool": false,
				"num": 4.5,
				"obj": {"this is": "a struct"},
				"array": ["str", 8.7, false]
			}
		}`,
	}, {
		name: "no api key",
		input: ContainerRunPayload{
			Environment: InvocationEnvironment{
				ActivationID:    "testaid",
				TransactionID:   "testtid",
				Namespace:       "testnamespace",
				FunctionName:    "testfunction",
				FunctionVersion: "testversion",
				Deadline:        time.UnixMilli(100),
			},
		},
		want: `{
			"activation_id": "testaid",
			"transaction_id": "testtid",
			"namespace": "testnamespace",
			"action_name": "testfunction",
			"action_version": "testversion",
			"deadline": "100",
			"value": {}
		}`,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.input)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}
