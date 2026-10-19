package py

import (
	"slices"
	"sort"
)

// Opcode is one difflib opcode: Tag is "replace", "delete", "insert" or "equal" over a[I1:I2] and b[J1:J2].
type Opcode struct {
	Tag            string
	I1, I2, J1, J2 int
}

type block struct{ i, j, k int }

type matcher struct {
	a, b []string
	b2j  map[string][]int
}

func newMatcher(a, b []string) *matcher {
	m := &matcher{a: a, b: b, b2j: map[string][]int{}}
	for i, elt := range b {
		m.b2j[elt] = append(m.b2j[elt], i)
	}
	return m
}

func (m *matcher) longest(alo, ahi, blo, bhi int) block {
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		next := map[int]int{}
		for _, j := range m.b2j[m.a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			next[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = next
	}
	for besti > alo && bestj > blo && m.a[besti-1] == m.b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && m.a[besti+bestsize] == m.b[bestj+bestsize] {
		bestsize++
	}
	return block{besti, bestj, bestsize}
}

func (m *matcher) blocks() []block {
	type span struct{ alo, ahi, blo, bhi int }
	queue := []span{{0, len(m.a), 0, len(m.b)}}
	var found []block
	for len(queue) > 0 {
		s := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x := m.longest(s.alo, s.ahi, s.blo, s.bhi)
		if x.k == 0 {
			continue
		}
		found = append(found, x)
		if s.alo < x.i && s.blo < x.j {
			queue = append(queue, span{s.alo, x.i, s.blo, x.j})
		}
		if x.i+x.k < s.ahi && x.j+x.k < s.bhi {
			queue = append(queue, span{x.i + x.k, s.ahi, x.j + x.k, s.bhi})
		}
	}
	sort.Slice(found, func(p, q int) bool { return found[p].i < found[q].i })
	var merged []block
	var cur block
	for _, b := range found {
		if cur.i+cur.k == b.i && cur.j+cur.k == b.j {
			cur.k += b.k
			continue
		}
		if cur.k != 0 {
			merged = append(merged, cur)
		}
		cur = b
	}
	if cur.k != 0 {
		merged = append(merged, cur)
	}
	return append(merged, block{len(m.a), len(m.b), 0})
}

func (m *matcher) opcodes() []Opcode {
	var out []Opcode
	i, j := 0, 0
	for _, b := range m.blocks() {
		tag := ""
		switch {
		case i < b.i && j < b.j:
			tag = "replace"
		case i < b.i:
			tag = "delete"
		case j < b.j:
			tag = "insert"
		}
		if tag != "" {
			out = append(out, Opcode{tag, i, b.i, j, b.j})
		}
		i, j = b.i+b.k, b.j+b.k
		if b.k != 0 {
			out = append(out, Opcode{"equal", b.i, i, b.j, j})
		}
	}
	return out
}

// GroupedOpcodes is difflib.SequenceMatcher(None, a, b, autojunk=False).get_grouped_opcodes(n).
func GroupedOpcodes(a, b []string, n int) [][]Opcode {
	codes := newMatcher(a, b).opcodes()
	if len(codes) == 0 {
		codes = []Opcode{{"equal", 0, 1, 0, 1}}
	}
	codes = slices.Clone(codes)
	if c := codes[0]; c.Tag == "equal" {
		codes[0] = Opcode{c.Tag, max(c.I1, c.I2-n), c.I2, max(c.J1, c.J2-n), c.J2}
	}
	if last := len(codes) - 1; codes[last].Tag == "equal" {
		c := codes[last]
		codes[last] = Opcode{c.Tag, c.I1, min(c.I2, c.I1+n), c.J1, min(c.J2, c.J1+n)}
	}
	var groups [][]Opcode
	var group []Opcode
	for _, c := range codes {
		if c.Tag == "equal" && c.I2-c.I1 > 2*n {
			group = append(group, Opcode{c.Tag, c.I1, min(c.I2, c.I1+n), c.J1, min(c.J2, c.J1+n)})
			groups = append(groups, group)
			group = nil
			c.I1, c.J1 = max(c.I1, c.I2-n), max(c.J1, c.J2-n)
		}
		group = append(group, c)
	}
	if len(group) > 1 || len(group) == 1 && group[0].Tag != "equal" {
		groups = append(groups, group)
	}
	return groups
}
