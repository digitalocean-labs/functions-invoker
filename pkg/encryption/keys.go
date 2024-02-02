package encryption

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Keys is a map from key name to AES-256 key.
type Keys map[string][]byte

// Decode implement `envconfig.Decoder`.
func (e *Keys) Decode(s string) error {
	// The keys are passed as key/value pairs, where the key is the key's name and the value is
	// the base64 encoded version of the encryption key.
	var base64Keys map[string]string
	if err := json.Unmarshal([]byte(s), &base64Keys); err != nil {
		return fmt.Errorf("failed to parse encryption config as JSON: %w", err)
	}

	keys := make(map[string][]byte, len(base64Keys))
	for keyName, base64EncodedKey := range base64Keys {
		decodedKey, err := base64.StdEncoding.DecodeString(base64EncodedKey)
		if err != nil {
			return fmt.Errorf("failed to base64-decode key %s: %w", keyName, err)
		}
		keys[keyName] = decodedKey
	}

	*e = keys
	return nil
}
