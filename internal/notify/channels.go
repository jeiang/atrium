package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	Assistant = "assistant"
	Media     = "media"
	System    = "system"

	Unknown = "unknown"
	Private = "private"
	Public  = "public"

	Auto  = "auto"
	Full  = "full"
	Title = "title"

	FailingAfter = 3
	privateTitle = "atrium"
	privateBody  = "Open atrium to read this notification."
)

var Channels = []string{Assistant, Media, System}

var legacyTopics = map[string][]string{Assistant: {KeyTopic}, Media: {KeyMediaTopic, KeyTopic}, System: {KeyTopic}}

type Channel struct {
	Name      string   `json:"name"`
	Topic     string   `json:"topic"`
	Privacy   string   `json:"privacy"`
	Content   string   `json:"content"`
	ProbedAt  *float64 `json:"probed_at"`
	Failures  int      `json:"failures"`
	LastError *string  `json:"last_error"`
	LastOKAt  *float64 `json:"last_ok_at"`
	Token     bool     `json:"token"`
	Subscribe string   `json:"subscribe"`
	ShowsText bool     `json:"full_text"`
}

func TokenKey(channel string) string { return "ntfy_token_" + channel }

func init() {
	for _, name := range Channels {
		settings.MustRegister(settings.Def{
			Key: TokenKey(name), Module: module, Type: settings.String, Class: settings.Secret, Secret: true, RequiresEncryptedDB: true, Page: "/settings/notifications",
		})
	}
}

func known(name string) bool {
	for _, c := range Channels {
		if c == name {
			return true
		}
	}
	return false
}

func NewTopic() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "atrium-" + hex.EncodeToString(buf[:]), nil
}

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func EnsureChannels(ctx context.Context, q db.Querier) error {
	for _, name := range Channels {
		var one int
		err := q.QueryRowContext(ctx, "SELECT 1 FROM ntfy_channels WHERE name = ?", name).Scan(&one)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		topic := ""
		for _, key := range legacyTopics[name] {
			if topic != "" {
				break
			}
			if topic, err = text(ctx, q, key); err != nil {
				return err
			}
		}
		if topic == "" {
			if topic, err = NewTopic(); err != nil {
				return err
			}
		}
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO ntfy_channels (name, topic, created_at) VALUES (?, ?, ?)", name, topic, stamp()); err != nil {
			return err
		}
	}
	return nil
}

const channelColumns = "name, topic, privacy, content, probed_at, failures, last_error, last_ok_at"

func scanChannel(row interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	return c, row.Scan(&c.Name, &c.Topic, &c.Privacy, &c.Content, &c.ProbedAt, &c.Failures, &c.LastError, &c.LastOKAt)
}

func GetChannel(ctx context.Context, q db.Querier, name string) (Channel, error) {
	if err := EnsureChannels(ctx, q); err != nil {
		return Channel{}, err
	}
	return scanChannel(q.QueryRowContext(ctx, "SELECT "+channelColumns+" FROM ntfy_channels WHERE name = ?", name))
}

func ListChannels(ctx context.Context, q db.Querier) ([]Channel, error) {
	out := make([]Channel, 0, len(Channels))
	for _, name := range Channels {
		c, err := GetChannel(ctx, q, name)
		if err != nil {
			return nil, err
		}
		var one int
		switch err := q.QueryRowContext(ctx, "SELECT 1 FROM secrets WHERE name = ?", TokenKey(name)).Scan(&one); {
		case err == nil:
			c.Token = true
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (c Channel) FullText() bool {
	switch c.Content {
	case Full:
		return true
	case Title:
		return false
	}
	return c.Privacy == Private
}

func Failing(ctx context.Context, q db.Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM ntfy_channels WHERE failures >= ? ORDER BY name", FailingAfter)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var name string
		return name, r.Scan(&name)
	})
}

func recordDelivery(ctx context.Context, q db.Querier, channel string, failure string) (int, error) {
	if failure == "" {
		_, err := q.ExecContext(ctx, "UPDATE ntfy_channels SET failures = 0, last_error = NULL, last_ok_at = ? WHERE name = ?", stamp(), channel)
		return 0, err
	}
	if _, err := q.ExecContext(ctx, "UPDATE ntfy_channels SET failures = failures + 1, last_error = ? WHERE name = ?", failure, channel); err != nil {
		return 0, err
	}
	var n int
	return n, q.QueryRowContext(ctx, "SELECT failures FROM ntfy_channels WHERE name = ?", channel).Scan(&n)
}

func SetContent(ctx context.Context, q db.Querier, channel, content string) error {
	if !known(channel) {
		return ChannelError("unknown channel " + channel)
	}
	if content != Auto && content != Full && content != Title {
		return ChannelError("content must be auto, full or title")
	}
	if err := EnsureChannels(ctx, q); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "UPDATE ntfy_channels SET content = ? WHERE name = ?", content, channel)
	return err
}

type ChannelError string

func (e ChannelError) Error() string { return string(e) }
