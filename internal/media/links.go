package media

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const FilePrefix = "/media/file/"

const linkKeyTTL = 30 * time.Second

var errEmptyLinkKey = errors.New("media: the link key is empty")

type Links struct {
	d          *db.DB
	legacyPath string
	mu         sync.Mutex
	key        []byte
	loaded     time.Time
}

func NewLinks(d *db.DB, legacyPath string) *Links {
	return &Links{d: d, legacyPath: legacyPath}
}

func trimKey(b []byte) []byte { return bytes.Trim(b, " \t\n\r\v\f") }

func (l *Links) cached() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.key != nil && time.Since(l.loaded) < linkKeyTTL {
		return l.key
	}
	return nil
}

func (l *Links) remember(key []byte) {
	l.mu.Lock()
	l.key, l.loaded = key, time.Now()
	l.mu.Unlock()
}

func (l *Links) secretKey(ctx context.Context) (key []byte, fresh bool, err error) {
	if key = l.cached(); key != nil {
		return key, false, nil
	}
	key, err = l.load(ctx)
	if err != nil {
		return nil, true, err
	}
	l.remember(key)
	return key, true, nil
}

func (l *Links) reread(ctx context.Context) []byte {
	value, err := secrets.New(l.d, l.d).Reading().Value(ctx, KeyLinkKey)
	if err != nil || value == "" {
		return nil
	}
	return []byte(value)
}

func (l *Links) load(ctx context.Context) ([]byte, error) {
	store := secrets.New(l.d, l.d)
	value, err := store.Reading().Value(ctx, KeyLinkKey)
	if err != nil {
		return nil, err
	}
	if value != "" {
		return []byte(value), nil
	}
	var legacy []byte
	adopted := false
	err = store.InTx(ctx, func(w *settings.Store) error {
		if value, err = w.Value(ctx, KeyLinkKey); err != nil || value != "" {
			return err
		}
		if data, err := os.ReadFile(l.legacyPath); err == nil {
			legacy = trimKey(data)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if len(legacy) > 0 {
			if adopted, err = w.Adopt(ctx, KeyLinkKey, string(legacy)); err != nil {
				return err
			}
		}
		value, err = w.Generate(ctx, KeyLinkKey)
		return err
	})
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, errEmptyLinkKey
	}
	if adopted {
		if err := os.Remove(l.legacyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("the retired media link key file could not be removed", logx.Err, err)
		}
	}
	return []byte(value), nil
}

func urlQuote(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == '~' || c == '/' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
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
	n, ok := new(big.Int).SetString(strings.TrimSpace(exp), 10)
	if !ok {
		return false
	}
	if new(big.Float).SetInt(n).Cmp(big.NewFloat(now)) <= 0 {
		return false
	}
	want := signText(secret, rel, n.String())
	return hmac.Equal([]byte(want), []byte(strings.ToLower(strings.TrimSpace(sig))))
}

func (l *Links) Verify(ctx context.Context, rel, exp, sig string, now float64) bool {
	secret, fresh, err := l.secretKey(ctx)
	if err != nil || len(secret) == 0 {
		return false
	}
	if verifyWith(secret, rel, exp, sig, now) {
		return true
	}
	if fresh {
		return false
	}
	again := l.reread(ctx)
	if len(again) == 0 || bytes.Equal(again, secret) {
		return false
	}
	l.remember(again)
	return verifyWith(again, rel, exp, sig, now)
}

func (l *Links) PublicURL(ctx context.Context, base, rel string, now float64, ttl int64) (string, error) {
	secret, _, err := l.secretKey(ctx)
	if err != nil {
		return "", err
	}
	if len(secret) == 0 {
		return "", errEmptyLinkKey
	}
	exp := int64(now + float64(ttl))
	return base + FilePrefix + urlQuote(rel) + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + Sign(secret, rel, exp), nil
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
	return base + "/" + urlQuote(rest)
}

func QueryValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func (l *Links) Verifier() func(*http.Request) bool {
	return func(r *http.Request) bool {
		rest, ok := strings.CutPrefix(r.URL.Path, FilePrefix)
		if !ok {
			return false
		}
		query := r.URL.Query()
		return l.Verify(r.Context(), rest, QueryValue(query["exp"]), QueryValue(query["sig"]), Now())
	}
}
