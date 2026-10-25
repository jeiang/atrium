package oidctest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type User struct {
	Sub           string
	Email         string
	EmailVerified bool
	Name          string
	Hosted        string
	Groups        []string
}

type Options struct {
	IssuerPath   string
	NoS256       bool
	GroupsIn     string
	GroupsClaim  string
	GroupPrefix  string
	AuthPost     bool
	NoRefresh    bool
	NoOffline    bool
	Alg          string
	ClientID     string
	ClientSecret string
}

type Provider struct {
	t      *testing.T
	opts   Options
	server *httptest.Server

	mu          sync.Mutex
	users       map[string]*User
	approve     string
	keys        []signer
	codes       map[string]grant
	refresh     map[string]string
	access      map[string]string
	auths       []url.Values
	tokenCalls  []url.Values
	Down        bool
	Tamper      func(claims map[string]any)
	Forge       string
	RevokeAll   bool
	Expired     bool
	RefreshCall int
	Redirect    string
	keyN        int
}

type grant struct {
	user      string
	nonce     string
	challenge string
	redirect  string
	scope     string
}

type signer struct {
	kid string
	rsa *rsa.PrivateKey
	ec  *ecdsa.PrivateKey
}

var (
	rsaOnce sync.Once
	rsaKeys []*rsa.PrivateKey
)

func rsaKey(i int) *rsa.PrivateKey {
	rsaOnce.Do(func() {
		for range 3 {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			rsaKeys = append(rsaKeys, k)
		}
	})
	return rsaKeys[i%len(rsaKeys)]
}

func New(t *testing.T, opts Options) *Provider {
	t.Helper()
	if opts.GroupsIn == "" {
		opts.GroupsIn = "token"
	}
	if opts.GroupsClaim == "" {
		opts.GroupsClaim = "groups"
	}
	if opts.Alg == "" {
		opts.Alg = "RS256"
	}
	if opts.ClientID == "" {
		opts.ClientID = "atrium"
	}
	p := &Provider{t: t, opts: opts, users: map[string]*User{}, codes: map[string]grant{}, refresh: map[string]string{}, access: map[string]string{}}
	p.addKey()
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *Provider) Issuer() string { return p.server.URL + p.opts.IssuerPath }

func (p *Provider) ClientID() string { return p.opts.ClientID }

func (p *Provider) AddUser(u User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u.Sub == "" {
		u.Sub = "sub-" + u.Name
	}
	p.users[u.Name] = &u
}

func (p *Provider) SetGroups(name string, groups ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[name].Groups = groups
}

func (p *Provider) Approve(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.approve = name
}

func (p *Provider) Authorizations() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.auths...)
}

func (p *Provider) TokenCalls() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.tokenCalls...)
}

func (p *Provider) RotateKey(dropOld bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if dropOld {
		p.keys = nil
	}
	p.addKeyLocked()
}

func (p *Provider) addKey() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.addKeyLocked()
}

