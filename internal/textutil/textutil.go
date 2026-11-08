package textutil

import "golang.org/x/text/cases"

func Casefold(s string) string { return cases.Fold().String(s) }
