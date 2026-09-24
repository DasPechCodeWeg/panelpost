package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OfflinePrefix marks vendor-signed keys.
const OfflinePrefix = "PP1-"

// embeddedPublicKeys are the vendor's Ed25519 verification keys, comma
// separated. They are public by design. Compiling them in, instead of reading
// them from configuration, means a key cannot be "validated" by swapping in a
// different public key. To rotate, add the new key here and drop the old one
// once its licences have expired. Test builds may override the list with
//
//	-ldflags "-X github.com/DasPechCodeWeg/panelpost/internal/license.embeddedPublicKeys=BASE64[,BASE64]"
var embeddedPublicKeys = "PWBbBDjN8zUpGbsyp637AMKUvrLlMnesbNNTzCXlZgk="

// Payload is the signed content of an offline key.
type Payload struct {
	Version   int    `json:"v"`
	ID        string `json:"id"`
	Tier      Tier   `json:"tier"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp,omitempty"` // unix seconds, 0 = perpetual
}

// Errors returned by offline verification.
var (
	ErrMalformed   = errors.New("this is not a valid Panelpost license key")
	ErrSignature   = errors.New("license key signature does not match; the key was changed or issued by someone else")
	ErrExpired     = errors.New("license key has expired")
	ErrNoVerifiers = errors.New("this build has no license verification keys; use an official release")
)

// GenerateKeyPair creates a new Ed25519 signing key pair, base64 encoded.
func GenerateKeyPair() (publicKey, privateKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(pub), base64.StdEncoding.EncodeToString(priv), nil
}

// ParsePrivateKey decodes a base64 Ed25519 private key.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("private key must be a base64 encoded Ed25519 key")
	}
	return ed25519.PrivateKey(raw), nil
}

// ParsePublicKeys decodes a comma separated list of base64 public keys.
func ParsePublicKeys(s string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(part)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid public key %q", part)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys, nil
}

// EmbeddedPublicKeys returns the verification keys compiled into this build.
func EmbeddedPublicKeys() []ed25519.PublicKey {
	keys, err := ParsePublicKeys(embeddedPublicKeys)
	if err != nil {
		return nil
	}
	return keys
}

// Sign issues an offline key.
func Sign(priv ed25519.PrivateKey, p Payload) (string, error) {
	if _, ok := ParseTier(string(p.Tier)); !ok || p.Tier == Free {
		return "", fmt.Errorf("tier must be pro or business, got %q", p.Tier)
	}
	if strings.TrimSpace(p.Name) == "" {
		return "", errors.New("licensee name is required")
	}
	p.Version = 1
	if p.ID == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		p.ID = fmt.Sprintf("%x", b)
	}
	if p.IssuedAt == 0 {
		p.IssuedAt = time.Now().Unix()
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, body)
	enc := base64.RawURLEncoding
	return OfflinePrefix + enc.EncodeToString(body) + "." + enc.EncodeToString(sig), nil
}

// VerifyOffline checks an offline key against trusted public keys.
func VerifyOffline(key string, trusted []ed25519.PublicKey, now time.Time) (License, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, OfflinePrefix) {
		return License{}, ErrMalformed
	}
	if len(trusted) == 0 {
		return License{}, ErrNoVerifiers
	}
	parts := strings.Split(strings.TrimPrefix(key, OfflinePrefix), ".")
	if len(parts) != 2 {
		return License{}, ErrMalformed
	}
	enc := base64.RawURLEncoding
	body, err1 := enc.DecodeString(parts[0])
	sig, err2 := enc.DecodeString(parts[1])
	if err1 != nil || err2 != nil || len(sig) != ed25519.SignatureSize {
		return License{}, ErrMalformed
	}
	verified := false
	for _, pub := range trusted {
		if ed25519.Verify(pub, body, sig) {
			verified = true
			break
		}
	}
	if !verified {
		return License{}, ErrSignature
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return License{}, ErrMalformed
	}
	tier, ok := ParseTier(string(p.Tier))
	if !ok || p.Version != 1 {
		return License{}, ErrMalformed
	}
	lic := License{Tier: tier, ID: p.ID, Licensee: p.Name, Email: p.Email, Source: "offline"}
	if p.ExpiresAt > 0 {
		lic.ExpiresAt = time.Unix(p.ExpiresAt, 0).UTC()
		if now.After(lic.ExpiresAt) {
			return lic, ErrExpired
		}
	}
	return lic, nil
}
