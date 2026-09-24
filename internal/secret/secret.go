// Package secret encrypts credentials (Grafana tokens, SMTP passwords) at
// rest with AES-256-GCM.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const prefix = "v1:"

// Box seals and opens secrets with a single key.
type Box struct {
	aead cipher.AEAD
}

// New derives a box from key material of any length.
func New(material []byte) (*Box, error) {
	if len(material) == 0 {
		return nil, errors.New("empty secret key")
	}
	sum := sha256.Sum256(material)
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// LoadOrCreate uses the explicit key when given, otherwise a random key file
// inside dataDir that is created on first start.
func LoadOrCreate(explicit, dataDir string) (*Box, error) {
	if strings.TrimSpace(explicit) != "" {
		return New([]byte(strings.TrimSpace(explicit)))
	}
	path := filepath.Join(dataDir, "secret.key")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		enc := []byte(base64.StdEncoding.EncodeToString(key))
		if err := os.WriteFile(path, enc, 0o600); err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
		raw = enc
	} else if err != nil {
		return nil, err
	}
	return New([]byte(strings.TrimSpace(string(raw))))
}

// Seal encrypts plaintext. An empty string stays empty.
func (b *Box) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a sealed value.
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, prefix) {
		return "", errors.New("value is not encrypted with a known scheme")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("ciphertext too short")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("could not decrypt a stored secret; was PANELPOST_SECRET_KEY changed?")
	}
	return string(plain), nil
}
