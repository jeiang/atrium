package rx

import (
	"slices"
	"sort"
)

type casedRune struct{ r, lower, upper rune }

func casedIndex(c rune) (int, bool) {
	return sort.Find(len(pyCased), func(i int) int { return int(c - pyCased[i].r) })
}

func pyIsCased(c rune) bool {
	_, ok := casedIndex(c)
	return ok
}

func pyLower(c rune) rune {
	if i, ok := casedIndex(c); ok {
		return pyCased[i].lower
	}
	return c
}

func pyUpper(c rune) rune {
	if i, ok := casedIndex(c); ok {
		return pyCased[i].upper
	}
	return c
}

var lowerPreimage = func() map[rune][]rune {
	m := map[rune][]rune{}
	for _, c := range pyCased {
		if c.lower != c.r {
			m[c.lower] = append(m[c.lower], c.r)
		}
	}
	return m
}()

func foldLiteral(r rune) []rune {
	if !pyIsCased(r) {
		return []rune{r}
	}
	lo := pyLower(r)
	var out []rune
	for _, x := range append([]rune{lo}, caseFixes[lo]...) {
		if pyLower(x) == x {
			out = append(out, x)
		}
		out = append(out, lowerPreimage[x]...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func casedIn(r [2]rune) bool {
	i := sort.Search(len(pyCased), func(i int) bool { return pyCased[i].r >= r[0] })
	return i < len(pyCased) && pyCased[i].r <= r[1]
}

func foldSet(ranges [][2]rune) [][2]rune {
	hasCased := false
	for _, r := range ranges {
		if casedIn(r) || (r[1] > 0xffff && r[0] != r[1]) {
			hasCased = true
			break
		}
	}
	if !hasCased {
		return ranges
	}
	var bmp, tail, base [][2]rune
	lowered := map[rune]bool{}
	tailLit := map[rune]bool{}
	for _, r := range ranges {
		if r[0] == r[1] && r[0] > 0xffff {
			l := pyLower(r[0])
			tailLit[l] = true
			base = append(base, [2]rune{l, l})
			continue
		}
		if r[0] <= 0xffff {
			bmp = append(bmp, [2]rune{r[0], min(r[1], 0xffff)})
		}
		if r[1] > 0xffff {
			tail = append(tail, r)
		}
	}
	for _, r := range bmp {
		i := sort.Search(len(pyCased), func(i int) bool { return pyCased[i].r >= r[0] })
		for ; i < len(pyCased) && pyCased[i].r <= r[1]; i++ {
			l := pyLower(pyCased[i].r)
			lowered[l] = true
			for _, f := range caseFixes[l] {
				lowered[f] = true
			}
		}
	}
	for l := range lowered {
		base = append(base, [2]rune{l, l})
	}
	base = append(append(base, bmp...), tail...)
	in := func(x rune) bool {
		if x <= 0xffff && (lowered[x] || (!pyIsCased(x) && inRanges(bmp, x))) {
			return true
		}
		if tailLit[x] {
			return true
		}
		for _, r := range tail {
			if u := pyUpper(x); (x >= r[0] && x <= r[1]) || (u >= r[0] && u <= r[1]) {
				return true
			}
		}
		return false
	}
	out := subtractCased(normalize(base))
	for _, c := range pyCased {
		if in(c.lower) {
			out = append(out, [2]rune{c.r, c.r})
		}
	}
	return normalize(out)
}

func subtractCased(ranges [][2]rune) [][2]rune {
	var out [][2]rune
	for _, r := range ranges {
		lo := r[0]
		i := sort.Search(len(pyCased), func(i int) bool { return pyCased[i].r >= r[0] })
		for ; i < len(pyCased) && pyCased[i].r <= r[1]; i++ {
			if c := pyCased[i].r; c > lo {
				out = append(out, [2]rune{lo, c - 1})
			}
			lo = pyCased[i].r + 1
		}
		if lo <= r[1] {
			out = append(out, [2]rune{lo, r[1]})
		}
	}
	return out
}

func normalize(ranges [][2]rune) [][2]rune {
	if len(ranges) == 0 {
		return nil
	}
	rs := slices.Clone(ranges)
	slices.SortFunc(rs, func(a, b [2]rune) int { return int(a[0] - b[0]) })
	out := rs[:1]
	for _, r := range rs[1:] {
		last := &out[len(out)-1]
		if r[0] <= last[1]+1 {
			last[1] = max(last[1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

func inRanges(ranges [][2]rune, c rune) bool {
	for _, r := range ranges {
		if c >= r[0] && c <= r[1] {
			return true
		}
	}
	return false
}
