package scans

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/deletions"
	"github.com/jeiang/atrium/internal/mail/gmaillabels"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/py"
)

var Names = [...]string{"rebalance", "deletions", "gmail_labels"}

// ScanAll is scan_all: each scanner's result by name; namer names rebalance's split folders. Every scanner gets
// the one ts as its run clock and created_ts; Python reads now() inside each scanner (rebalance once, the others
// per created proposal), so its timestamps of one pass can differ by the milliseconds the pass took.
func ScanAll(ctx context.Context, d *db.DB, ts float64, namer rebalance.Namer) (py.Dict, error) {
	rb, err := rebalance.Scan(ctx, d, ts, namer)
	if err != nil {
		return nil, err
	}
	dl, err := deletions.Scan(ctx, d, ts)
	if err != nil {
		return nil, err
	}
	gl, err := gmaillabels.Scan(ctx, d, ts)
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: Names[0], Value: rb.Dict()}, {Key: Names[1], Value: dl.Dict()}, {Key: Names[2], Value: gl.Dict()}}, nil
}
