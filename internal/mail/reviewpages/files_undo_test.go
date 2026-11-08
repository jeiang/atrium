package reviewpages

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestUndoFileWriteRestoresCreatesAndRefuses(t *testing.T) {
	s := newSite(t)
	s.thread()
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")

	if err := os.WriteFile(path, []byte("new"), 0o666); err != nil {
		t.Fatal(err)
	}
	undo := fmt.Sprintf(`{"op":"file_restore","path":%q,"before":null,"sha":%q}`, path, fileSHA("new"))
	id := s.write(100, "file_write", "note.txt", 1, nil, `"new"`, undo)
	status(t, s.post(fmt.Sprintf("/activity/write/%d/undo", id), nil), http.StatusSeeOther)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file still exists: %v", err)
	}

	if err := os.WriteFile(path, []byte("new"), 0o666); err != nil {
		t.Fatal(err)
	}
	undo = fmt.Sprintf(`{"op":"file_restore","path":%q,"before":"old","sha":%q}`, path, fileSHA("new"))
	id = s.write(101, "file_write", "note.txt", 1, `"old"`, `"new"`, undo)
	status(t, s.post(fmt.Sprintf("/activity/write/%d/undo", id), nil), http.StatusSeeOther)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("content = %q, %v", data, err)
	}

	if err := os.WriteFile(path, []byte("changed"), 0o666); err != nil {
		t.Fatal(err)
	}
	undo = fmt.Sprintf(`{"op":"file_restore","path":%q,"before":"old","sha":%q}`, path, fileSHA("new"))
	id = s.write(102, "file_write", "note.txt", 1, `"old"`, `"new"`, undo)
	body := status(t, s.post(fmt.Sprintf("/activity/write/%d/undo", id), nil), http.StatusConflict)
	if !strings.Contains(body, "file changed since the write") {
		t.Errorf("refusal body: %s", body)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "changed" {
		t.Errorf("refused undo changed the file to %q", data)
	}
}
