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

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/secrets"
)

const FilePrefix = "/media/file/"

type Links struct {
	d          *db.DB
	legacyPath string
}

func NewLinks(d *db.DB, legacyPath string) *Links {
	return &Links{d: d, legacyPath: legacyPath}
}

func trimKey(b []byte) []byte { return bytes.Trim(b, " \t\n\r\v\f") }

func (l *Links) secretKey(ctx context.Context) ([]byte, error) {
	store := secrets.New(l.d, l.d)
	value, err := store.Value(ctx, KeyLinkKey)
	if err != nil || value != "" {
		return []byte(value), err
	}
	var legacy []byte
	err = store.InTx(ctx, func(w *secrets.Store) error {
		if value, err = w.Value(ctx, KeyLinkKey); err != nil || value != "" {
			return err
		}
		if data, err := os.ReadFile(l.legacyPath); err == nil {
			legacy = trimKey(data)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if len(legacy) > 0 {
			if err := w.Adopt(ctx, KeyLinkKey, string(legacy)); err != nil {
				return err
			}
		}
		value, err = w.GeneratedKey(ctx, KeyLinkKey)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(legacy) > 0 {
		if err := os.Remove(l.legacyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("the retired media link key file could not be removed", logx.Err, err)
		}
	}
	return []byte(value), nil
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

func (l *Links) Verify(ctx context.Context, rel, exp, sig string, now float64) bool {
	secret, err := l.secretKey(ctx)
	if err != nil {
		return false
	}
	return verifyWith(secret, rel, exp, sig, now)
}

func (l *Links) PublicURL(ctx context.Context, base, rel string, now float64, ttl int64) (string, error) {
	secret, err := l.secretKey(ctx)
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
		return l.Verify(r.Context(), rest, firstQuery(query["exp"]), firstQuery(query["sig"]), Now())
	}
}
