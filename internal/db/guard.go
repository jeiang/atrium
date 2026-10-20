package db

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"
)

var plaintextHeader = []byte("SQLite format 3\x00")

func guardFile(path string, key Key) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, len(plaintextHeader))
	n, err := io.ReadFull(f, header)
	if n == 0 {
		return nil
	}
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	plaintext := bytes.Equal(header[:n], plaintextHeader)
	switch {
	case plaintext && !key.IsZero():
		return ErrPlaintextDB
	case !plaintext && key.IsZero():
		return ErrKeyMissing
	}
	return nil
}

func guardEngine(w *sql.DB, key Key) error {
	if key.IsZero() {
		return nil
	}
	var version string
	err := w.QueryRow("PRAGMA cipher_version").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) || err == nil && version == "" {
		return ErrNoCipher
	}
	if err != nil {
		return err
	}
	var n int
	if err := w.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n); err != nil {
		if strings.Contains(err.Error(), "not a database") {
			return ErrWrongKey
		}
		return err
	}
	return nil
}
