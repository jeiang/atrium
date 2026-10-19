package store_test

import (
	"encoding/json"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

func TestFlag(t *testing.T) {
	d := dbtest.File(t)
	if _, err := d.Write.ExecContext(t.Context(), "CREATE TABLE f (a INTEGER, b INTEGER, c INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(t.Context(), "INSERT INTO f VALUES (1, 0, NULL)"); err != nil {
		t.Fatal(err)
	}
	var a, b, c store.Flag
	if err := d.Read.QueryRowContext(t.Context(), "SELECT a, b, c FROM f").Scan(&a, &b, &c); err != nil {
		t.Fatal(err)
	}
	if !a || b || c {
		t.Errorf("scanned %v %v %v", a, b, c)
	}
	out, err := json.Marshal(map[string]store.Flag{"x": true, "y": false})
	if err != nil || string(out) != `{"x":1,"y":0}` {
		t.Errorf("encoded %s, %v", out, err)
	}
	var back map[string]store.Flag
	if err := json.Unmarshal([]byte(`{"x":1,"y":0,"z":true}`), &back); err != nil || !back["x"] || back["y"] || !back["z"] {
		t.Errorf("decoded %v, %v", back, err)
	}
	if err := json.Unmarshal([]byte(`"yes"`), &a); err == nil {
		t.Error("a string decoded as a flag")
	}
	if store.Bit(true) != 1 || store.Bit(false) != 0 {
		t.Error("Bit")
	}
}
