package protocol

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"invoker/pkg/encryption"
)

var (
	// This is taken from a health function as scheduled after the invoker comes up.
	// We're testing this as well as this message is somewhat out-of-the-norm as it doesn't go
	// through the "usual" authentication flow and thus lacks some information we otherwise expect.
	//go:embed testdata/activation_message_health.json
	activationMessageHealth []byte

	// This is taken from the "overrides predefined parameters" test in invocation_test.go. It's
	// slightly adjusted to also contain init parameters and disable activation storage.
	// It contains both encrypted and decrypted parameters to thoroughly test their handling being
	// transparent.
	//go:embed testdata/activation_message_full.json
	activationMessageFull []byte
)

func TestParseActivationMessage(t *testing.T) {
	t.Parallel()

	decryptor, err := encryption.NewDecryptor(encryption.Keys{
		// This key has been used in the environment when producing the test messages referenced above.
		"aes-256": mustBase64Decode(t, "cPNFhshWSMHytzZqFur1QnEl1y5EkUBxK/bEdhkyXMc="),
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		input     []byte
		want      *ActivationMessage
		wantError bool
	}{{
		name:      "nil input",
		input:     nil,
		wantError: true,
	}, {
		name:      "empty input",
		input:     []byte{},
		wantError: true,
	}, {
		name:      "wrong input",
		input:     []byte("[]"),
		wantError: true,
	}, {
		name:  "health invocation",
		input: activationMessageHealth,
		want: &ActivationMessage{
			TransactionID:       "sid_invokerHealth",
			TransactionStart:    time.UnixMilli(1669228564541),
			ActivationID:        "b06a31ee5b6c4b48aa31ee5b6cdb48bf",
			RootControllerIndex: "0",
			IsBlocking:          false,
			Function: ActivationMessageFunction{
				Namespace: "whisk.system",
				Name:      "invokerHealthTestAction0",
				Version:   "0.0.1",
			},
			InvocationIdentity: InvocationIdentity{
				Subject:          "whisk.system",
				Namespace:        "whisk.system",
				APIKey:           "48674879-9994-42a3-a748-79999442a356:j8DmUChNjIoQ8nygYAcCspZQRXpj2mw3Whq6dpYdmYI79xiwXhAWyGgmFZ835za9",
				StoreActivations: true,
			},
		},
	}, {
		name:  "full example",
		input: activationMessageFull,
		want: &ActivationMessage{
			TransactionID:       "e2d2aa85b5115fda86524d25f3a0fded",
			TransactionStart:    time.UnixMilli(1669276664249),
			ActivationID:        "40547988d96d441e947988d96d141e6f",
			RootControllerIndex: "0",
			IsBlocking:          true,
			Function: ActivationMessageFunction{
				Namespace: "fn-test-28620",
				Name:      "TestFunctionInvocations__overrides_predefined_parameters-kNMzNSxmK",
				Version:   "0.0.1",
				Revision:  "1-c8cf59c129b0676039b545dc54e9d7ae",
				Binding:   "abinding",
			},
			InvocationIdentity: InvocationIdentity{
				Subject:          "fn-test-28620-subject",
				Namespace:        "fn-test-28620-namespace",
				Annotations:      map[string]string{"thisis": "atest"},
				APIKey:           "7759a935-e45c-4752-86fd-f5d216c6437f:i2NDjgGTGibl4jYKrHwjtYr8UMXX57KqhXKTN4dDmJtQYLqkfNlxpvjm3ABftnpE",
				StoreActivations: false,
			},
			Parameters: map[string]json.RawMessage{
				"added": []byte(`"this is new!"`),
				"array": []byte(`[1.1,"anotherstr"]`),
				"bool":  []byte(`true`),
				"obj":   []byte(`{"this is":"a struct"}`),
			},
			InitParameters: map[string]json.RawMessage{
				"num": []byte(`8.7`),
				"str": []byte(`"a \"tricky\" string"`),
			},
		},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activationMessage, err := ParseActivationMessage(tt.input, decryptor)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Compare the parameters and init parameters separately as they're subject to JSON
			// mangling and thus might change in representation.
			requireEqualJSON(t, tt.want.Parameters, activationMessage.Parameters)
			requireEqualJSON(t, tt.want.InitParameters, activationMessage.InitParameters)

			// Force-equal the actual comparison afterwards.
			activationMessage.Parameters = tt.want.Parameters
			activationMessage.InitParameters = tt.want.InitParameters

			require.Equal(t, tt.want, activationMessage)
		})
	}
}

func mustBase64Decode(t testing.TB, in string) []byte {
	t.Helper()

	decoded, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		panic(err)
	}

	return decoded
}

func requireEqualJSON(t *testing.T, want any, got any) {
	t.Helper()

	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)

	require.JSONEq(t, string(wantJSON), string(gotJSON))
}
