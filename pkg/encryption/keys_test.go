package encryption

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptionKeysDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		want      Keys
		wantError bool
	}{{
		name:  "basic",
		input: `{"aes-256": "dGVzdGtleTE="}`,
		want:  Keys{"aes-256": []byte("testkey1")},
	}, {
		name:  "multiple keys",
		input: `{"key1": "dGVzdGtleTE=", "key2": "Zm9va2V5Mg=="}`,
		want:  Keys{"key1": []byte("testkey1"), "key2": []byte("fookey2")},
	}, {
		name:  "empty keys",
		input: "{}",
		want:  Keys{},
	}, {
		name:      "broken JSON",
		input:     "[]",
		wantError: true,
	}, {
		name:      "empty input",
		input:     "",
		wantError: true,
	}, {
		name:      "malformed base64-encoded key",
		input:     `{"aes-256": "foo"}`,
		wantError: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var manifests Keys
			err := manifests.Decode(tt.input)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, manifests)
		})
	}
}
