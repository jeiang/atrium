package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"time"
)

const gatePollInterval = time.Millisecond

type writeGate struct{ path string }

type gateableConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
}

type gatedConn struct {
	gateableConn
	gate *writeGate
	held *os.File
}

type gatedTx struct {
	driver.Tx
	conn *gatedConn
}

func (c *gatedConn) lock(ctx context.Context, wait time.Duration) (bool, error) {
	if c.held != nil || c.gate.path == "" {
		return false, nil
	}
	f, err := admit(ctx, c.gate.path, wait)
	if err != nil {
		return false, err
	}
	c.held = f
	return true, nil
}

func (c *gatedConn) unlock() {
	if c.held != nil {
		_ = c.held.Close()
		c.held = nil
	}
}

func (c *gatedConn) Close() error {
	c.unlock()
	return c.gateableConn.Close()
}

func (c *gatedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *gatedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	took, err := c.lock(ctx, busyTimeout)
	if err != nil {
		return nil, err
	}
	tx, err := c.gateableConn.BeginTx(ctx, opts)
	switch {
	case err != nil:
		if took {
			c.unlock()
		}
		return nil, err
	case took:
		return gatedTx{Tx: tx, conn: c}, nil
	default:
		return tx, nil
	}
}

func (t gatedTx) Commit() error {
	defer t.conn.unlock()
	return t.Tx.Commit()
}

func (t gatedTx) Rollback() error {
	defer t.conn.unlock()
	return t.Tx.Rollback()
}

func (c *gatedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	took, err := c.lock(ctx, busyTimeout)
	if err != nil {
		return nil, err
	}
	if took {
		defer c.unlock()
	}
	return c.gateableConn.ExecContext(ctx, query, args)
}

func (c *gatedConn) Ping(ctx context.Context) error {
	if p, ok := c.gateableConn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *gatedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.gateableConn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *gatedConn) IsValid() bool {
	if v, ok := c.gateableConn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *gatedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := c.gateableConn.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func holdGate(ctx context.Context, conn *sql.Conn, wait time.Duration) (func(), error) {
	release := func() {}
	err := conn.Raw(func(dc any) error {
		g, ok := dc.(*gatedConn)
		if !ok {
			return nil
		}
		took, err := g.lock(ctx, wait)
		if took {
			release = func() { _ = conn.Raw(func(any) error { g.unlock(); return nil }) }
		}
		return err
	})
	return release, err
}

func admit(ctx context.Context, path string, wait time.Duration) (*os.File, error) {
	queue, err := os.OpenFile(path+".write-queue.lock", os.O_RDONLY|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	defer func() { _ = queue.Close() }()
	writer, err := os.OpenFile(path+".write.lock", os.O_RDONLY|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	held := false
	defer func() {
		if !held {
			_ = writer.Close()
		}
	}()
	queued, err := tryFlock(queue)
	if err == nil && queued {
		held, err = tryFlock(writer)
	}
	if err != nil {
		return nil, err
	}
	if held {
		return writer, nil
	}
	ticker := time.NewTicker(gatePollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, &LockTimeout{Path: writer.Name(), Timeout: wait}
		case <-ticker.C:
		}
		if !queued {
			if queued, err = tryFlock(queue); err != nil {
				return nil, err
			}
		}
		if queued {
			if held, err = tryFlock(writer); err != nil {
				return nil, err
			}
			if held {
				return writer, nil
			}
		}
	}
}
