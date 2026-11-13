package db

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"os"
)

func isPlaintext(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, len(plaintextHeader))
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return bytes.Equal(header[:n], plaintextHeader), nil
}

func OpenSource(path string, key Key) (*sql.DB, error) {
	plain, err := isPlaintext(path)
	if err != nil {
		return nil, err
	}
	if plain {
		key = Key{}
	}
	s, err := readOnlyDSN(path, key)
	if err != nil {
		return nil, err
	}
	if err := guardFile(path, key); err != nil {
		return nil, err
	}
	d := Open(s, busyWaitSQL(busyTimeout), "PRAGMA foreign_keys = OFF", "PRAGMA query_only = ON")
	d.SetMaxOpenConns(1)
	if err := guardEngine(d, key); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}
