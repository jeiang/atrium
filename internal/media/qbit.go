package media

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // the BitTorrent v1 info hash is SHA-1
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"regexp"

	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/urlx"
)

const (
	TagMedia    = "media"
	TagFiled    = "filed"
	TagIgnored  = "ignored"
	TagSelected = "selected"

	PriorityNormal = 1
	PrioritySkip   = 0

	StoppedUP = "stoppedUP"
	StoppedDL = "stoppedDL"

	okReply    = "Ok."
	maxBencode = 512
)

var (
	StateLabels = map[string]string{
		"downloading": "downloading", "metaDL": "fetching metadata", "forcedMetaDL": "fetching metadata",
		"stalledDL": "stalled", "checkingDL": "checking", "forcedDL": "downloading", "queuedDL": "queued",
		"allocating": "allocating", "moving": "moving", "checkingResumeData": "checking", "uploading": "seeding",
		"forcedUP": "seeding", "stalledUP": "seeding", "queuedUP": "queued", "checkingUP": "checking",
		StoppedUP: "done", StoppedDL: "stopped", "pausedUP": "done", "pausedDL": "stopped", "error": "error",
		"missingFiles": "missing files", "unknown": "unknown",
	}

	btih = regexp.MustCompile(`^urn:btih:([0-9a-fA-F]{40}|[A-Za-z2-7]{32})$`)

	activeStates = map[string]bool{
		"downloading": true, "metaDL": true, "forcedMetaDL": true, "stalledDL": true,
		"checkingDL": true, "forcedDL": true, "queuedDL": true, "allocating": true,
		"moving": true, "checkingResumeData": true, "uploading": true, "forcedUP": true,
		"checkingUP": true,
	}
)

type QbitError string

func (e QbitError) Error() string { return string(e) }

type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Tags        string  `json:"tags"`
	ContentPath string  `json:"content_path"`
	Progress    float64 `json:"progress"`
	Size        *int64  `json:"size"`
	DLSpeed     *int64  `json:"dlspeed"`
	UpSpeed     *int64  `json:"upspeed"`
	ETA         *int64  `json:"eta"`
}

type TorrentFile struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Priority int     `json:"priority"`
	Size     *int64  `json:"size"`
	Progress float64 `json:"progress"`
}

func (t Torrent) Active() bool { return activeStates[t.State] }

func (t Torrent) HasTag(tag string) bool {
	for part := range strings.SplitSeq(t.Tags, ",") {
		if strings.TrimSpace(part) == tag {
			return true
		}
	}
	return false
}

func accepted(reply string) bool {
	reply = strings.TrimSpace(reply)
	if reply == okReply {
		return true
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(reply), &d); err != nil || d == nil {
		return false
	}
	if failures := d["failure_count"]; truthy(failures) {
		return false
	}
	total := 0.0
	for _, key := range []string{"success_count", "pending_count"} {
		v := d[key]
		n, ok := number(v)
		if !ok {
			return false
		}
		total += n
	}
	return total > 0
}

func number(v any) (float64, bool) {
	switch v := v.(type) {
	case nil:
		return 0, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func unquotePlus(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, okHi := hexValue(s[i+1])
			lo, okLo := hexValue(s[i+2])
			if okHi && okLo {
				b = append(b, hi<<4|lo)
				i += 2
				continue
			}
		}
		b = append(b, s[i])
	}
	return strings.ToValidUTF8(string(b), "\ufffd")
}

func queryValues(query, name string) []string {
	var out []string
	for pair := range strings.SplitSeq(query, "&") {
		if pair == "" {
			continue
		}
		key, value, found := strings.Cut(pair, "=")
		if !found || value == "" {
			continue
		}
		if unquotePlus(key) == name {
			out = append(out, unquotePlus(value))
		}
	}
	return out
}

