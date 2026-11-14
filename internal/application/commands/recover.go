package commands

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

const (
	ReplayWindow = 24 * time.Hour
	Retention    = 30 * 24 * time.Hour
)

func Open(ctx context.Context, d *db.DB, appKey func() ([]byte, error), tempDir string) (*Store, error) {
	s := New(d)
	s.appKey = appKey
	s.tempDir = tempDir
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "command-body-") {
			if err := os.Remove(filepath.Join(tempDir, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	var n int64
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE commands SET state = ?, ended_at = ? WHERE state = ?", string(Interrupted), seconds(s.now()), string(Running))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if n > 0 {
		log.Warn("marked interrupted API commands", "count", n)
	}
	return s, err
}

func (s *Store) Prune(ctx context.Context) error {
	now := seconds(s.now())
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE commands SET "+released+" WHERE idempotency_key IS NOT NULL AND ended_at < ?", now-ReplayWindow.Seconds()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM commands WHERE ended_at < ?", now-Retention.Seconds())
		return err
	})
}

func (s *Store) macKey() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mac != nil {
		return s.mac, nil
	}
	raw, err := s.appKey()
	if err != nil {
		return nil, err
	}
	mac, err := hkdf.Key(sha256.New, raw, nil, "atrium/commands/fingerprint/v1", sha256.Size)
	if err != nil {
		return nil, err
	}
	s.mac = mac
	return mac, nil
}
