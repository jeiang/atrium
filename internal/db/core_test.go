package db_test

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

const (
	roleEnv = "ATRIUM_DB_TEST_ROLE"
	pathEnv = "ATRIUM_DB_TEST_PATH"
)

func TestMain(m *testing.M) {
	switch os.Getenv(roleEnv) {
	case "":
		os.Exit(m.Run())
	case "writer":
		os.Exit(writerProcess(os.Getenv(pathEnv)))
	case "locker":
		os.Exit(lockerProcess(os.Getenv(pathEnv)))
	default:
		os.Exit(3)
	}
}

func child(t *testing.T, role, path string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), roleEnv+"="+role, pathEnv+"="+path)
	cmd.Stderr = os.Stderr
	return cmd
}

func increment(ctx context.Context, d *db.DB) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT n FROM counter").Scan(&n); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE counter SET n = ?", n+1)
		return err
	})
}

func writerProcess(path string) int {
	d, err := db.Connect(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = d.Close() }()
	ctx := context.Background()
	var ok, busy, other int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		switch err := increment(ctx, d); {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "database is locked"):
			busy++
		default:
			other++
			fmt.Fprintln(os.Stderr, err)
		}
		var n int
		if err := d.Read.QueryRowContext(ctx, "SELECT n FROM counter").Scan(&n); err != nil {
			other++
			fmt.Fprintln(os.Stderr, err)
		}
	}
	fmt.Println(ok, busy, other)
	return 0
}

func lockerProcess(path string) int {
	l, err := db.Exclusive(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = l.Close() }()
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	return 0
}

func TestConnectPragmas(t *testing.T) {
	d := dbtest.File(t)
	for _, c := range []struct {
		name      string
		pool      *sql.DB
		queryOnly int
	}{{"write", d.Write, 0}, {"read", d.Read, 1}} {
		var fk, busy, sync, qo int
		var mode string
		for _, q := range []struct {
			pragma string
			dst    any
		}{
			{"foreign_keys", &fk}, {"busy_timeout", &busy}, {"synchronous", &sync},
			{"query_only", &qo}, {"journal_mode", &mode},
		} {
			if err := c.pool.QueryRow("PRAGMA " + q.pragma).Scan(q.dst); err != nil {
				t.Fatal(err)
			}
		}
		if fk != 1 || busy != 30000 || sync != 2 || qo != c.queryOnly || mode != "wal" {
			t.Errorf("%s: foreign_keys=%d busy_timeout=%d synchronous=%d query_only=%d journal_mode=%s",
				c.name, fk, busy, sync, qo, mode)
		}
	}
	if _, err := d.Read.Exec("CREATE TABLE t (x)"); err == nil {
		t.Error("reader pool accepted a write")
	}
}

func TestConnectCreatesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("CREATE TABLE t (x)"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestWithTx(t *testing.T) {
	ctx := t.Context()
	d := dbtest.File(t)
	if _, err := d.Write.Exec("CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	insert := func(x int) func(*sql.Tx) error {
		return func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO t VALUES (?)", x)
			return err
		}
	}
	if err := d.WithTx(ctx, insert(1)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := insert(2)(tx); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("WithTx error = %v, want boom", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic not propagated")
			}
		}()
		_ = d.WithTx(ctx, func(tx *sql.Tx) error {
			_ = insert(3)(tx)
			panic("boom")
		})
	}()
	var sum, n int
	if err := d.Read.QueryRow("SELECT COALESCE(SUM(x), 0), COUNT(*) FROM t").Scan(&sum, &n); err != nil {
		t.Fatal(err)
	}
	if sum != 1 || n != 1 {
		t.Errorf("rows: sum=%d count=%d, want only the committed row", sum, n)
	}
}

