package notify

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	modules "github.com/jeiang/atrium/internal/module"
)

const (
	Default = "assistant"
	System  = modules.SystemChannel

	Auto  = "auto"
	Full  = "full"
	Title = "title"

	FailingAfter = 3
	privateTitle = "atrium"
	privateBody  = "Open atrium to read this notification."
)

type Channel struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	ShowsText bool   `json:"full_text"`
}

func Channels(ctx context.Context) []string {
	if set := modules.SetFrom(ctx); set != nil {
		return set.Catalog().Channels()
	}
	return []string{System}
}

func known(ctx context.Context, name string) bool {
	return slices.Contains(Channels(ctx), name)
}

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func EnsureChannels(ctx context.Context, q db.Querier) error {
	for _, name := range Channels(ctx) {
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO ntfy_channels (name, topic, created_at) VALUES (?, '', ?)", name, stamp()); err != nil {
			return err
		}
	}
	return nil
}

func GetChannel(ctx context.Context, q db.Querier, name string) (Channel, error) {
	c := Channel{Name: name, Title: channelTitle(ctx, name), Content: Auto}
	err := q.QueryRowContext(ctx, "SELECT content FROM ntfy_channels WHERE name = ?", name).Scan(&c.Content)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Channel{}, err
	}
	c.ShowsText = c.FullText()
	return c, nil
}

func channelTitle(ctx context.Context, name string) string {
	if set := modules.SetFrom(ctx); set != nil {
		if title := set.Catalog().ChannelTitle(name); title != "" {
			return title
		}
	}
	return name
}

func ListChannels(ctx context.Context, q db.Querier) ([]Channel, error) {
	names := Channels(ctx)
	out := make([]Channel, 0, len(names))
	for _, name := range names {
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
	if !known(ctx, channel) {
		return ChannelError("unknown channel " + channel)
	}
	if content != Auto && content != Full && content != Title {
		return ChannelError("content must be auto, full or title")
	}
	if err := ledger.CheckAdmission(ctx); err != nil {
		return err
	}
	if err := EnsureChannels(ctx, q); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "UPDATE ntfy_channels SET content = ? WHERE name = ?", content, channel)
	return err
}

type ChannelError string

func (e ChannelError) Error() string { return string(e) }
