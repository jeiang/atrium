package mail

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyChatKey  = "mail_chat_key"
	KeyEmbedKey = "mail_embed_key"

	EndpointChat  = "chat"
	EndpointEmbed = "embed"
)

func init() {
	for _, key := range []string{KeyChatKey, KeyEmbedKey} {
		settings.MustRegister(settings.Def{Key: key, Module: module, Type: settings.String, Class: settings.Secret, Secret: true, RequiresEncryptedDB: true})
	}
}

type Endpoint struct {
	Kind, URL, Model, key string
}

func (e Endpoint) fingerprint() string {
	sum := sha256.Sum256([]byte(e.URL + "\x00" + e.Model + "\x00" + e.key))
	return hex.EncodeToString(sum[:8])
}

func Hosted(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
	}
	return strings.Contains(host, ".")
}

func Endpoints(ctx context.Context, q Querier) ([]Endpoint, error) {
	r := Read(ctx, q)
	chat, embed := r.Text(KeyChatModel), r.Text(KeyEmbedModel)
	if r.Err() != nil {
		return nil, r.Err()
	}
	store := secrets.NewIn(nil, readOnly{q}, nil)
	out := []Endpoint{{Kind: EndpointChat, Model: chat}, {Kind: EndpointEmbed, Model: embed}}
	for i, pair := range [][2]string{{KeyChatURL, KeyChatKey}, {KeyEmbedURL, KeyEmbedKey}} {
		u, err := urlOf(ctx, q, pair[0])
		if err != nil {
			return nil, err
		}
		key, err := store.Value(ctx, pair[1])
		if err != nil {
			return nil, err
		}
		out[i].URL, out[i].key = u, key
	}
	return out, nil
}

func withKeys(ctx context.Context, endpoints []Endpoint) context.Context {
	for _, e := range endpoints {
		ctx = httpx.WithBearer(ctx, e.URL, e.key)
	}
	return ctx
}

type ProbeResult struct {
	Kind      string   `json:"kind"`
	URL       string   `json:"url"`
	Model     string   `json:"model"`
	OK        bool     `json:"ok"`
	Reason    string   `json:"reason"`
	Dimension int      `json:"dimension,omitempty"`
	Models    []string `json:"models"`
	Hosted    bool     `json:"hosted"`
}

func discover(ctx context.Context, base string) []string {
	text, err := httpx.GetText(ctx, base+"/models", 10*time.Second)
	if err != nil {
		return nil
	}
	var reply struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(text), &reply) != nil {
		return nil
	}
	ids := make([]string, 0, len(reply.Data))
	for _, m := range reply.Data {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids
}

var probeSchema = map[string]any{
	"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
	"required": []string{"ok"}, "additionalProperties": false,
}

func probeChat(ctx context.Context, e Endpoint) (string, int) {
	raw, err := llm.ChatJSON(llm.WithOptions(ctx, llm.Options{Model: e.Model, MaxTokens: 32}), e.URL, "Reply with JSON only.", `Return {"ok": true}.`, probeSchema, 32)
	if err != nil {
		return httpx.Message(err), 0
	}
	var answer struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(raw, &answer) != nil || answer.OK == nil {
		return "the reply did not follow the JSON schema", 0
	}
	return "", 0
}

func probeEmbed(ctx context.Context, e Endpoint) (string, int) {
	raw, err := httpx.PostJSON(ctx, e.URL+"/embeddings", map[string]any{"model": e.Model, "input": []string{"atrium probe"}})
	if err != nil {
		return httpx.Message(err), 0
	}
	var reply struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &reply) != nil || len(reply.Data) != 1 || len(reply.Data[0].Embedding) == 0 {
		return "the reply has no embedding", 0
	}
	dim := len(reply.Data[0].Embedding)
	if dim != config.EmbedDim {
		return fmt.Sprintf("the embedding has %d dimensions; the index stores %d", dim, config.EmbedDim), dim
	}
	return "", dim
}

func ProbeEndpoints(ctx context.Context, d *db.DB, now float64) ([]ProbeResult, error) {
	endpoints, err := Endpoints(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	ctx = withKeys(ctx, endpoints)
	out := make([]ProbeResult, 0, len(endpoints))
	for _, e := range endpoints {
		res := ProbeResult{Kind: e.Kind, URL: e.URL, Model: e.Model, Hosted: Hosted(e.URL)}
		if e.URL == "" {
			res.Reason = "no URL configured"
		} else {
			res.Models = discover(ctx, e.URL)
			if e.Kind == EndpointChat {
				res.Reason, res.Dimension = probeChat(ctx, e)
			} else {
				res.Reason, res.Dimension = probeEmbed(ctx, e)
			}
		}
		res.OK = res.Reason == ""
		if res.Models == nil {
			res.Models = []string{}
		}
		models, _ := json.Marshal(res.Models)
		if _, err := d.Write.ExecContext(ctx, "INSERT INTO endpoint_probes (kind, fingerprint, ok, reason, dimension, models, probed_at) VALUES (?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(kind) DO UPDATE SET fingerprint = excluded.fingerprint, ok = excluded.ok, reason = excluded.reason, "+
			"dimension = excluded.dimension, models = excluded.models, probed_at = excluded.probed_at",
			e.Kind, e.fingerprint(), res.OK, res.Reason, nullDim(res.Dimension), string(models), now); err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

func nullDim(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func ProbeFailures(ctx context.Context, q Querier) (string, error) {
	endpoints, err := Endpoints(ctx, q)
	if err != nil {
		return "", err
	}
	var reasons []string
	for _, e := range endpoints {
		var fingerprint, reason string
		var ok bool
		err := q.QueryRowContext(ctx, "SELECT fingerprint, ok, reason FROM endpoint_probes WHERE kind = ?", e.Kind).Scan(&fingerprint, &ok, &reason)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		if fingerprint == e.fingerprint() && !ok {
			reasons = append(reasons, e.Kind+" endpoint probe failed: "+reason)
		}
	}
	return strings.Join(reasons, "; "), nil
}
