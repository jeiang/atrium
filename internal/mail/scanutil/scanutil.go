package scanutil

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/textx"
)

type Limits struct {
	SampleSubjects int
	TopLabels      int
}

func ReadLimits(ctx context.Context, q db.Querier) (Limits, error) {
	r := endpoint.Read(ctx, q)
	l := Limits{SampleSubjects: r.Int(mail.KeyScanSampleSubjects), TopLabels: r.Int(mail.KeyScanTopLabels)}
	return l, r.Err()
}

func Digest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

type Message struct {
	ID       int64
	Subject  string
	FromName string
	FromAddr string
	DateTS   float64
}

func (m Message) View() map[string]any {
	return map[string]any{"id": m.ID, "subject": m.Subject, "from_name": m.FromName, "from_addr": m.FromAddr, "date_ts": m.DateTS}
}

func (l Limits) ranked(values []string) ([]string, map[string]int) {
	counts := map[string]int{}
	var order []string
	for _, v := range values {
		if v == "" {
			continue
		}
		if counts[v] == 0 {
			order = append(order, v)
		}
		counts[v]++
	}
	slices.SortFunc(order, func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), cmp.Compare(a, b))
	})
	return order[:min(len(order), l.TopLabels)], counts
}

func (l Limits) TopLabelsOf(values []string) []string {
	labels, _ := l.ranked(values)
	return labels
}

func (l Limits) TopCounts(values []string) []any {
	labels, counts := l.ranked(values)
	out := make([]any, len(labels))
	for i, label := range labels {
		out[i] = map[string]any{"label": label, "count": counts[label]}
	}
	return out
}

func (l Limits) SampleSubjectsOf(rows []Message) []string {
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(a, b Message) int {
		return cmp.Or(cmp.Compare(b.DateTS, a.DateTS), cmp.Compare(a.ID, b.ID))
	})
	seen := map[string]bool{}
	out := []string{}
	for _, r := range sorted {
		subject := strings.TrimSpace(r.Subject)
		if subject != "" && !seen[subject] {
			seen[subject] = true
			out = append(out, textx.Clip(subject, 120))
		}
		if len(out) >= l.SampleSubjects {
			break
		}
	}
	return out
}

type Draft struct {
	Title   string
	Payload map[string]any
	Preview map[string]any
}

func Publish(ctx context.Context, d *db.DB, ts float64, kind string, account *string, fingerprint string, build func() (*Draft, error)) (int64, bool, error) {
	known, err := proposals.Known(ctx, d.Write, fingerprint)
	if err != nil || known {
		return 0, false, err
	}
	draft, err := build()
	if err != nil || draft == nil {
		return 0, false, err
	}
	return proposals.Create(ctx, d, kind, account, draft.Title, draft.Payload, draft.Preview, fingerprint, ts)
}

type Result struct {
	Candidates int
	Created    int
}

func (r Result) Dict() map[string]any {
	return map[string]any{"candidates": r.Candidates, "created": r.Created}
}

func SnapshotList(snaps []mailops.Snapshot) []any {
	out := make([]any, len(snaps))
	for i, s := range snaps {
		out[i] = s.Dict()
	}
	return out
}
