package oidc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

const (
	sealedPrefix = "v1:"
	plainPrefix  = "p:"
)

type sealer struct {
	aead  cipher.AEAD
	plain bool
}

func newSealer(secret string, encryptedDB bool) *sealer {
	if secret == "" {
		return &sealer{plain: encryptedDB}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("atrium-oidc-token-seal-v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &sealer{aead: aead}
}

func (s *sealer) seal(value string) string {
	if value == "" {
		return ""
	}
	if s == nil || s.aead == nil {
		if s != nil && s.plain {
			return plainPrefix + value
		}
		return ""
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(value), nil))
}

func (s *sealer) open(stored string) string {
	switch {
	case strings.HasPrefix(stored, plainPrefix):
		if s != nil && s.plain {
			return strings.TrimPrefix(stored, plainPrefix)
		}
	case strings.HasPrefix(stored, sealedPrefix):
		raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, sealedPrefix))
		if err != nil || s == nil || s.aead == nil || len(raw) < s.aead.NonceSize() {
			return ""
		}
		plain, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], nil)
		if err == nil {
			return string(plain)
		}
	}
	return ""
}
