package caldavtest

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type Server struct {
	*httptest.Server
	mu        sync.Mutex
	Calendars []string
	objects   map[string]string
	Puts      []string
	Deleted   []string
	User      string
	Password  string
}

const header = `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CR="urn:ietf:params:xml:ns:carddav">`

const BookPath = "/addressbooks/card/"

func (s *Server) SetObject(path, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[path] = body
}

func (s *Server) RemoveObject(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, path)
}

func propstat(prop string) string {
	return "<D:propstat><D:prop>" + prop + "</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>"
}

func response(href, prop string) string {
	return "<D:response><D:href>" + href + "</D:href>" + propstat(prop) + "</D:response>"
}

func New(t testing.TB, user, password string, calendars ...string) *Server {
	t.Helper()
	if len(calendars) == 0 {
		calendars = []string{"Home"}
	}
	s := &Server{Calendars: calendars, objects: map[string]string{}, User: user, Password: password}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Objects() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.objects {
		out[k] = v
	}
	return out
}

func calendarPath(i int) string { return fmt.Sprintf("/calendars/cal%d/", i) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if user, pass, ok := r.BasicAuth(); !ok || user != s.User || pass != s.Password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case "PROPFIND":
		s.propfind(w, r, string(body))
	case http.MethodPut:
		s.objects[r.URL.Path] = string(body)
		s.Puts = append(s.Puts, string(body))
		w.WriteHeader(http.StatusCreated)
	case "REPORT":
		var out strings.Builder
		out.WriteString(header)
		for path, data := range s.objects {
			if !strings.HasPrefix(path, r.URL.Path) {
				continue
			}
			if strings.HasPrefix(path, "/addressbooks/") {
				out.WriteString(response(path, `<D:getetag>"1"</D:getetag><CR:address-data>`+html.EscapeString(data)+`</CR:address-data>`))
			} else {
				out.WriteString(response(path, `<D:getetag>"1"</D:getetag><C:calendar-data>`+html.EscapeString(data)+`</C:calendar-data>`))
			}
		}
		out.WriteString("</D:multistatus>")
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, out.String())
	case http.MethodDelete:
		if _, ok := s.objects[r.URL.Path]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(s.objects, r.URL.Path)
		s.Deleted = append(s.Deleted, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) propfind(w http.ResponseWriter, r *http.Request, body string) {
	var out strings.Builder
	out.WriteString(header)
	switch {
	case strings.Contains(body, "current-user-principal"):
		out.WriteString(response(r.URL.Path, `<D:current-user-principal><D:href>/principal/</D:href></D:current-user-principal>`))
	case strings.Contains(body, "calendar-home-set"):
		out.WriteString(response(r.URL.Path, `<C:calendar-home-set><D:href>`+s.URL+`/calendars/</D:href></C:calendar-home-set>`))
	case strings.Contains(body, "addressbook-home-set"):
		out.WriteString(response(r.URL.Path, `<CR:addressbook-home-set><D:href>`+s.URL+`/addressbooks/</D:href></CR:addressbook-home-set>`))
	case strings.HasPrefix(r.URL.Path, "/addressbooks/"):
		out.WriteString(response("/addressbooks/", `<D:resourcetype><D:collection/></D:resourcetype>`))
		out.WriteString(response(BookPath, `<D:resourcetype><D:collection/><CR:addressbook/></D:resourcetype>`))
	default:
		out.WriteString(response("/calendars/", `<D:resourcetype><D:collection/></D:resourcetype><D:displayname>calendars</D:displayname>`))
		for i, name := range s.Calendars {
			out.WriteString(response(calendarPath(i), `<D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:displayname>`+
				html.EscapeString(name)+`</D:displayname><C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set>`))
		}
	}
	out.WriteString("</D:multistatus>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, out.String())
}

func CalendarPath(i int) string { return calendarPath(i) }
