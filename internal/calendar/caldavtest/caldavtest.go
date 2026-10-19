// Package caldavtest is an iCloud CalDAV stand-in for end-to-end tests: principal and calendar discovery over
// PROPFIND, event storage over PUT, REPORT and DELETE.
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

// Server is the fake. Calendars lists the display names it serves (the first is "Home" by default).
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

const header = `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`

func propstat(prop string) string {
	return "<D:propstat><D:prop>" + prop + "</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>"
}

func response(href, prop string) string {
	return "<D:response><D:href>" + href + "</D:href>" + propstat(prop) + "</D:response>"
}

// New starts the server and closes it when t ends.
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

// Objects returns the stored events by path.
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
			if strings.HasPrefix(path, r.URL.Path) {
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

// CalendarPath is the path of the i-th calendar.
func CalendarPath(i int) string { return calendarPath(i) }
