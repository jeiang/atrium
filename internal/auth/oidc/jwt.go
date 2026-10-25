package oidc

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	maxTokenBytes = 64 << 10
	clockLeeway   = 60 * time.Second
)

var ErrToken = errors.New("oidc: invalid id token")

func tokenError(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrToken, fmt.Sprintf(format, a...))
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type namedKey struct {
	kid string
	alg string
	key crypto.PublicKey
}

func decodeBig(s string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("bad number")
	}
	return new(big.Int).SetBytes(raw), nil
}

func parseJWKS(data []byte) ([]namedKey, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("oidc: jwks is not JSON: %w", err)
	}
	var out []namedKey
	for _, k := range doc.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		key, err := k.public()
		if err != nil {
			continue
		}
		out = append(out, namedKey{kid: k.Kid, alg: k.Alg, key: key})
	}
	if len(out) == 0 {
		return nil, errors.New("oidc: jwks holds no usable signing key")
	}
	return out, nil
}

func (k jwk) public() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := decodeBig(k.N)
		if err != nil {
			return nil, err
		}
		e, err := decodeBig(k.E)
		if err != nil || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return nil, errors.New("bad exponent")
		}
		if n.BitLen() < 2048 {
			return nil, errors.New("rsa key too small")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, errors.New("unsupported curve")
		}
		x, err := decodeBig(k.X)
		if err != nil {
			return nil, err
		}
		y, err := decodeBig(k.Y)
		if err != nil {
			return nil, err
		}
		if !curve.IsOnCurve(x, y) {
			return nil, errors.New("point not on curve")
		}
		return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, errors.New("unsupported curve")
		}
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("bad key")
		}
		return ed25519.PublicKey(raw), nil
	}
	return nil, errors.New("unsupported key type")
}

type header struct {
	Alg  string          `json:"alg"`
	Kid  string          `json:"kid"`
	Crit json.RawMessage `json:"crit"`
}

type rawToken struct {
	header header
	claims map[string]any
	signed string
	sig    []byte
}

func splitToken(raw string) (rawToken, error) {
	if len(raw) > maxTokenBytes {
		return rawToken{}, tokenError("token too large")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return rawToken{}, tokenError("not a compact JWS")
	}
	var t rawToken
	head, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(head, &t.header) != nil {
		return rawToken{}, tokenError("unreadable header")
	}
	if len(t.header.Crit) > 0 {
		return rawToken{}, tokenError("critical headers are not supported")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return rawToken{}, tokenError("unreadable payload")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&t.claims); err != nil || t.claims == nil {
		return rawToken{}, tokenError("unreadable payload")
	}
	if t.sig, err = base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return rawToken{}, tokenError("unreadable signature")
	}
	t.signed = parts[0] + "." + parts[1]
	return t, nil
}

func hashFor(alg string) (crypto.Hash, bool) {
	switch alg {
	case "RS256", "PS256", "ES256":
		return crypto.SHA256, true
	case "RS384", "PS384", "ES384":
		return crypto.SHA384, true
	case "RS512", "PS512", "ES512":
		return crypto.SHA512, true
	}
	return 0, false
}

func verifySignature(alg string, key crypto.PublicKey, signed string, sig []byte) bool {
	if alg == "EdDSA" {
		pub, ok := key.(ed25519.PublicKey)
		return ok && ed25519.Verify(pub, []byte(signed), sig)
	}
	h, ok := hashFor(alg)
	if !ok {
		return false
	}
	hasher := h.New()
	hasher.Write([]byte(signed))
	digest := hasher.Sum(nil)
	switch pub := key.(type) {
	case *rsa.PublicKey:
		switch alg[:2] {
		case "RS":
			return rsa.VerifyPKCS1v15(pub, h, digest, sig) == nil
		case "PS":
			return rsa.VerifyPSS(pub, h, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
		}
	case *ecdsa.PublicKey:
		if alg[:2] != "ES" {
			return false
		}
		size := (pub.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*size {
			return false
		}
		r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
		return ecdsa.Verify(pub, digest, r, s)
	}
	return false
}

func (k namedKey) allows(alg string) bool {
	if k.alg != "" && k.alg != alg {
		return false
	}
	switch k.key.(type) {
	case *rsa.PublicKey:
		return strings.HasPrefix(alg, "RS") || strings.HasPrefix(alg, "PS")
	case *ecdsa.PublicKey:
		return strings.HasPrefix(alg, "ES")
	case ed25519.PublicKey:
		return alg == "EdDSA"
	}
	return false
}

func supportedAlg(alg string) bool {
	if alg == "EdDSA" {
		return true
	}
	_, ok := hashFor(alg)
	return ok
}

type claimRules struct {
	issuer   string
	audience string
	nonce    string
	subject  string
	now      time.Time
}

func (t rawToken) checkSignature(keys []namedKey) error {
	if !supportedAlg(t.header.Alg) {
		return tokenError("algorithm %q is not accepted", t.header.Alg)
	}
	for _, k := range keys {
		if t.header.Kid != "" && k.kid != "" && k.kid != t.header.Kid {
			continue
		}
		if k.allows(t.header.Alg) && verifySignature(t.header.Alg, k.key, t.signed, t.sig) {
			return nil
		}
	}
	return tokenError("signature does not match a published key")
}

func (t rawToken) hasKey(keys []namedKey) bool {
	if t.header.Kid == "" {
		return true
	}
	for _, k := range keys {
		if k.kid == t.header.Kid {
			return true
		}
	}
	return false
}

func numeric(v any) (time.Time, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return time.Time{}, false
	}
	f, err := n.Float64()
	if err != nil || f <= 0 || f > 1e12 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

func (t rawToken) checkClaims(r claimRules) error {
	iss, _ := t.claims["iss"].(string)
	if iss == "" || iss != r.issuer {
		return tokenError("issuer mismatch")
	}
	if err := audienceOK(t.claims, r.audience); err != nil {
		return err
	}
	exp, ok := numeric(t.claims["exp"])
	if !ok {
		return tokenError("missing exp")
	}
	if !r.now.Before(exp.Add(clockLeeway)) {
		return tokenError("expired")
	}
	if v, present := t.claims["nbf"]; present {
		nbf, ok := numeric(v)
		if !ok || r.now.Add(clockLeeway).Before(nbf) {
			return tokenError("not yet valid")
		}
	}
	if v, present := t.claims["iat"]; present {
		iat, ok := numeric(v)
		if !ok || r.now.Add(clockLeeway).Before(iat) {
			return tokenError("issued in the future")
		}
	}
	sub, _ := t.claims["sub"].(string)
	if sub == "" || len(sub) > 255 {
		return tokenError("missing sub")
	}
	if r.subject != "" && sub != r.subject {
		return tokenError("subject changed")
	}
	if r.nonce != "" {
		got, _ := t.claims["nonce"].(string)
		if subtle.ConstantTimeCompare([]byte(got), []byte(r.nonce)) != 1 {
			return tokenError("nonce mismatch")
		}
	}
	return nil
}

func audienceOK(claims map[string]any, client string) error {
	switch aud := claims["aud"].(type) {
	case string:
		if aud == client {
			return nil
		}
	case []any:
		found := false
		for _, a := range aud {
			if s, ok := a.(string); ok && s == client {
				found = true
			}
		}
		if found {
			if azp, _ := claims["azp"].(string); len(aud) > 1 && azp != client {
				return tokenError("authorized party mismatch")
			}
			return nil
		}
	}
	return tokenError("audience mismatch")
}
