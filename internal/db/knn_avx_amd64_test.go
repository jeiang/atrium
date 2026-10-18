//go:build !modernc

package db

import (
	"math"
	"sort"
	"strings"
	"testing"
)

func TestKNNOnAVXPath(t *testing.T) {
	db := openTest(t)
	var debug string
	if err := db.QueryRow("SELECT vec_debug()").Scan(&debug); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(debug, "Build flags: avx") {
		t.Fatalf("sqlite-vec is built without AVX:\n%s", debug)
	}
	// l2_sqr_float_avx handles L2 over float vectors whose dimension is a multiple of 16.
	const dim, rows, k = 32, 200, 10
	if _, err := db.Exec("CREATE VIRTUAL TABLE v USING vec0(embedding FLOAT[32])"); err != nil {
		t.Fatal(err)
	}
	vec := func(seed int) []float32 {
		v := make([]float32, dim)
		for i := range v {
			v[i] = float32(math.Sin(float64(seed*dim+i) * 0.7))
		}
		return v
	}
	query := vec(rows + 1)
	type hit struct {
		rowid    int64
		distance float64
	}
	want := make([]hit, rows)
	for r := range rows {
		v := vec(r)
		if _, err := db.Exec("INSERT INTO v(rowid, embedding) VALUES (?, ?)", r+1, SerializeFloat32(v)); err != nil {
			t.Fatal(err)
		}
		var sum float64
		for i := range v {
			d := float64(v[i] - query[i])
			sum += d * d
		}
		want[r] = hit{int64(r + 1), math.Sqrt(sum)}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].distance < want[j].distance })
	res, err := db.Query("SELECT rowid, distance FROM v WHERE embedding MATCH ? AND k = ?", SerializeFloat32(query), k)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Close() }()
	var got []hit
	for res.Next() {
		var h hit
		if err := res.Scan(&h.rowid, &h.distance); err != nil {
			t.Fatal(err)
		}
		got = append(got, h)
	}
	if err := res.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != k {
		t.Fatalf("got %d rows, want %d", len(got), k)
	}
	for i, h := range got {
		if h.rowid != want[i].rowid || math.Abs(h.distance-want[i].distance) > 1e-4 {
			t.Errorf("hit %d = %+v, want %+v", i, h, want[i])
		}
	}
}
