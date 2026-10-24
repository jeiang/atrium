package db

import (
	"database/sql"
	"fmt"
)

const (
	smallCacheKiB = 2048
	knnCacheKiB   = 196608
)

func cachePragma(kib int) string {
	return fmt.Sprintf("PRAGMA cache_size = %d", -kib)
}

func newReader(dsn string, pragmas []string, cacheKiB int) *sql.DB {
	all := append(append([]string{}, pragmas...), cachePragma(cacheKiB), "PRAGMA query_only = ON")
	return Open(dsn, all...)
}

func newKNN(dsn string, pragmas []string) *sql.DB {
	k := newReader(dsn, pragmas, knnCacheKiB)
	k.SetMaxOpenConns(1)
	k.SetMaxIdleConns(1)
	k.SetConnMaxIdleTime(0)
	return k
}
