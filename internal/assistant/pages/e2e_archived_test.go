package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestArchivedProjectRefusesAddingContent(t *testing.T) {
	s := newSite(t)
	project := s.newProject()
	id := fmt.Sprint(project)
	note := upload{"note.md", "text/markdown", []byte("# Note\nhello")}
	status(t, s.multipart("/projects/"+id+"/files", nil, []upload{note}, hx, "true"), http.StatusOK)
	fileID := s.count("SELECT id FROM thread_files WHERE project_id = ?", project)
	thread := s.newThread("Loose", 0)
	inside := s.newThread("Inside", project)

	status(t, s.post("/projects/"+id+"/archive", url.Values{"archived": {"1"}}), http.StatusSeeOther)
	files := s.count("SELECT COUNT(*) FROM thread_files")
	allThreads := s.count("SELECT COUNT(*) FROM threads")

	added := s.multipart("/projects/"+id+"/files", nil, []upload{note}, hx, "true")
	mustContain(t, status(t, added, http.StatusUnprocessableEntity), "“Kitchen” is archived. Restore it to add files.", `role="alert"`, "note.md")
	status(t, s.multipart("/projects/"+id+"/files", nil, []upload{note}), http.StatusUnprocessableEntity)

	removed := s.post(fmt.Sprintf("/projects/%s/files/%d/delete", id, fileID), nil, hx, "true")
	mustContain(t, status(t, removed, http.StatusUnprocessableEntity), "Restore it to remove files.", "note.md")
	status(t, s.post(fmt.Sprintf("/projects/%s/files/%d/delete", id, fileID), nil), http.StatusUnprocessableEntity)

	started := s.multipart("/chat", map[string]string{"text": "hello", "project_id": id}, []upload{note}, hx, "true")
	mustContain(t, status(t, started, http.StatusUnprocessableEntity), "Project archived", "Restore it to start chats in it.")
	if got := started.Header.Get("HX-Reswap"); got != "none" {
		t.Errorf("HX-Reswap %q, want none", got)
	}
	status(t, s.multipart("/chat", map[string]string{"text": "hello", "project_id": id}, nil), http.StatusUnprocessableEntity)

	moved := s.post(fmt.Sprintf("/chat/%d/project", thread), url.Values{"project_id": {id}}, hx, "true")
	mustContain(t, status(t, moved, http.StatusUnprocessableEntity), "Project archived", "Restore it to move chats into it.")
	status(t, s.post(fmt.Sprintf("/chat/%d/project", thread), url.Values{"project_id": {id}}), http.StatusUnprocessableEntity)
	branch := s.post(fmt.Sprintf("/chat/%d/branch", inside), url.Values{"seq": {"1"}}, hx, "true")
	mustContain(t, status(t, branch, http.StatusUnprocessableEntity), "Project archived", "Restore it to start chats in it.")
	status(t, s.post(fmt.Sprintf("/chat/%d/branch", inside), url.Values{"seq": {"1"}}), http.StatusUnprocessableEntity)
	status(t, s.post(fmt.Sprintf("/chat/%d/project", thread), url.Values{"project_id": {"999"}}), http.StatusNotFound)
	status(t, s.multipart("/chat", map[string]string{"text": "hello", "project_id": "999"}, nil), http.StatusNotFound)
	status(t, s.multipart("/projects/999/files", nil, []upload{note}), http.StatusNotFound)

	if n := s.count("SELECT COUNT(*) FROM thread_files"); n != files {
		t.Errorf("thread_files %d, want %d", n, files)
	}
	if n := s.count("SELECT COUNT(*) FROM threads"); n != allThreads {
		t.Errorf("threads %d, want %d", n, allThreads)
	}
	if n := s.count("SELECT COUNT(*) FROM thread_files WHERE id = ?", fileID); n != 1 {
		t.Errorf("the file row count is %d, want 1", n)
	}
	download := s.get(fmt.Sprintf("/projects/%s/files/%d/download", id, fileID))
	if download.StatusCode != http.StatusOK {
		t.Errorf("download %d", download.StatusCode)
	}
	status(t, s.get("/projects/"+id), http.StatusOK)
	status(t, s.post("/projects/"+id, with(kitchen, "name", "Renamed")), http.StatusSeeOther)

	status(t, s.post("/projects/"+id+"/archive", url.Values{"archived": {"0"}}), http.StatusSeeOther)
	status(t, s.multipart("/projects/"+id+"/files", nil, []upload{note}, hx, "true"), http.StatusOK)
	status(t, s.multipart("/chat", map[string]string{"text": "", "project_id": id}, nil, hx, "true"), http.StatusNoContent)
	status(t, s.post(fmt.Sprintf("/chat/%d/project", thread), url.Values{"project_id": {id}}), http.StatusSeeOther)
	status(t, s.post(fmt.Sprintf("/projects/%s/files/%d/delete", id, fileID), nil, hx, "true"), http.StatusOK)
}

func TestUploadBodyLimits(t *testing.T) {
	s := newSite(t)
	fields := map[string]string{"text": "hello"}
	for i := 0; i <= maxParts; i++ {
		fields[fmt.Sprintf("f%d", i)] = "x"
	}
	status(t, s.multipart("/chat", fields, nil, hx, "true"), http.StatusBadRequest)
	heavy := map[string]string{"text": "hello"}
	for i := 0; i < 5; i++ {
		heavy[fmt.Sprintf("f%d", i)] = strings.Repeat("x", fieldBytes-1)
	}
	status(t, s.multipart("/chat", heavy, nil, hx, "true"), http.StatusBadRequest)
	status(t, s.multipart("/chat", map[string]string{"text": strings.Repeat("x", fieldBytes+1)}, nil, hx, "true"), http.StatusBadRequest)
	if n := s.count("SELECT COUNT(*) FROM threads WHERE deleted_at IS NULL"); n != 0 {
		t.Errorf("refused bodies left %d threads", n)
	}
}
