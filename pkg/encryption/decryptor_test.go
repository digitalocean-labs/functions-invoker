package encryption

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecryptorDecrypt(t *testing.T) {
	t.Parallel()

	testKey := mustBase64Decode(t, "SmFOZFJmVWpYbjJyNXU4eC9BP0QoRytLYlBlU2hWa1k=")

	tests := []struct {
		name      string
		keys      Keys
		keyName   string
		input     []byte
		want      []byte
		wantError bool
	}{{
		name:    "int",
		keys:    Keys{"testkey": testKey},
		keyName: "testkey",
		// Value obtained from the Controller.
		input: mustBase64Decode(t, "AAAAgH/CfLmQHalJrdmPKln12FiODRySlbmFHPwoGBZS9oKlt5tkjKHo11g3GI7BfNq8WuKhYeRjV0/q6280XE7tIHSG6XjKReGyZ2UboW4cudeWsET+cK07O1uzV1FUh5KemwgVKblb7aFr1TvhhGCHG7Ds7QTVGO3pv8NsP6upLH4OIMq9CI84QNXj3wCXlOGEKts="),
		want:  []byte(`5`),
	}, {
		name:    "float",
		keys:    Keys{"testkey": testKey},
		keyName: "testkey",
		// Value obtained from the Controller.
		input: mustBase64Decode(t, "AAAAgLlaLG5UTRRQGVsQl2gLtiKFG67QM9rwrFfjFS9kF4VlMTJOd+EC9P5+zT2LP4y12EEM3ZwxVzr1tE6dL9nb02D8N+cbnmBt+XBbP3CTayky5QyEW2LFb9hIV0a2M86lTOUiFs8i/pAHwGMYit+IJtkyq1CHUukvHdTEtzIFKRn2rnGUuMCumAQx0HaQJkK7tXR5dA=="),
		want:  []byte(`4.5`),
	}, {
		name:    "string",
		keys:    Keys{"testkey": testKey},
		keyName: "testkey",
		// Value obtained from the Controller.
		input: mustBase64Decode(t, "AAAAgMCLOsxMxfSx8NBNg8+KyOcPOh8IyHY/WG0M5KrrhGfpSoWThl9z94YoAkeVcgmMlarq22foFSM4fkkcrWa6KIxQjbpAyYRe4u/cub5YOydOHUdutFrzgIQvBEwXpPiTE2A4R5R0dYJrBAkp8tsA71syuFAmiI3dRTq7lbTf+RtZEZr00XIMIMgH0n0fJr/7jsXV0p55"),
		want:  []byte(`"foo"`),
	}, {
		name:    "bool",
		keys:    Keys{"testkey": testKey},
		keyName: "testkey",
		// Value obtained from the Controller.
		input: mustBase64Decode(t, "AAAAgMz59bVgbCEitUtbgAvUIrRljZlu8RH8gR6AnnoKmwhIDBAi9FPorDg9BsdrwPbdpSxOVUOsW1HoDnyQOTTVKkKLJN5Ll0/b3j4sWlpmYbRo1a/2dCAg4+VUhjvfK+Sxi9FwtycgugmZRqm5C2QHBPXXa85b3oBXJbSafRJH8b44aNy0tFSyWOn4LC2xXqVgI2bfp1Gp"),
		want:  []byte(`false`),
	}, {
		name:    "structured",
		keys:    Keys{"testkey": testKey},
		keyName: "testkey",
		// Value obtained from the Controller.
		input: mustBase64Decode(t, "AAAAgHGKmGGBxUvp1HLSxcwEaD8qYkcDnp52YxuQb1W3PTp3ypml899dEq8dL3NSm6BIuagGj2BLViQDqEE378tgUVPwwOkbUj/hzMqD/ujz3/B8KfIVc6FCHqXDockRj+/NrMxbBFIVqpk5bjAMEtbH4AD4Jr/d8hsLxHmVDKc98mbts9M1UgZksJk4YP7FZJebHstfWaaVuvFwgioMi0U="),
		want:  []byte(`{"foo":"bar"}`),
	}, {
		name:      "key not present",
		keys:      Keys{},
		keyName:   "nopes",
		input:     []byte{},
		wantError: true,
	}, {
		name:      "malformed input",
		keys:      Keys{"testkey": testKey},
		keyName:   "testkey",
		input:     []byte{},
		wantError: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decryptor, err := NewDecryptor(tt.keys)
			require.NoError(t, err, "failed to create decryptor")

			decrypted, err := decryptor.Decrypt(tt.keyName, tt.input)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, decrypted)
		})
	}
}

func BenchmarkDecryptorDecrypt(b *testing.B) {
	decryptor, err := NewDecryptor(Keys{"testkey": mustBase64Decode(b, "SmFOZFJmVWpYbjJyNXU4eC9BP0QoRytLYlBlU2hWa1k=")})
	require.NoError(b, err)

	value := []byte(`5`)
	encrypted := mustBase64Decode(b, "AAAAgH/CfLmQHalJrdmPKln12FiODRySlbmFHPwoGBZS9oKlt5tkjKHo11g3GI7BfNq8WuKhYeRjV0/q6280XE7tIHSG6XjKReGyZ2UboW4cudeWsET+cK07O1uzV1FUh5KemwgVKblb7aFr1TvhhGCHG7Ds7QTVGO3pv8NsP6upLH4OIMq9CI84QNXj3wCXlOGEKts=")

	for i := 0; i < b.N; i++ {
		decrypted, err := decryptor.Decrypt("testkey", encrypted)
		if err != nil || !bytes.Equal(value, decrypted) {
			b.Fatalf("Failed to decrypt value: decrypted: %q, err: %v", decrypted, err)
		}
	}
}

func mustBase64Decode(t testing.TB, in string) []byte {
	t.Helper()

	decoded, err := base64.StdEncoding.DecodeString(in)
	require.NoError(t, err)

	return decoded
}