func (p *Provider) addKeyLocked() {
	s := signer{kid: fmt.Sprintf("key-%d-%d", len(p.keys), time.Now().UnixNano())}
	if p.opts.Alg == "ES256" {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		s.ec = k
	} else {
		s.rsa = rsaKey(p.keyN)
		p.keyN++
	}
	p.keys = append(p.keys, s)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	down := p.Down
	p.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(p.opts.IssuerPath, "/"))
	switch path {
	case "/.well-known/openid-configuration":
		p.discovery(w)
	case "/authorize":
		p.authorize(w, r)
	case "/token":
		p.token(w, r)
	case "/jwks":
		p.jwks(w)
	case "/userinfo":
		p.userinfo(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *Provider) discovery(w http.ResponseWriter) {
	base := strings.TrimSuffix(p.Issuer(), "/")
	methods := []string{"client_secret_basic", "client_secret_post"}
	if p.opts.AuthPost {
		methods = []string{"client_secret_post"}
	}
	doc := map[string]any{
		"issuer":                                p.Issuer(),
		"authorization_endpoint":                base + "/authorize",
		"token_endpoint":                        base + "/token",
		"jwks_uri":                              base + "/jwks",
		"userinfo_endpoint":                     base + "/userinfo",
		"token_endpoint_auth_methods_supported": methods,
		"scopes_supported":                      []string{"openid", "profile", "email", "groups"},
		"id_token_signing_alg_values_supported": []string{p.opts.Alg},
	}
	if !p.opts.NoOffline {
		doc["scopes_supported"] = []string{"openid", "profile", "email", "groups", "offline_access"}
	}
	if !p.opts.NoS256 {
		doc["code_challenge_methods_supported"] = []string{"plain", "S256"}
	} else {
		doc["code_challenge_methods_supported"] = []string{"plain"}
	}
	writeJSON(w, doc)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return b64(b)
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.auths = append(p.auths, q)
	if q.Get("client_id") != p.opts.ClientID || q.Get("response_type") != "code" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	redirect := q.Get("redirect_uri")
	if p.Redirect != "" && redirect != p.Redirect {
		http.Error(w, "redirect_uri mismatch", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "pkce required", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	bq := back.Query()
	bq.Set("state", q.Get("state"))
	if p.approve == "" {
		bq.Set("error", "access_denied")
	} else {
		code := randomString()
		p.codes[code] = grant{user: p.approve, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: redirect, scope: q.Get("scope")}
		bq.Set("code", code)
	}
	back.RawQuery = bq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (p *Provider) clientOK(r *http.Request) bool {
	if p.opts.ClientSecret == "" {
		return r.PostForm.Get("client_id") == p.opts.ClientID
	}
	if id, secret, ok := r.BasicAuth(); ok {
		cid, _ := url.QueryUnescape(id)
		sec, _ := url.QueryUnescape(secret)
		return !p.opts.AuthPost && cid == p.opts.ClientID && sec == p.opts.ClientSecret
	}
	return r.PostForm.Get("client_id") == p.opts.ClientID && r.PostForm.Get("client_secret") == p.opts.ClientSecret
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": code})
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenCalls = append(p.tokenCalls, r.PostForm)
	if !p.clientOK(r) {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		g, ok := p.codes[r.PostForm.Get("code")]
		delete(p.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || g.redirect != r.PostForm.Get("redirect_uri") || b64(sum[:]) != g.challenge {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		p.reply(w, g.user, g.nonce, g.scope, true)
	case "refresh_token":
		p.RefreshCall++
		name, ok := p.refresh[r.PostForm.Get("refresh_token")]
		if !ok || p.RevokeAll {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		p.reply(w, name, "", "openid", false)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (p *Provider) groupsFor(u *User) []string {
	out := make([]string, len(u.Groups))
	for i, g := range u.Groups {
		out[i] = p.opts.GroupPrefix + g
	}
	return out
}

func setPath(claims map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := claims
	for _, part := range parts[:len(parts)-1] {
		next, _ := cur[part].(map[string]any)
		if next == nil {
			next = map[string]any{}
			cur[part] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

func (p *Provider) userClaims(u *User, withGroups bool) map[string]any {
	claims := map[string]any{"sub": u.Sub}
	if u.Email != "" {
		claims["email"] = u.Email
		claims["email_verified"] = u.EmailVerified
	}
	if u.Name != "" {
		claims["name"] = u.Name
	}
	if u.Hosted != "" {
		claims["hd"] = u.Hosted
	}
	if withGroups {
		setPath(claims, p.opts.GroupsClaim, p.groupsFor(u))
	}
	return claims
}

func (p *Provider) reply(w http.ResponseWriter, name, nonce, scope string, first bool) {
	u := p.users[name]
	claims := p.userClaims(u, p.opts.GroupsIn == "token" || p.opts.GroupsIn == "both")
	now := time.Now()
	claims["iss"] = p.Issuer()
	claims["aud"] = p.opts.ClientID
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(10 * time.Minute).Unix()
	if p.Expired {
		claims["exp"] = now.Add(-10 * time.Minute).Unix()
		claims["iat"] = now.Add(-20 * time.Minute).Unix()
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	if p.Tamper != nil {
		p.Tamper(claims)
	}
	access := "at-" + randomString()
	p.access[access] = name
	out := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "id_token": p.sign(claims)}
	if !p.opts.NoRefresh && (!first || strings.Contains(scope, "offline_access")) {
		rt := "rt-" + randomString()
		p.refresh[rt] = name
		out["refresh_token"] = rt
	}
	writeJSON(w, out)
}

func (p *Provider) sign(claims map[string]any) string {
	s := p.keys[len(p.keys)-1]
	alg := p.opts.Alg
	switch p.Forge {
	case "none":
		alg = "none"
	case "hs256":
		alg = "HS256"
	}
	head, _ := json.Marshal(map[string]string{"alg": alg, "kid": s.kid, "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signed := b64(head) + "." + b64(body)
	switch p.Forge {
	case "none":
		return signed + "."
	case "hs256":
		pub := p.pubPEMLike(s)
		mac := hmac.New(sha256.New, pub)
		mac.Write([]byte(signed))
		return signed + "." + b64(mac.Sum(nil))
	case "badsig":
		return signed + "." + b64([]byte("not a signature at all, but long enough to look like one......................................................................................................................................................................................"))
	}
	digest := sha256.Sum256([]byte(signed))
	if s.ec != nil {
		r, sg, err := ecdsa.Sign(rand.Reader, s.ec, digest[:])
		if err != nil {
			panic(err)
		}
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		sg.FillBytes(sig[32:])
		return signed + "." + b64(sig)
	}
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.rsa, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	return signed + "." + b64(sig)
}

func (p *Provider) pubPEMLike(s signer) []byte {
	if s.rsa != nil {
		return s.rsa.N.Bytes()
	}
	return ecPoint(s)
}

func ecPoint(s signer) []byte {
	b, err := s.ec.PublicKey.Bytes()
	if err != nil {
		panic(err)
	}
	return b
}

func (p *Provider) jwks(w http.ResponseWriter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var keys []map[string]any
	for _, s := range p.keys {
		if s.ec != nil {
			keys = append(keys, map[string]any{"kty": "EC", "crv": "P-256", "kid": s.kid, "use": "sig", "alg": "ES256",
				"x": b64(ecPoint(s)[1:33]), "y": b64(ecPoint(s)[33:])})
			continue
		}
		keys = append(keys, map[string]any{"kty": "RSA", "kid": s.kid, "use": "sig", "alg": "RS256",
			"n": b64(s.rsa.N.Bytes()), "e": b64(big.NewInt(int64(s.rsa.E)).Bytes())})
	}
	writeJSON(w, map[string]any{"keys": keys})
}

func (p *Provider) userinfo(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	defer p.mu.Unlock()
	name, ok := p.access[token]
	if !ok || p.RevokeAll {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(w, p.userClaims(p.users[name], p.opts.GroupsIn == "userinfo" || p.opts.GroupsIn == "both"))
}
