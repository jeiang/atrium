package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

const ScanCommand = "proposals scan-all"

type ScanResult struct {
	Key   string
	Value any
}

type Scan struct {
	Module string
	Uses   []application.Name
	Run    func(ctx context.Context, d *db.DB, cfg config.Config, now func() float64) (results []ScanResult, ran bool, err error)
}
