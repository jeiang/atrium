package notify

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

const (
	Assistant = "assistant"
	Media     = "media"
	System    = "system"

	Auto  = "auto"
	Full  = "full"
	Title = "title"

	FailingAfter = 3
	privateTitle = "atrium"
	privateBody  = "Open atrium to read this notification."
)

var Channels = []string{Assistant, Media, System}

type Channel struct {
	Name      string `json:"name"`
	Content   string `json:"content"`
	ShowsText bool   `json:"full_text"`
}

func known(name string) bool {
	for _, c := range Channels {
		if c == name {
			return true
		}
	}
	return false
}

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func EnsureChannels(ctx context.Context, q db.Querier) error {
	for _, name := range Channels {
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO ntfy_channels (name, topic, created_at) VALUES (?, '', ?)", name, stamp()); err != nil {
			return err
		}
	}
	return nil
}

func GetChannel(ctx context.Context, q db.Querier, name string) (Channel, error) {
	c := Channel{Name: name, Content: Auto}
	err := q.QueryRowContext(ctx, "SELECT content FROM ntfy_channels WHERE name = ?", name).Scan(&c.Content)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Channel{}, err
	}
	c.ShowsText = c.FullText()
	return c, nil
}

func ListChannels(ctx context.Context, q db.Querier) ([]Channel, error) {
	out := make([]Channel, 0, len(Channels))
	for _, name := range Channels {
		c, err := GetChannel(ctx, q, name)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (c Channel) FullText() bool { return c.Content != Title }

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
