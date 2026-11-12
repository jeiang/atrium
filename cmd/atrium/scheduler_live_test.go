package main

import (
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/db"
)

type heldGates struct {
	gates []chan struct{}
	once  []sync.Once
}

func newHeldGates(t *testing.T, n int) *heldGates {
	g := &heldGates{gates: make([]chan struct{}, n), once: make([]sync.Once, n)}
	for i := range g.gates {
		g.gates[i] = make(chan struct{})
		t.Cleanup(func() { g.release(i) })
	}
	return g
}

func (g *heldGates) release(i int) { g.once[i].Do(func() { close(g.gates[i]) }) }

func liveRun(t *testing.T, d *db.DB, run int64) string {
	t.Helper()
	var owner string
	if err := d.Read.QueryRow("SELECT COALESCE(owner, '') FROM runs WHERE id = ? AND state = 'running' AND lease_until > ?", run, unixNow()).Scan(&owner); err != nil {
		t.Fatalf("run %d is not live: %v", run, err)
	}
	return owner
}
