package media

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/py"
)

const (
	FilePrefix   = "/media/file/"
	linkKeyBytes = 32
)

type Links struct {
	key     string
	keyPath string
	mu      sync.Mutex
	secret  []byte
}

func NewLinks(linkKey, keyPath string) *Links {
	return &Links{key: linkKey, keyPath: keyPath}
}

func trimKey(b []byte) []byte { return bytes.Trim(b, " \t\n\r\v\f") }

func (l *Links) secretKey() ([]byte, error) {
	if l.key != "" {
		return []byte(l.key), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.secret != nil {
		return l.secret, nil
	}
	secret, err := l.loadOrCreate()
	if err != nil {
		return nil, err
	}
	l.secret = secret
	return secret, nil
}

func (l *Links) loadOrCreate() ([]byte, error) {
	data, err := os.ReadFile(l.keyPath)
	if err == nil {
		return trimKey(data), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	raw := make([]byte, linkKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	fresh := []byte(hex.EncodeToString(raw))
	if err := os.MkdirAll(filepath.Dir(l.keyPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(l.keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		data, err := os.ReadFile(l.keyPath)
		if err != nil {
			return nil, err
		}
		return trimKey(data), nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(fresh); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return fresh, nil
}

func signText(secret []byte, rel, exp string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(rel + "\n" + exp))
	return hex.EncodeToString(mac.Sum(nil))
}

func Sign(secret []byte, rel string, exp int64) string {
	return signText(secret, rel, strconv.FormatInt(exp, 10))
}

func verifyWith(secret []byte, rel, exp, sig string, now float64) bool {
	n, err := py.IntString(exp)
	if err != nil {
		return false
	}
	if new(big.Float).SetInt(n).Cmp(big.NewFloat(now)) <= 0 {
		return false
	}
	want := signText(secret, rel, n.String())
	return hmac.Equal([]byte(want), []byte(py.Lower(py.Strip(sig))))
}

func (l *Links) Verify(rel, exp, sig string, now float64) bool {
	secret, err := l.secretKey()
	if err != nil {
		return false
	}
	return verifyWith(secret, rel, exp, sig, now)
}

func (l *Links) PublicURL(base, rel string, now float64, ttl int64) (string, error) {
	secret, err := l.secretKey()
	if err != nil {
		return "", err
	}
	exp := int64(now + float64(ttl))
	return base + FilePrefix + py.URLQuote(rel) + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + Sign(secret, rel, exp), nil
}

func (s Settings) VPNURL(rel string) string {
	top, rest, _ := strings.Cut(rel, "/")
	base := ""
	switch top {
	case Movies:
		base = s.MoviesURL
	case TV:
		base = s.TVURL
	}
	if base == "" {
		return ""
	}
	return base + "/" + py.URLQuote(rest)
}

func firstQuery(values []string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (l *Links) Verifier() func(*http.Request) bool {
	return func(r *http.Request) bool {
		rest, ok := strings.CutPrefix(r.URL.Path, FilePrefix)
		if !ok {
			return false
		}
		query := r.URL.Query()
		return l.Verify(rest, firstQuery(query["exp"]), firstQuery(query["sig"]), Now())
	}
}
