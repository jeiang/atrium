package torrents

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

const WatchInterval = 5 * time.Second

func touchDownloads(ctx context.Context, d *db.DB) {
	if err := state.New(d).Touch(context.WithoutCancel(ctx), state.Downloads); err != nil {
		logx.Logger("torrents").Warn("downloads topic touch failed", logx.Err, err)
	}
}

func digest(list []Torrent) [sha256.Size]byte {
	rows := make([]string, len(list))
	for i, t := range list {
		rows[i] = fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", t.Hash, t.Name, t.State, t.Tags, int(math.Floor(t.Progress*100)))
	}
	slices.Sort(rows)
	h := sha256.New()
	for _, row := range rows {
		h.Write([]byte(row))
		h.Write([]byte{'\n'})
	}
	var out [sha256.Size]byte
	h.Sum(out[:0])
	return out
}

func (a *App) Watch(ctx context.Context, d contract.Deps) error {
	in := &intake{app: a, d: d}
	tick := time.NewTicker(WatchInterval)
	defer tick.Stop()
	var last [sha256.Size]byte
	seen := false
	for {
		if list, err := in.List(ctx, contract.Filter{}); err == nil {
			sum := digest(list)
			if seen && sum != last {
				touchDownloads(ctx, d.DB)
			}
			last, seen = sum, true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
