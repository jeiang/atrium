package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

var writeGates sync.Map

func gateWrites(w *sql.DB, path string) error {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return err
	}
	writeGates.Store(w, canonical)
	return nil
}

func ungateWrites(w *sql.DB) { writeGates.Delete(w) }

func admitWriter(ctx context.Context, w *sql.DB) (func(), error) {
	v, ok := writeGates.Load(w)
	if !ok {
		return func() {}, nil
	}
	path := v.(string)
	queue, err := os.OpenFile(path+".write-queue.lock", os.O_RDONLY|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	writer, err := os.OpenFile(path+".write.lock", os.O_RDONLY|os.O_CREATE, 0o666)
	if err != nil {
		return nil, errors.Join(err, queue.Close())
	}
	release := func() { _ = writer.Close() }
	queued, err := tryFlock(queue)
	if err == nil && queued {
		var held bool
		if held, err = tryFlock(writer); err == nil && held {
			_ = queue.Close()
			return release, nil
		}
	}
	if err != nil {
		return nil, errors.Join(err, queue.Close(), writer.Close())
	}
	done := make(chan error, 1)
	go func() {
		var err error
		if !queued {
			err = flock(queue)
		}
		if err == nil {
			err = flock(writer)
		}
		_ = queue.Close()
		done <- err
	}()
	timer := time.NewTimer(busyTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = &LockTimeout{Path: writer.Name(), Timeout: busyTimeout}
	}
	go func() {
		<-done
		release()
	}()
	return nil, err
}

func flock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
		default:
			return fmt.Errorf("db: flock %s: %w", f.Name(), err)
		}
	}
}
