//go:build modernc

package db

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"modernc.org/libc"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// SQLite's default busy handler sleeps through sqlite3_interrupt, so a canceled
// statement kept waiting its whole busy timeout. This handler ends the wait as
// soon as the driver interrupts the connection, like internal/db/busywait.

type busyState struct{ ms, slept int32 }

var (
	busyMu     sync.Mutex
	busyStates = map[uintptr]*busyState{}
)

var openDriver = sync.OnceValue(func() driver.Driver {
	d := &sqlite.Driver{}
	d.MustRegisterScalarFunction("atrium_busy_timeout", 1, setBusyTimeout)
	return d
})

func setBusyTimeout(fc *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	ms, ok := args[0].(int64)
	if !ok || ms < 0 || ms > 1<<31-1 {
		return nil, errors.New("atrium_busy_timeout: needs milliseconds")
	}
	v := reflect.ValueOf(fc).Elem()
	tls := (*libc.TLS)(unsafe.Pointer(v.FieldByName("tls").Pointer()))
	db := sqlite3.Xsqlite3_context_db_handle(tls, uintptr(v.FieldByName("ctx").Uint()))
	busyMu.Lock()
	s := busyStates[db]
	if s == nil {
		s = &busyState{}
		busyStates[db] = s
	}
	s.ms = int32(ms)
	busyMu.Unlock()
	if rc := sqlite3.Xsqlite3_busy_handler(tls, db, cFuncPointer(busyWait), db); rc != sqlite3.SQLITE_OK {
		return nil, errors.New("atrium_busy_timeout: busy handler not installed")
	}
	return ms, nil
}

func busyWait(tls *libc.TLS, db uintptr, n int32) int32 {
	busyMu.Lock()
	s := busyStates[db]
	busyMu.Unlock()
	if s == nil {
		return 0
	}
	if n == 0 {
		s.slept = 0
	}
	if sqlite3.Xsqlite3_is_interrupted(tls, db) != 0 || s.slept >= s.ms {
		return 0
	}
	d := min(n+1, 10, s.ms-s.slept)
	time.Sleep(time.Duration(d) * time.Millisecond)
	s.slept += d
	return 1
}

// cFuncPointer converts a Go function to the function pointer modernc's C
// translation calls, as modernc.org/sqlite does for its own callbacks.
func cFuncPointer[T any](f T) uintptr {
	return *(*uintptr)(unsafe.Pointer(&struct{ f T }{f}))
}
