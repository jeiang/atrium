package pages

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestLibraryFilesListsThreadAndProjectFilesNewestFirst(t *testing.T) {
	s := newSite(t)
	empty := status(t, s.get("/library/files"), http.StatusOK)
	mustContain(t, empty, "<html", "No files yet")

	thread := s.newThread("Budget talk", 0)
	project := s.newProject()
	untitled := s.newThread("", 0)
	s.exec("INSERT INTO thread_files (thread_id, name, media_type, bytes, path, origin, created_at) VALUES (?, 'budget.csv', 'text/csv', 2048, 'x/budget.csv', 'upload', 100)", thread)
	s.exec("INSERT INTO thread_files (project_id, name, media_type, bytes, path, origin, created_at) VALUES (?, 'plan.pdf', 'application/pdf', 10, 'x/plan.pdf', 'upload', 200)", project)
	s.exec("UPDATE threads SET title = NULL WHERE id = ?", untitled)
	s.exec("INSERT INTO thread_files (thread_id, name, media_type, bytes, path, origin, created_at) VALUES (?, 'notes.txt', 'text/plain', 1, 'x/notes.txt', 'upload', 50)", untitled)
	budget := s.count("SELECT id FROM thread_files WHERE name = 'budget.csv'")
	plan := s.count("SELECT id FROM thread_files WHERE name = 'plan.pdf'")

	page := status(t, s.get("/library/files"), http.StatusOK)
	mustContain(t, page, "<html", "budget.csv", "Budget talk", fmt.Sprintf(`href="/chat/%d"`, thread), fmt.Sprintf(`href="/chat/%d/files/%d/download"`, thread, budget), `aria-label="Download budget.csv"`,
		"plan.pdf", "Kitchen", fmt.Sprintf(`href="/projects/%d/edit"`, project), fmt.Sprintf(`href="/projects/%d/files/%d/download"`, project, plan),
		"notes.txt", "Untitled thread")
	mustNotContain(t, page, "No files yet")
	if strings.Index(page, "plan.pdf") >= strings.Index(page, "budget.csv") || strings.Index(page, "budget.csv") >= strings.Index(page, "notes.txt") {
		t.Errorf("files are not newest first: %s", mainOf(page))
	}
}
