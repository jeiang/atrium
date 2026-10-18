package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"math"
)

// Open returns a pool on the SQLite file at path through the driver selected
// at build time (open_cgo.go or open_modernc.go). Each pragma statement runs
// on every new connection.
func Open(path string, pragmas ...string) *sql.DB {
	return sql.OpenDB(connector{drv: openDriver(), dsn: path, pragmas: pragmas})
}

type connector struct {
	drv     driver.Driver
	dsn     string
	pragmas []string
}

func (c connector) Driver() driver.Driver { return c.drv }

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	if len(c.pragmas) == 0 {
		return conn, nil
	}
	exec, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("db: %T cannot execute pragmas", conn)
	}
	for _, p := range c.pragmas {
		if _, err := exec.ExecContext(ctx, p, nil); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("db: %s: %w", p, err)
		}
	}
	return conn, nil
}

// SerializeFloat32 encodes v as the little-endian float32 blob vec0 stores,
// like sqlite_vec.serialize_float32.
func SerializeFloat32(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}
