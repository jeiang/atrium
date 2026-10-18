package embed

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
)

type document struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

func init() {
	for _, key := range []string{"dentist", "trip"} {
		readpaths.Register(readpaths.Path{
			Name:   "embed.message_document " + key,
			Golden: "message.get_message " + key,
			Run: func(ctx context.Context, d *db.DB) (any, error) {
				var id int64
				if err := d.Read.QueryRowContext(ctx,
					"SELECT id FROM messages WHERE msgid = ? ORDER BY gone_at IS NULL DESC, id DESC",
					key+"@golden.example").Scan(&id); err != nil {
					return nil, err
				}
				text, err := MessageDocument(ctx, d.Read, id)
				return document{id, text}, err
			},
			Want: func(recorded json.RawMessage) (any, error) {
				var m struct {
					ID       int64  `json:"id"`
					Subject  string `json:"subject"`
					FromName string `json:"from_name"`
					FromAddr string `json:"from_addr"`
					Body     string `json:"body"`
				}
				if err := json.Unmarshal(recorded, &m); err != nil {
					return nil, err
				}
				return document{m.ID, DocumentText(m.Subject, m.FromName, m.FromAddr, m.Body)}, nil
			},
		})
	}
}
