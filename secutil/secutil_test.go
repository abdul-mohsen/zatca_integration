package secutil

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	key, err := NewKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}

	tests := []string{
		"hello world",
		"-----BEGIN EC PRIVATE KEY-----\nMIGk...\n-----END EC PRIVATE KEY-----",
		"supersecretpassword123!@#",
		"",
	}

	for _, plain := range tests {
		enc, err := key.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plain, err)
		}
		if plain != "" && !strings.HasPrefix(enc, "enc:") {
			t.Fatalf("expected enc: prefix, got %q", enc)
		}
		dec, err := key.Decrypt(enc)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if dec != plain {
			t.Fatalf("got %q, want %q", dec, plain)
		}
	}
}

func TestPlaintextRejected(t *testing.T) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	key, err := NewKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}

	// Values without "enc:" prefix must be rejected
	_, err = key.Decrypt("old-unencrypted-value")
	if err == nil {
		t.Fatal("expected error for unencrypted value")
	}
}

func TestHexKey(t *testing.T) {
	hex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	key, err := NewKey(hex)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := key.Encrypt("test")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := key.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "test" {
		t.Fatalf("got %q, want %q", dec, "test")
	}
}

func TestBadKey(t *testing.T) {
	_, err := NewKey("tooshort")
	if err == nil {
		t.Fatal("expected error for short key")
	}
	_, err = NewKey("")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}
