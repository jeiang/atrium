// Package scanutil is scanutil.py: the fingerprint digest and preview helpers the proposal scanners share.
package scanutil

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/py"
)

const (
	SampleSubjects = 5
	TopLabels      = 5
)

// Digest is digest(*parts) for str and int parts: sha256 of each str(part) and a NUL, 16 hex digits.
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

// Message is the message columns the previews show.
type Message struct {
	ID       int64
	Subject  string
	FromName string
	FromAddr string
	DateTS   float64
}

// View is message_view.
func (m Message) View() py.Dict {
	return py.Dict{
		{Key: "id", Value: m.ID}, {Key: "subject", Value: m.Subject}, {Key: "from_name", Value: m.FromName},
		{Key: "from_addr", Value: m.FromAddr}, {Key: "date_ts", Value: m.DateTS},
	}
}

// TopCounts is top_counts(values, TopLabels): the most common non-empty values, ties by value.
func TopCounts(values []string) []any {
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
	for _, label := range order[:min(len(order), TopLabels)] {
		out = append(out, py.Dict{{Key: "label", Value: label}, {Key: "count", Value: counts[label]}})
	}
	return out
}

// SampleSubjectsOf is sample_subjects(rows, SampleSubjects): distinct stripped subjects, newest first, clipped to
// 120 characters.
func SampleSubjectsOf(rows []Message) []string {
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
		if len(out) >= SampleSubjects {
			break
		}
	}
	return out
}
