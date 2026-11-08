package textutil

import "golang.org/x/text/cases"

func Casefold(s string) string { return cases.Fold().String(s) }

func Clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
