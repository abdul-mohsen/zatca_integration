// Package secutil provides AES-256-GCM encryption for sensitive database fields.
// The ciphertext is stored as base64 with an "enc:" prefix so the code can
// distinguish encrypted values from plaintext (for migration / backward compat).
package secutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const prefix = "enc:"

// Key holds a validated 32-byte AES-256 key.
type Key struct {
	raw []byte
}

// NewKey creates a Key from a hex-encoded or base64-encoded 32-byte key.
// Accepts either 64 hex chars or 44 base64 chars (both decode to 32 bytes).
func NewKey(encoded string) (*Key, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("secutil: encryption key is empty")
	}

	// Try base64 first, then hex
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 {
		raw, err = decodeHex(encoded)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("secutil: key must be 32 bytes (got %d); provide 64 hex chars or 44 base64 chars", len(raw))
		}
	}
	return &Key{raw: raw}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM.
// Returns "enc:" + base64(nonce + ciphertext).
// If plaintext is empty, returns empty string unchanged.
func (k *Key) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	block, err := aes.NewCipher(k.raw)
	if err != nil {
		return "", fmt.Errorf("secutil: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secutil: new gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("secutil: random nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value produced by Encrypt.
// Returns an error if the value is not encrypted (missing "enc:" prefix).
// If the value is empty, returns empty string.
func (k *Key) Decrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, prefix) {
		return "", errors.New("secutil: value is not encrypted (missing enc: prefix)")
	}

	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", fmt.Errorf("secutil: base64 decode: %w", err)
	}

	block, err := aes.NewCipher(k.raw)
	if err != nil {
		return "", fmt.Errorf("secutil: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secutil: new gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("secutil: ciphertext too short")
	}

	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("secutil: decrypt: %w", err)
	}
	return string(plaintext), nil
}

// IsEncrypted returns true if the value has the "enc:" prefix.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, prefix)
}

func decodeHex(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errors.New("odd-length hex")
	}
	b := make([]byte, len(s)/2)
	for i := 0; i < len(b); i++ {
		hi, err := hexVal(s[2*i])
		if err != nil {
			return nil, err
		}
		lo, err := hexVal(s[2*i+1])
		if err != nil {
			return nil, err
		}
		b[i] = hi<<4 | lo
	}
	return b, nil
}

func hexVal(c byte) (byte, error) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', nil
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, nil
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, nil
	default:
		return 0, fmt.Errorf("invalid hex char: %c", c)
	}
}
