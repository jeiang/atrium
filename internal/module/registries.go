package module

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/ops"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
	"github.com/jeiang/atrium/internal/review"
)

const (
	SystemChannel = "system"
	platformOwner = "platform"
)

type EffectOrigin struct {
	Name   string
	Direct bool
}

type Ref struct {
	Ref     string
	Label   string
	Project int64
	Line    string
}

type RefKind struct {
	Kind    string
	Resolve func(context.Context, db.Querier, string) (*Ref, error)
}

func NumberedRef(kind string, resolve func(ctx context.Context, q db.Querier, id int64, number string) (*Ref, error)) RefKind {
	return RefKind{Kind: kind, Resolve: func(ctx context.Context, q db.Querier, raw string) (*Ref, error) {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, nil
		}
		number := strconv.FormatInt(id, 10)
		r, err := resolve(ctx, q, id, number)
		if errors.Is(err, sql.ErrNoRows) || err == nil && r == nil {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		r.Ref = kind + ":" + number
		if r.Label == "" {
			r.Label = strings.TrimSuffix(strings.TrimPrefix(r.Line, "About "), ".")
		}
		return r, nil
	}}
}

type Attachment struct {
	Kind string
	Tool string
	Key  string
	Load func(context.Context, db.Querier, int64) ([]map[string]any, error)
}

type registries struct {
	origins        map[string]string
	day            daySources
	communications communications.Feed
	refresh        Refresher
	direct         []string
	channels       []string
	refs           map[string]RefKind
	attachments    []Attachment
	categories     []review.Category
	corpora        []embed.Corpus
	queues         []ops.Queue
	scans          []proposalsapp.Scan
}

type claims map[string]string

func (c claims) take(hook, key, owner string) error {
	if prev, ok := c[key]; ok {
		return &DuplicateHook{Hook: hook + " " + key, Modules: []string{prev, owner}}
	}
	c[key] = owner
	return nil
}

