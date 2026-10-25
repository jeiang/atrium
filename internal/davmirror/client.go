package davmirror

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

type Kind struct {
	Ext        string
	homeProp   string
	isCollect  func(resourceType) bool
	report     string
	dataOf     func(prop) string
	namespaces string
}

const (
	nsDAV  = "DAV:"
	nsCal  = "urn:ietf:params:xml:ns:caldav"
	nsCard = "urn:ietf:params:xml:ns:carddav"
)

var Calendars = Kind{
	Ext:        ".ics",
	homeProp:   "<C:calendar-home-set/>",
	namespaces: `xmlns:D="DAV:" xmlns:C="` + nsCal + `"`,
	isCollect:  func(r resourceType) bool { return r.Calendar != nil },
	report: `<C:calendar-query xmlns:D="DAV:" xmlns:C="` + nsCal + `"><D:prop><D:getetag/><C:calendar-data/></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"/></C:comp-filter></C:filter></C:calendar-query>`,
	dataOf: func(p prop) string { return p.CalendarData },
}

var Contacts = Kind{
	Ext:        ".vcf",
	homeProp:   "<C:addressbook-home-set/>",
	namespaces: `xmlns:D="DAV:" xmlns:C="` + nsCard + `"`,
	isCollect:  func(r resourceType) bool { return r.Addressbook != nil },
	report:     `<C:addressbook-query xmlns:D="DAV:" xmlns:C="` + nsCard + `"><D:prop><D:getetag/><C:address-data/></D:prop></C:addressbook-query>`,
	dataOf:     func(p prop) string { return p.AddressData },
}

type resourceType struct {
	Calendar    *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
	Addressbook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook"`
}

type href struct {
	Href string `xml:"DAV: href"`
}

type prop struct {
	Principal    href         `xml:"DAV: current-user-principal"`
	CalHome      href         `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
	CardHome     href         `xml:"urn:ietf:params:xml:ns:carddav addressbook-home-set"`
	ResourceType resourceType `xml:"DAV: resourcetype"`
	CalendarData string       `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	AddressData  string       `xml:"urn:ietf:params:xml:ns:carddav address-data"`
}

type response struct {
	Href     string `xml:"DAV: href"`
	Propstat []struct {
		Prop   prop   `xml:"DAV: prop"`
		Status string `xml:"DAV: status"`
	} `xml:"DAV: propstat"`
}

func (r response) prop() prop {
	for _, ps := range r.Propstat {
		if ps.Status == "" || strings.Contains(ps.Status, " 200 ") {
			return ps.Prop
		}
	}
	return prop{}
}

type StatusError struct {
	Method, URL string
	Status      int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("dav: %s %s: HTTP %d", e.Method, e.URL, e.Status)
}

func (e *StatusError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

func do(ctx context.Context, client *http.Client, method string, target *url.URL, depth, body string) ([]response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target.String(), strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>`+body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", depth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, &StatusError{Method: method, URL: target.Redacted(), Status: resp.StatusCode}
	}
	var ms struct {
		Responses []response `xml:"DAV: response"`
	}
	if err := xml.Unmarshal(raw, &ms); err != nil {
		return nil, err
	}
	return ms.Responses, nil
}

func resolve(base *url.URL, ref string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return nil, err
	}
	return base.ResolveReference(u), nil
}

func Fetch(ctx context.Context, client *http.Client, endpoint string, k Kind) ([]Collection, error) {
	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	found, err := do(ctx, client, "PROPFIND", base, "0", `<D:propfind xmlns:D="DAV:"><D:prop><D:current-user-principal/></D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	principalRef := ""
	for _, r := range found {
		if h := r.prop().Principal.Href; h != "" {
			principalRef = h
		}
	}
	if principalRef == "" {
		return nil, errors.New("dav: no current-user-principal")
	}
	principal, err := resolve(base, principalRef)
	if err != nil {
		return nil, err
	}
	found, err = do(ctx, client, "PROPFIND", principal, "0", `<D:propfind `+k.namespaces+`><D:prop>`+k.homeProp+`</D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	homeRef := ""
	for _, r := range found {
		p := r.prop()
		for _, h := range []string{p.CalHome.Href, p.CardHome.Href} {
			if h != "" {
				homeRef = h
			}
		}
	}
	if homeRef == "" {
		return nil, errors.New("dav: no home set")
	}
	home, err := resolve(principal, homeRef)
	if err != nil {
		return nil, err
	}
	found, err = do(ctx, client, "PROPFIND", home, "1", `<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	var out []Collection
	for _, r := range found {
		if !k.isCollect(r.prop().ResourceType) {
			continue
		}
		collection, err := resolve(home, r.Href)
		if err != nil {
			return nil, err
		}
		objects, err := do(ctx, client, "REPORT", collection, "1", k.report)
		if err != nil {
			return nil, err
		}
		c := Collection{Name: path.Base(strings.TrimRight(collection.Path, "/"))}
		for _, o := range objects {
			if data := k.dataOf(o.prop()); data != "" {
				c.Objects = append(c.Objects, Object{Name: path.Base(o.Href), Body: []byte(data)})
			}
		}
		out = append(out, c)
	}
	return out, nil
}

type basicAuth struct {
	user, password string
	next           http.RoundTripper
}

func (b basicAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth(b.user, b.password)
	return b.next.RoundTrip(r)
}

func BasicClient(base *http.Client, user, password string) *http.Client {
	c := *base
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	c.Transport = basicAuth{user: user, password: password, next: next}
	return &c
}
