package scanutil

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/py"
)

type Limits struct {
	SampleSubjects int
	TopLabels      int
}

func ReadLimits(ctx context.Context, q mail.Querier) (Limits, error) {
	r := mail.Read(ctx, q)
	l := Limits{SampleSubjects: r.Int(mail.KeyScanSampleSubjects), TopLabels: r.Int(mail.KeyScanTopLabels)}
	return l, r.Err()
}

func Digest(parts ...any) string {
	h := sha256.New()
	for _, part := range parts {
		switch v := part.(type) {
		case string:
			h.Write([]byte(v))
		case int64:
			h.Write([]byte(strconv.FormatInt(v, 10)))
		default:
			panic("scanutil: Digest part must be a string or an int64")
		}
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

func (m Message) View() py.Dict {
	return py.Dict{
		{Key: "id", Value: m.ID}, {Key: "subject", Value: m.Subject}, {Key: "from_name", Value: m.FromName},
		{Key: "from_addr", Value: m.FromAddr}, {Key: "date_ts", Value: m.DateTS},
	}
}

func (l Limits) TopCounts(values []string) []any {
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
	out := []any{}
	for _, label := range order[:min(len(order), l.TopLabels)] {
		out = append(out, py.Dict{{Key: "label", Value: label}, {Key: "count", Value: counts[label]}})
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
		subject := py.Strip(r.Subject)
		if subject != "" && !seen[subject] {
			seen[subject] = true
			out = append(out, py.Clip(subject, 120))
		}
		if len(out) >= l.SampleSubjects {
			break
		}
	}
	return out
}

type Result struct {
	Candidates int
	Created    int
}

func (r Result) Dict() py.Dict {
	return py.Dict{{Key: "candidates", Value: r.Candidates}, {Key: "created", Value: r.Created}}
}

func (r Result) MarshalJSON() ([]byte, error) { return []byte(py.JSONDumps(r.Dict())), nil }

func SnapshotList(snaps []mailops.Snapshot) []any {
	out := make([]any, len(snaps))
	for i, s := range snaps {
		out[i] = s.Dict()
	}
	return out
}