func compileRegistries(decls []Declaration) (registries, error) {
	r := registries{origins: map[string]string{}, refs: map[string]RefKind{}}
	var err error
	if r.day, err = compileDay(decls); err != nil {
		return registries{}, err
	}
	if r.communications, err = compileCommunications(decls); err != nil {
		return registries{}, err
	}
	if r.refresh, err = compileRefresh(decls); err != nil {
		return registries{}, err
	}
	channels := claims{SystemChannel: platformOwner}
	refs, tools, hubs, pages := claims{}, claims{}, claims{}, claims{}
	categories := claims{review.CategorySettings: platformOwner, review.CategoryOperations: platformOwner}
	operations, corpora, queues := claims{}, claims{}, claims{}
	fallback := ""
	for _, d := range decls {
		for _, c := range d.Corpora {
			if strings.TrimSpace(c.Name) == "" {
				return registries{}, &InvalidDeclaration{Module: d.Name, Field: "corpora"}
			}
			if err := corpora.take("embedding corpus", c.Name, d.Name); err != nil {
				return registries{}, err
			}
			r.corpora = append(r.corpora, c)
		}
		for _, q := range d.Queues {
			if strings.TrimSpace(q.Name) == "" || q.Count == nil {
				return registries{}, &InvalidDeclaration{Module: d.Name, Field: "queues"}
			}
			if err := queues.take("queue", q.Name, d.Name); err != nil {
				return registries{}, err
			}
			r.queues = append(r.queues, q)
		}
		if s := d.ProposalScan; s != nil {
			if s.Run == nil || len(s.Uses) == 0 {
				return registries{}, &InvalidDeclaration{Module: d.Name, Field: "proposal scan"}
			}
			scan := *s
			scan.Module = d.Name
			r.scans = append(r.scans, scan)
		}
		for _, origin := range d.EffectOrigins {
			if err := claims(r.origins).take("effect origin", origin.Name, d.Name); err != nil {
				return registries{}, err
			}
			if origin.Direct {
				r.direct = append(r.direct, origin.Name)
			}
		}
		for _, name := range d.Channels {
			if strings.TrimSpace(name) == "" {
				return registries{}, &InvalidDeclaration{Module: d.Name, Field: "channels"}
			}
			if err := channels.take("notification channel", name, d.Name); err != nil {
				return registries{}, err
			}
			r.channels = append(r.channels, name)
		}
		for _, ref := range d.Refs {
			if strings.TrimSpace(ref.Kind) == "" || ref.Resolve == nil {
				return registries{}, &InvalidDeclaration{Module: d.Name, Field: "refs"}
			}
			if err := refs.take("reference kind", ref.Kind, d.Name); err != nil {
				return registries{}, err
			}
			r.refs[ref.Kind] = ref
		}
		for _, a := range d.Attachments {
			if a.Kind == "" || a.Tool == "" || a.Key == "" || a.Load == nil {
				return registries{}, &InvalidDeclaration{Module: d.Name, Field: "attachments"}
			}
			if err := tools.take("attachment tool", a.Tool, d.Name); err != nil {
				return registries{}, err
			}
			r.attachments = append(r.attachments, a)
		}
		if d.ActivityCategory != "" {
			if err := categories.take("activity category", d.ActivityCategory, d.Name); err != nil {
				return registries{}, err
			}
		}
		for _, c := range d.EffectCategories {
			if strings.TrimSpace(c.Name) == "" {
				return registries{}, &InvalidDeclaration{Module: d.Name, Field: "effect categories"}
			}
			if err := categories.take("activity category", c.Name, d.Name); err != nil {
				return registries{}, err
			}
			if len(c.Operations) == 0 {
				if fallback != "" {
					return registries{}, &DuplicateHook{Hook: "default activity category", Modules: []string{fallback, d.Name}}
				}
				fallback = d.Name
			}
			for _, prefix := range c.Operations {
				if err := operations.take("activity operation prefix", prefix, d.Name); err != nil {
					return registries{}, err
				}
			}
			r.categories = append(r.categories, c)
		}
		for _, p := range d.Pages {
			for _, item := range p.Nav {
				switch {
				case item.Hub:
					if err := hubs.take("navigation hub", item.Page, d.Name); err != nil {
						return registries{}, err
					}
				case item.Page != "" && pages[item.Page] != d.Name:
					if err := pages.take("navigation page", item.Page, d.Name); err != nil {
						return registries{}, err
					}
				}
			}
		}
	}
	slices.Sort(r.channels)
	r.channels = append(r.channels, SystemChannel)
	slices.SortFunc(r.categories, func(a, b review.Category) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(r.queues, func(a, b ops.Queue) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(r.scans, func(a, b proposalsapp.Scan) int { return strings.Compare(a.Module, b.Module) })
	return r, nil
}

func (c *Catalog) DirectOrigins() []string { return slices.Clone(c.regs.direct) }

func (c *Catalog) Channels() []string { return slices.Clone(c.regs.channels) }

func (c *Catalog) Ref(kind string) (RefKind, bool) {
	ref, ok := c.regs.refs[kind]
	return ref, ok
}

func (c *Catalog) Attachments() []Attachment { return slices.Clone(c.regs.attachments) }

func (c *Catalog) ActivityCategories() []review.Category { return slices.Clone(c.regs.categories) }

func (c *Catalog) Corpora() []embed.Corpus { return slices.Clone(c.regs.corpora) }

func (c *Catalog) ProposalScans() []proposalsapp.Scan { return slices.Clone(c.regs.scans) }

func bindScans(decls []Declaration, scans []proposalsapp.Scan) []Declaration {
	var uses []application.Name
	for _, s := range scans {
		uses = append(uses, s.Uses...)
	}
	out := slices.Clone(decls)
	for i, d := range out {
		if d.Name != Core {
			continue
		}
		bindings := slices.Clone(d.Bindings)
		for j, b := range bindings {
			if b.Kind == application.CLI && b.Target == proposalsapp.ScanCommand {
				bindings[j].Uses = slices.Concat(b.Uses, uses)
			}
		}
		out[i].Bindings = bindings
	}
	return out
}
