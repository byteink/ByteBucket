package storage

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

// withKey installs a fresh 32-byte key and restores whatever was there before,
// so these tests cannot leak key state into the rest of the package.
func withKey(t *testing.T) {
	t.Helper()
	prev := encryptionKey
	t.Cleanup(func() { encryptionKey = prev })
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	SetEncryptionKey(key)
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	withKey(t)
	for _, pt := range []string{"", "secret", strings.Repeat("x", 4096)} {
		ct, err := Encrypt(pt)
		if err != nil {
			t.Fatalf("encrypt %q: %v", pt, err)
		}
		got, err := Decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt %q: %v", pt, err)
		}
		if got != pt {
			t.Fatalf("round-trip mismatch: got %q want %q", got, pt)
		}
	}
}

// AES-GCM uses a random nonce, so the same plaintext must not encrypt to the
// same ciphertext twice. Equal ciphertexts would betray a fixed/missing nonce.
func TestEncrypt_NonceIsRandom(t *testing.T) {
	withKey(t)
	a, err := Encrypt("same")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encrypt("same")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext")
	}
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	withKey(t)
	ct, err := Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	other := make([]byte, 32)
	other[0] = 1 // guaranteed different from the random key
	SetEncryptionKey(other)
	if _, err := Decrypt(ct); err == nil {
		t.Fatal("decrypt with wrong key must fail, got nil error")
	}
}

func TestDecrypt_TamperedCiphertextFails(t *testing.T) {
	withKey(t)
	ct, err := Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF // flip a bit in the GCM tag region
	if _, err := Decrypt(base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("decrypt of tampered ciphertext must fail (GCM auth), got nil error")
	}
}

func TestDecrypt_RejectsMalformedInput(t *testing.T) {
	withKey(t)
	if _, err := Decrypt("not base64!!!"); err == nil {
		t.Fatal("decrypt of non-base64 must fail")
	}
	// Valid base64 but shorter than the GCM nonce.
	short := base64.StdEncoding.EncodeToString([]byte("tiny"))
	if _, err := Decrypt(short); err == nil {
		t.Fatal("decrypt of too-short ciphertext must fail")
	}
}

func TestEncrypt_RejectsBadKeyLength(t *testing.T) {
	prev := encryptionKey
	t.Cleanup(func() { encryptionKey = prev })
	SetEncryptionKey([]byte("17-bytes-long-key")) // not 16/24/32
	if _, err := Encrypt("secret"); err == nil {
		t.Fatal("encrypt with invalid AES key length must fail")
	}
}
