package media

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media/agentclient"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

const (
	catalogueDeadline   = 30 * time.Second
	catalogueRevalidate = 10 * time.Second
	catalogueMaxPages   = 10000
)

type LibraryUnavailable struct{ Cause error }

func (e *LibraryUnavailable) Error() string { return "the media library is unavailable" }

func (e *LibraryUnavailable) Unwrap() error { return e.Cause }

type Snapshot struct {
	RootRevision string
	Observed     time.Time
	Generation   int64
	Stale        error
	nodes        map[string]protocol.Node
	children     map[string][]protocol.Node
}

func nodeKey(root, rel string) string { return root + "\x00" + rel }

func newSnapshot(page protocol.LibraryPage, nodes []protocol.Node, generation int64) *Snapshot {
	s := &Snapshot{RootRevision: page.RootRevision, Observed: time.Now().UTC(), Generation: generation,
		nodes: make(map[string]protocol.Node, len(nodes)), children: map[string][]protocol.Node{}}
	for _, n := range nodes {
		s.nodes[nodeKey(n.Root, n.Rel)] = n
		parent := path.Dir(n.Rel)
		if parent == "." {
			parent = ""
		}
		k := nodeKey(n.Root, parent)
		s.children[k] = append(s.children[k], n)
	}
	for _, list := range s.children {
		slices.SortFunc(list, func(a, b protocol.Node) int { return strings.Compare(path.Base(a.Rel), path.Base(b.Rel)) })
	}
	return s
}

func (s Snapshot) node(root, rel string) (protocol.Node, bool) {
	n, ok := s.nodes[nodeKey(root, rel)]
	return n, ok
}

func (s Snapshot) list(root, parent string) []protocol.Node { return s.children[nodeKey(root, parent)] }

type flight struct {
	done chan struct{}
	err  error
}

type Catalogue struct {
	mu     sync.Mutex
	snap   *Snapshot
	digest [32]byte
	stale  error
	flight *flight
	nudge  chan struct{}
}

var catalogues sync.Map

func CatalogueFor(d *db.DB) *Catalogue {
	if c, ok := catalogues.Load(d); ok {
		return c.(*Catalogue)
	}
	c, _ := catalogues.LoadOrStore(d, &Catalogue{nudge: make(chan struct{}, 1)})
	return c.(*Catalogue)
}

func (c *Catalogue) Nudge() {
	select {
	case c.nudge <- struct{}{}:
	default:
	}
}

func (c *Catalogue) Nudges() <-chan struct{} { return c.nudge }

func (c *Catalogue) Refresh(ctx context.Context, client *agentclient.Client) error {
	c.mu.Lock()
	if f := c.flight; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flight = f
	c.mu.Unlock()
	page, nodes, err := scanLibrary(ctx, client)
	c.mu.Lock()
	if err != nil {
		c.stale = err
	} else {
		digest := sha256.New()
		_ = json.NewEncoder(digest).Encode(nodes)
		var sum [32]byte
		copy(sum[:], digest.Sum(nil))
		generation := int64(1)
		if c.snap != nil {
			generation = c.snap.Generation
			if sum != c.digest {
				generation++
			}
		}
		c.snap, c.digest, c.stale = newSnapshot(page, nodes, generation), sum, nil
	}
	f.err = err
	c.flight = nil
	close(f.done)
	c.mu.Unlock()
	return err
}

func scanLibrary(ctx context.Context, client *agentclient.Client) (protocol.LibraryPage, []protocol.Node, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogueDeadline)
	defer cancel()
	page, nodes, err := libraryPages(ctx, client)
	var gone *agentclient.Error
	if errors.As(err, &gone) && gone.Status == http.StatusGone {
		page, nodes, err = libraryPages(ctx, client)
	}
	return page, nodes, err
}

func libraryPages(ctx context.Context, client *agentclient.Client) (protocol.LibraryPage, []protocol.Node, error) {
	var first protocol.LibraryPage
	nodes := []protocol.Node{}
	cursor := ""
	for i := range catalogueMaxPages {
		page, err := client.Library(ctx, cursor)
		if err != nil {
			return first, nil, err
		}
		if i == 0 {
			first = page
		} else if page.SnapshotID != first.SnapshotID {
			return first, nil, agentclient.ErrUnavailable
		}
		nodes = append(nodes, page.Nodes...)
		if page.Next == "" {
			return first, nodes, nil
		}
		cursor = page.Next
	}
	return first, nil, agentclient.ErrUnavailable
}

func (c *Catalogue) Current(ctx context.Context, refresh func(context.Context) error) (Snapshot, error) {
	c.mu.Lock()
	snap, busy := c.snap, c.flight != nil
	c.mu.Unlock()
	if snap == nil {
		err := refresh(ctx)
		c.mu.Lock()
		snap = c.snap
		c.mu.Unlock()
		if snap == nil {
			if err == nil {
				err = agentclient.ErrUnavailable
			}
			return Snapshot{}, &LibraryUnavailable{Cause: err}
		}
	} else if !busy && time.Since(snap.Observed) > catalogueRevalidate {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogueDeadline)
			defer cancel()
			_ = refresh(ctx)
		}()
	}
	c.mu.Lock()
	out := *snap
	out.Stale = c.stale
	c.mu.Unlock()
	return out, nil
}
