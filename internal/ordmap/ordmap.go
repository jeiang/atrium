package ordmap

import "slices"

type Map[K comparable, V any] struct {
	keys   []K
	values map[K]V
}

func (m *Map[K, V]) Set(k K, v V) {
	if m.values == nil {
		m.values = map[K]V{}
	}
	if _, seen := m.values[k]; !seen {
		m.keys = append(m.keys, k)
	}
	m.values[k] = v
}

func (m *Map[K, V]) Get(k K) (V, bool) {
	v, ok := m.values[k]
	return v, ok
}

func (m *Map[K, V]) Keys() []K { return m.keys }

func (m *Map[K, V]) Len() int { return len(m.keys) }

func (m *Map[K, V]) Sorted(cmp func(a, b K) int) []K {
	out := slices.Clone(m.keys)
	slices.SortFunc(out, cmp)
	return out
}

type Entry[K comparable] struct {
	Key   K
	Count int
}

type Counter[K comparable] struct {
	m Map[K, int]
}

func (c *Counter[K]) Add(k K) {
	n, _ := c.m.Get(k)
	c.m.Set(k, n+1)
}

func (c *Counter[K]) Get(k K) int {
	n, _ := c.m.Get(k)
	return n
}

func (c *Counter[K]) Total() int {
	total := 0
	for _, k := range c.m.Keys() {
		total += c.Get(k)
	}
	return total
}

func (c *Counter[K]) MostCommon(n int) []Entry[K] {
	out := make([]Entry[K], 0, c.m.Len())
	for _, k := range c.m.Keys() {
		out = append(out, Entry[K]{k, c.Get(k)})
	}
	slices.SortStableFunc(out, func(a, b Entry[K]) int { return b.Count - a.Count })
	return out[:min(n, len(out))]
}
