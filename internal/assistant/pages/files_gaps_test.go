package pages

import (
	"fmt"
	"net/http"
	"os"
	"testing"
)

func projectWithBrief(t *testing.T, s *site) int64 {
	t.Helper()
	s.exec("INSERT INTO projects (name, instructions, effort, context_limit, archived, created_at, updated_at) VALUES ('Home', '', 'high', 90000, 0, 1, 1)")
	status(t, s.multipart("/projects/1/files", nil, []upload{{"brief.md", "text/markdown", []byte("# Brief\nhello")}}, hx, "true"), http.StatusOK)
	var fid int64
	if err := s.db.Read.QueryRow("SELECT id FROM thread_files WHERE project_id = 1").Scan(&fid); err != nil {
		t.Fatal(err)
	}
	return fid
}

func TestProjectFileMissingFromStorageIs404(t *testing.T) {
	s := newSite(t)
	fid := projectWithBrief(t, s)
	var path string
	if err := s.db.Read.QueryRow("SELECT path FROM thread_files WHERE id = ?", fid).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	gone := s.get(fmt.Sprintf("/projects/1/files/%d/download", fid))
	status(t, gone, http.StatusNotFound)
}

func TestUploadsSkipNamelessAndEmptyParts(t *testing.T) {
	s := newSite(t)
	s.exec("INSERT INTO projects (name, instructions, effort, context_limit, archived, created_at, updated_at) VALUES ('Home', '', 'high', 90000, 0, 1, 1)")
	only := []upload{{"", "text/plain", []byte("nameless")}, {"empty.txt", "text/plain", nil}}
	mustContain(t, status(t, s.multipart("/projects/1/files", nil, only, hx, "true"), http.StatusUnprocessableEntity), "Choose at least one file.")
	if n := s.count("SELECT COUNT(*) FROM thread_files"); n != 0 {
		t.Fatalf("nameless and empty parts stored %d files", n)
	}
	mixed := append(only, upload{"kept.txt", "text/plain", []byte("kept")})
	status(t, s.multipart("/projects/1/files", nil, mixed, hx, "true"), http.StatusOK)
	if n := s.count("SELECT COUNT(*) FROM thread_files"); n != 1 {
		t.Fatalf("stored %d files, want 1", n)
	}
	if n := s.count("SELECT COUNT(*) FROM thread_files WHERE name = 'kept.txt'"); n != 1 {
		t.Errorf("kept.txt rows = %d", n)
	}
}