func MagnetHash(magnet string) (string, bool) {
	if !strings.HasPrefix(magnet, "magnet:?") {
		return "", false
	}
	parts, err := urlx.URLSplit(magnet)
	if err != nil {
		return "", false
	}
	for _, xt := range queryValues(parts.Query, "xt") {
		loc := btih.FindStringSubmatchIndex(xt)
		if loc == nil || loc[0] != 0 {
			continue
		}
		value := xt[loc[2]:loc[3]]
		if len(value) == 40 {
			return strings.ToLower(value), true
		}
		raw, err := base32.StdEncoding.DecodeString(strings.ToUpper(value))
		if err != nil {
			return "", false
		}
		return hex.EncodeToString(raw), true
	}
	return "", false
}

type bdict map[string][3]any

func bdecode(data []byte, i, depth int) (value any, end int, ok bool) {
	if depth > maxBencode || i >= len(data) {
		return nil, 0, false
	}
	switch c := data[i]; {
	case c == 'i':
		stop := bytes.IndexByte(data[i:], 'e')
		if stop < 0 {
			return nil, 0, false
		}
		n, valid := parseInt(string(data[i+1 : i+stop]))
		if !valid {
			return nil, 0, false
		}
		return n, i + stop + 1, true
	case c == 'l':
		out := []any{}
		i++
		for i >= len(data) || data[i] != 'e' {
			v, next, ok := bdecode(data, i, depth+1)
			if !ok {
				return nil, 0, false
			}
			out = append(out, v)
			i = next
		}
		return out, i + 1, true
	case c == 'd':
		out := bdict{}
		i++
		for i >= len(data) || data[i] != 'e' {
			key, next, ok := bdecode(data, i, depth+1)
			if !ok {
				return nil, 0, false
			}
			start := next
			v, next, ok := bdecode(data, start, depth+1)
			if !ok {
				return nil, 0, false
			}
			switch k := key.(type) {
			case []byte:
				out[string(k)] = [3]any{v, start, next}
			case int64:
			default:
				return nil, 0, false
			}
			i = next
		}
		return out, i + 1, true
	case c >= '0' && c <= '9':
		colon := bytes.IndexByte(data[i:], ':')
		if colon < 0 {
			return nil, 0, false
		}
		length, valid := parseInt(string(data[i : i+colon]))
		if !valid || length < 0 {
			return nil, 0, false
		}
		from := i + colon + 1
		if length > int64(len(data)-from) {
			return nil, 0, false
		}
		to := from + int(length)
		return data[from:to], to, true
	}
	return nil, 0, false
}

func TorrentHash(data []byte) (string, error) {
	top, _, ok := bdecode(data, 0, 0)
	if !ok {
		return "", QbitError("not a torrent file")
	}
	d, isDict := top.(bdict)
	if !isDict {
		return "", QbitError("not a torrent file")
	}
	info, found := d["info"]
	if !found {
		return "", QbitError("not a torrent file")
	}
	sum := sha1.Sum(data[info[1].(int):info[2].(int)]) //nolint:gosec // the BitTorrent v1 info hash is SHA-1
	return hex.EncodeToString(sum[:]), nil
}

type Qbit struct {
	base, key string
	http      *http.Client
	report    func(context.Context, connections.State, string)
}

func NewQbit(base, key string, client *http.Client) *Qbit {
	return &Qbit{base: strings.TrimRight(base, "/"), key: key, http: client}
}

