package davmirror

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/connections"
)

type discovery struct {
	home        *url.URL
	collections []CollectionRef
	listedAt    time.Time
}

type syncOutcome struct {
	result connections.Result
	at     time.Time
}

const (
	relistAfter = time.Hour
	syncFresh   = 20 * time.Minute
)

var (
	discoveries   sync.Map
	syncOutcomes  sync.Map
	syncing       sync.Map
	transientWait = 2 * time.Second
)

func cacheKey(capability, endpoint, user, password string) string {
	sum := sha256.Sum256([]byte(capability + "\x00" + endpoint + "\x00" + user + "\x00" + password))
	return hex.EncodeToString(sum[:])
}

func jittered(d time.Duration) time.Duration {
	return d*3/4 + rand.N(d/2)
}

func stale(err error) bool {
	status, ok := errors.AsType[*StatusError](err)
	if !ok {
		return false
	}
	switch status.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return true
	}
	return false
}

func (c *Client) cached() (discovery, bool) {
	if c.cache == "" {
		return discovery{}, false
	}
	found, ok := discoveries.Load(c.cache)
	if !ok {
		return discovery{}, false
	}
	return found.(discovery), true
}

func (c *Client) keep(home *url.URL, refs []CollectionRef) {
	if c.cache != "" {
		discoveries.Store(c.cache, discovery{home: home, collections: refs, listedAt: time.Now()})
	}
}

func (c *Client) forget(err error) {
	if c.cache == "" {
		return
	}
	if _, loaded := discoveries.LoadAndDelete(c.cache); loaded {
		log.Info("dav discovery cache dropped", "reason", err.Error())
	}
}

func rememberSync(key string, result connections.Result) {
	syncOutcomes.Store(key, syncOutcome{result: result, at: time.Now()})
}

func recentSync(key string) (connections.Result, bool) {
	found, ok := syncOutcomes.Load(key)
	if !ok {
		return connections.Result{}, false
	}
	o := found.(syncOutcome)
	return o.result, time.Since(o.at) < syncFresh
}

func maskedURL(u *url.URL) string {
	segments := strings.Split(u.Path, "/")
	for i, s := range segments {
		if s != "" && strings.Trim(s, "0123456789") == "" {
			segments[i] = "<id>"
		}
	}
	return u.Scheme + "://" + u.Host + strings.Join(segments, "/")
}
