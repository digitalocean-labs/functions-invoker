package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
)

const (
	// To be compatible with the legacy system, we use a fixed nonceSize of 128.
	// The standard nonce size in the Go stdlib is 12 but since the controller hardcodes a nonce
	// size of 128 for its AES-256 implementation (which we use), we have to rely on an explicit
	// nonce size.
	nonceSize = 128
	// The first 4 bytes of an encrypted value denote the actual nonceSize.
	nonceSizeLength = 4
)

// NewDecryptor creates a decryptor that can decrypt values encrypted with the passed keys.
func NewDecryptor(keys Keys) (*Decryptor, error) {
	// Since we're assuming a fixed nonceSize, we can precreate blocks and AEADs for all of the
	// keys known to us.
	keyNameToAEAD := make(map[string]cipher.AEAD, len(keys))
	for keyName, key := range keys {
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("failed to create cipher block from key: %w", err)
		}

		aesgcm, err := cipher.NewGCMWithNonceSize(block, nonceSize)
		if err != nil {
			return nil, fmt.Errorf("failed to create GCM instance: %w", err)
		}
		keyNameToAEAD[keyName] = aesgcm
	}

	return &Decryptor{
		keyNameToAEAD: keyNameToAEAD,
	}, nil
}

// Decryptor can decrypt values with varying keys.
type Decryptor struct {
	keyNameToAEAD map[string]cipher.AEAD
}

// Decrypt decrypts the given encrypted value using the given key name.
func (d *Decryptor) Decrypt(keyName string, encrypted []byte) ([]byte, error) {
	aesgcm, ok := d.keyNameToAEAD[keyName]
	if !ok {
		return nil, fmt.Errorf("failed to find key for name %q", keyName)
	}

	// Since we assume a nonceSize the first 4 bytes denote the nonceSize, anything smaller
	// than or equal to 4 + nonceSize bytes is malformed.
	if len(encrypted) <= nonceSizeLength+nonceSize {
		return nil, fmt.Errorf("malformed encrypted value: length %d <= %d", len(encrypted), nonceSizeLength+nonceSize)
	}

	if givenNonceLength := binary.BigEndian.Uint32(encrypted); givenNonceLength != nonceSize {
		return nil, fmt.Errorf("malformed encrypted value: wrong nonce-length of %d", givenNonceLength)
	}

	return aesgcm.Open(nil, encrypted[nonceSizeLength:nonceSizeLength+nonceSize], encrypted[nonceSizeLength+nonceSize:], nil)
}
