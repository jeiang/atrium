package scans

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/deletions"
	"github.com/jeiang/atrium/internal/mail/gmaillabels"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/py"
)

var Names = [...]string{"rebalance", "deletions", "gmail_labels"}

func ScanAll(ctx context.Context, d *db.DB, ts float64, namer rebalance.Namer) (out py.Dict, err error) {
	err = passrun.Track(ctx, d, passrun.Scan, func() error {
		rb, err := rebalance.Scan(ctx, d, ts, namer)
		if err != nil {
			return err
		}
		dl, err := deletions.Scan(ctx, d, ts)
		if err != nil {
			return err
		}
		gl, err := gmaillabels.Scan(ctx, d, ts)
		if err != nil {
			return err
		}
		out = py.Dict{{Key: Names[0], Value: rb.Dict()}, {Key: Names[1], Value: dl.Dict()}, {Key: Names[2], Value: gl.Dict()}}
		return nil
	})
	return out, err
}