func (q *Qbit) do(ctx context.Context, method, path string, params []param, body io.Reader, contentType string) ([]byte, error) {
	target := q.base + "/api/v2/" + path
	if len(params) > 0 {
		target += "?" + encodeParams(params)
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return nil, QbitError("qBittorrent unreachable: invalid URL")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if q.key != "" {
		req.Header.Set("Authorization", "Bearer "+q.key)
	}
	res, err := send(ctx, q.http, req)
	if err != nil {
		var failure *httpFailure
		if errors.As(err, &failure) {
			q.observe(ctx, connections.Failing, "qBittorrent unreachable: "+failure.class)
			return nil, QbitError("qBittorrent unreachable: " + failure.class)
		}
		return nil, err
	}
	if res.status != http.StatusOK {
		state := connections.Failing
		if res.status == http.StatusUnauthorized || res.status == http.StatusForbidden {
			state = connections.NeedsReauth
		}
		q.observe(ctx, state, fmt.Sprintf("qBittorrent answered %d", res.status))
		return nil, QbitError(fmt.Sprintf("qBittorrent: %s: %d", path, res.status))
	}
	return res.body, nil
}

func (q *Qbit) observe(ctx context.Context, state connections.State, reason string) {
	if q.report != nil {
		q.report(ctx, state, reason)
	}
}

func (q *Qbit) post(ctx context.Context, path string, form []param) error {
	_, err := q.do(ctx, http.MethodPost, path, nil, strings.NewReader(encodeParams(form)), "application/x-www-form-urlencoded")
	return err
}

func checkAdd(body []byte) error {
	if !accepted(string(body)) {
		text := strings.TrimSpace(string(body))
		if text == "" {
			text = "no reply"
		}
		return QbitError("qBittorrent did not accept the torrent: " + text)
	}
	return nil
}

func (q *Qbit) Torrents(ctx context.Context, filter, tag, hashes string) ([]Torrent, error) {
	var params []param
	for _, p := range []param{{"filter", filter}, {"tag", tag}, {"hashes", hashes}} {
		if p.value != "" {
			params = append(params, p)
		}
	}
	body, err := q.do(ctx, http.MethodGet, "torrents/info", params, nil, "")
	if err != nil {
		return nil, err
	}
	var out []Torrent
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, QbitError("qBittorrent: torrents/info: not JSON")
	}
	return out, nil
}

func (q *Qbit) Completed(ctx context.Context) ([]Torrent, error) {
	return q.Torrents(ctx, "completed", "", "")
}

func (q *Qbit) Torrent(ctx context.Context, hash string) (*Torrent, error) {
	found, err := q.Torrents(ctx, "", "", hash)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

func (q *Qbit) Files(ctx context.Context, hash string) ([]TorrentFile, error) {
	body, err := q.do(ctx, http.MethodGet, "torrents/files", []param{{"hash", hash}}, nil, "")
	if err != nil {
		return nil, err
	}
	var out []TorrentFile
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, QbitError("qBittorrent: torrents/files: not JSON")
	}
	return out, nil
}

func (q *Qbit) AddMagnet(ctx context.Context, magnet, tags string) error {
	form := []param{{"urls", magnet}}
	if tags != "" {
		form = append(form, param{"tags", tags})
	}
	body, err := q.do(ctx, http.MethodPost, "torrents/add", nil, strings.NewReader(encodeParams(form)), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	return checkAdd(body)
}

func (q *Qbit) AddFile(ctx context.Context, data []byte, name, tags string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if tags != "" {
		if err := w.WriteField("tags", tags); err != nil {
			return err
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="torrents"; filename="%s"`, strings.NewReplacer(`\`, `\\`, `"`, `%22`).Replace(name)))
	header.Set("Content-Type", "application/x-bittorrent")
	part, err := w.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	body, err := q.do(ctx, http.MethodPost, "torrents/add", nil, &buf, w.FormDataContentType())
	if err != nil {
		return err
	}
	return checkAdd(body)
}

func (q *Qbit) RemoveTags(ctx context.Context, hash, tags string) error {
	return q.post(ctx, "torrents/removeTags", []param{{"hashes", hash}, {"tags", tags}})
}

func (q *Qbit) AddTags(ctx context.Context, hash, tags string) error {
	return q.post(ctx, "torrents/addTags", []param{{"hashes", hash}, {"tags", tags}})
}

func (q *Qbit) SetPriority(ctx context.Context, hash string, ids []int, priority int) error {
	if len(ids) == 0 {
		return nil
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return q.post(ctx, "torrents/filePrio", []param{{"hash", hash}, {"id", strings.Join(parts, "|")}, {"priority", strconv.Itoa(priority)}})
}

func (q *Qbit) Start(ctx context.Context, hash string) error {
	return q.post(ctx, "torrents/start", []param{{"hashes", hash}})
}

func (q *Qbit) Delete(ctx context.Context, hash string, files bool) error {
	return q.post(ctx, "torrents/delete", []param{{"hashes", hash}, {"deleteFiles", strconv.FormatBool(files)}})
}
