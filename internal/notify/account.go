package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
)

type AccountError string

func (e AccountError) Error() string { return string(e) }

func baseURL(ctx context.Context, q db.Querier) (*url.URL, error) {
	raw, err := text(ctx, q, KeyURL)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if raw == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, AccountError(KeyURL + " must be an http or https URL")
	}
	return u, nil
}

func endpoint(base *url.URL, path string) string {
	return strings.TrimSuffix(base.String(), "/") + path
}

func IsNtfySh(base *url.URL) bool { return strings.EqualFold(base.Hostname(), "ntfy.sh") }

func call(ctx context.Context, q db.Querier, req *http.Request) (*http.Response, error) {
	timeout, err := timeoutOf(ctx, q)
	if err != nil {
		return nil, err
	}
	client, err := newClient(ctx, q, timeout)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	return client.Do(req)
}

func mint(ctx context.Context, q db.Querier, base *url.URL, user, password, label string) (string, error) {
	body := `{"label":` + jsonString(label) + `,"expires":0}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(base, "/v1/account/token"), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := call(ctx, q, req)
	if err != nil {
		return "", AccountError("ntfy unreachable: " + errorClass(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", AccountError("ntfy refused the username or password")
	}
	if resp.StatusCode >= 400 {
		return "", AccountError(fmt.Sprintf("ntfy %d while creating a token", resp.StatusCode))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil || out.Token == "" {
		return "", AccountError("ntfy returned no token")
	}
	return out.Token, nil
}

func revoke(ctx context.Context, q db.Querier, base *url.URL, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint(base, "/v1/account/token"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Token", token)
	resp, err := call(ctx, q, req)
	if err != nil {
		return AccountError("ntfy unreachable: " + errorClass(err))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusUnauthorized {
		return AccountError(fmt.Sprintf("ntfy %d while revoking the token", resp.StatusCode))
	}
	return nil
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func Connect(ctx context.Context, d *db.DB, user, password, by string) error {
	if strings.TrimSpace(user) == "" || password == "" {
		return AccountError("username and password are required")
	}
	if !d.Encrypted() {
		return secrets.EncryptionRequired(TokenKey(Assistant))
	}
	base, err := baseURL(ctx, d.Read)
	if err != nil {
		return err
	}
	tokens := map[string]string{}
	for _, name := range Channels {
		token, err := mint(ctx, d.Read, base, user, password, "atrium "+name)
		if err != nil {
			return err
		}
		tokens[name] = token
	}
	store := secrets.New(d, d)
	for _, name := range Channels {
		old, err := store.Value(ctx, TokenKey(name))
		if err != nil {
			return err
		}
		if err := store.Set(ctx, TokenKey(name), tokens[name], by); err != nil {
			return err
		}
		if old != "" {
			_ = revoke(ctx, d.Read, base, old)
		}
	}
	return ProbeAll(ctx, d)
}

func ResetChannel(ctx context.Context, d *db.DB, name string) error {
	if !known(name) {
		return ChannelError("unknown channel " + name)
	}
	store := secrets.New(d, d)
	token, err := store.Value(ctx, TokenKey(name))
	if err != nil {
		return err
	}
	if token != "" {
		base, err := baseURL(ctx, d.Read)
		if err != nil {
			return err
		}
		if err := revoke(ctx, d.Read, base, token); err != nil {
			return err
		}
		if err := store.Delete(ctx, TokenKey(name)); err != nil {
			return err
		}
	}
	topic, err := NewTopic()
	if err != nil {
		return err
	}
	_, err = d.Write.ExecContext(ctx, "INSERT INTO ntfy_channels (name, topic, created_at) VALUES (?, ?, ?) "+
		"ON CONFLICT(name) DO UPDATE SET topic = excluded.topic, privacy = 'unknown', probed_at = NULL, failures = 0, last_error = NULL, last_ok_at = NULL",
		name, topic, stamp())
	return err
}

func probe(ctx context.Context, q db.Querier, base *url.URL, topic string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint(base, "/"+url.PathEscape(topic)+"/json?poll=1&since=none"), nil)
	if err != nil {
		return Unknown, err
	}
	resp, err := call(ctx, q, req)
	if err != nil {
		return Unknown, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Private, nil
	case resp.StatusCode < 300:
		return Public, nil
	}
	return Unknown, nil
}

func ProbeAll(ctx context.Context, d *db.DB) error {
	base, err := baseURL(ctx, d.Read)
	if err != nil {
		return err
	}
	list, err := ListChannels(ctx, d.Write)
	if err != nil {
		return err
	}
	for _, c := range list {
		privacy, err := probe(ctx, d.Read, base, c.Topic)
		if err != nil {
			return err
		}
		if _, err := d.Write.ExecContext(ctx, "UPDATE ntfy_channels SET privacy = ?, probed_at = ? WHERE name = ?", privacy, stamp(), c.Name); err != nil {
			return err
		}
	}
	return nil
}

func probeUnknown(ctx context.Context, d *db.DB) error {
	if raw, err := text(ctx, d.Read, KeyURL); err != nil || raw == "" {
		return err
	}
	if err := EnsureChannels(ctx, d.Write); err != nil {
		return err
	}
	var n int
	if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM ntfy_channels WHERE probed_at IS NULL").Scan(&n); err != nil || n == 0 {
		return err
	}
	return ProbeAll(ctx, d)
}