func TestGoroutineWriters(t *testing.T) {
	ctx := t.Context()
	d := dbtest.File(t)
	if _, err := d.Write.Exec("CREATE TABLE counter (n INTEGER); INSERT INTO counter VALUES (0)"); err != nil {
		t.Fatal(err)
	}
	const workers, each = 8, 50
	var wg sync.WaitGroup
	errs := make(chan error, workers*each*2)
	for range workers {
		wg.Go(func() {
			for range each {
				if err := increment(ctx, d); err != nil {
					errs <- err
				}
				var n int
				if err := d.Read.QueryRowContext(ctx, "SELECT n FROM counter").Scan(&n); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var n int
	if err := d.Read.QueryRow("SELECT n FROM counter").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != workers*each {
		t.Errorf("counter = %d, want %d", n, workers*each)
	}
}

func TestWriterProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("CREATE TABLE counter (n INTEGER); INSERT INTO counter VALUES (0)"); err != nil {
		t.Fatal(err)
	}
	cmds := make([]*exec.Cmd, 3)
	outs := make([]strings.Builder, len(cmds))
	for i := range cmds {
		cmds[i] = child(t, "writer", path)
		cmds[i].Stdout = &outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
		var ok, busy, other int
		if _, err := fmt.Sscan(outs[i].String(), &ok, &busy, &other); err != nil {
			t.Fatalf("writer %d output %q: %v", i, outs[i].String(), err)
		}
		t.Logf("writer %d: %d commits, %d SQLITE_BUSY, %d other errors", i, ok, busy, other)
		if busy != 0 || other != 0 || ok == 0 {
			t.Errorf("writer %d: want commits and no errors", i)
		}
		total += ok
	}
	var n int
	if err := d.Read.QueryRow("SELECT n FROM counter").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != total {
		t.Errorf("counter = %d, want %d commits", n, total)
	}
}

func TestExclusiveProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atrium.db")
	start := func() (*exec.Cmd, chan string) {
		cmd := child(t, "locker", path)
		if _, err := cmd.StdinPipe(); err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		lines := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(out).ReadString('\n')
			lines <- strings.TrimSpace(line)
		}()
		return cmd, lines
	}
	first, firstLocked := start()
	if got := <-firstLocked; got != "locked" {
		t.Fatalf("first locker printed %q", got)
	}
	_, secondLocked := start()
	select {
	case got := <-secondLocked:
		t.Fatalf("second locker printed %q while the first held the lock", got)
	case <-time.After(500 * time.Millisecond):
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-secondLocked:
		if got != "locked" {
			t.Fatalf("second locker printed %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock not released when its holder died")
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

func TestExclusiveCloseReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notion")
	l, err := db.Exclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan *db.Lock)
	go func() {
		l2, err := db.Exclusive(path)
		if err != nil {
			t.Error(err)
		}
		acquired <- l2
	}()
	select {
	case <-acquired:
		t.Fatal("second Exclusive did not block")
	case <-time.After(200 * time.Millisecond):
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if l2 := <-acquired; l2 != nil {
		_ = l2.Close()
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

func TestMemory(t *testing.T) {
	for _, c := range []struct {
		d  *db.DB
		fk int
	}{{dbtest.Memory(t), 1}, {dbtest.MemoryNoForeignKeys(t), 0}} {
		var fk int
		if err := c.d.Read.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != c.fk {
			t.Errorf("foreign_keys = %d, want %d", fk, c.fk)
		}
		if err := c.d.WithTx(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec("CREATE TABLE t (x); INSERT INTO t VALUES (" + strconv.Itoa(c.fk) + ")")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var x int
		if err := c.d.Read.QueryRow("SELECT x FROM t").Scan(&x); err != nil || x != c.fk {
			t.Errorf("read after write: x=%d err=%v", x, err)
		}
	}
}

func TestConnectRejectsQuery(t *testing.T) {
	if _, err := db.Connect(filepath.Join(t.TempDir(), "a?b.db")); err == nil {
		t.Error("Connect accepted a path containing '?'")
	}
}
