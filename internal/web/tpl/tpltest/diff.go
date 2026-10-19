package tpltest

import (
	"fmt"
	"strings"
	"testing"
)

// Rewrite maps a substring of the Jinja side's canonical tokens to the Go spelling it is documented to take.
type Rewrite struct {
	From string
	To   string
	Why  string
	Only func(token string) bool
}

// Diff is the line diff of two canonical forms, empty when they are equal.
func Diff(want, got []string) string {
	if len(want) == len(got) {
		same := true
		for i := range want {
			if want[i] != got[i] {
				same = false
				break
			}
		}
		if same {
			return ""
		}
	}
	rows := make([][]int, len(want)+1)
	for i := range rows {
		rows[i] = make([]int, len(got)+1)
	}
	for i := len(want) - 1; i >= 0; i-- {
		for j := len(got) - 1; j >= 0; j-- {
			if want[i] == got[j] {
				rows[i][j] = rows[i+1][j+1] + 1
			} else {
				rows[i][j] = max(rows[i+1][j], rows[i][j+1])
			}
		}
	}
	var b strings.Builder
	for i, j := 0, 0; i < len(want) || j < len(got); {
		switch {
		case i < len(want) && j < len(got) && want[i] == got[j]:
			i++
			j++
		case j < len(got) && (i == len(want) || rows[i][j+1] >= rows[i+1][j]):
			fmt.Fprintf(&b, "+ %s\n", got[j])
			j++
		default:
			fmt.Fprintf(&b, "- %s\n", want[i])
			i++
		}
	}
	return b.String()
}

// Compare fails t unless the Go render equals the Jinja render once the documented rewrites are applied to the
// Jinja side.
func Compare(t testing.TB, want, got string, documented ...Rewrite) {
	t.Helper()
	wantTokens := Canonical(want)
	for i, token := range wantTokens {
		for _, r := range documented {
			if r.Only == nil || r.Only(token) {
				token = strings.ReplaceAll(token, r.From, r.To)
			}
		}
		wantTokens[i] = token
	}
	if diff := Diff(wantTokens, Canonical(got)); diff != "" {
		t.Errorf("canonical diff (- Jinja, + Go):\n%s", diff)
	}
}
