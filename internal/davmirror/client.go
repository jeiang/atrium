package davmirror

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
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
	collect    string
	query      func(uid string) string
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
	collect: `<C:supported-calendar-component-set/>`,
	query: func(uid string) string {
		return `<C:calendar-query xmlns:D="DAV:" xmlns:C="` + nsCal + `"><D:prop><D:getetag/><C:calendar-data/></D:prop>` +
			`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:prop-filter name="UID">` +
			`<C:text-match collation="i;octet">` + html.EscapeString(uid) + `</C:text-match></C:prop-filter></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`
	},
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
	DisplayName  string       `xml:"DAV: displayname"`
	Components   []struct {
		Name string `xml:"name,attr"`
	} `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set>comp"`
	CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	AddressData  string `xml:"urn:ietf:params:xml:ns:carddav address-data"`
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

type CollectionRef struct {
	Name       string
	PathName   string
	URL        *url.URL
	Components []string
}

type ObjectRef struct {
	URL  *url.URL
	Body []byte
}

type Client struct {
	http *http.Client
	base *url.URL
}

func New(httpClient *http.Client, endpoint string) (*Client, error) {
	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{http: httpClient, base: base}, nil
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

func (c *Client) Collections(ctx context.Context, k Kind) ([]CollectionRef, error) {
	found, err := do(ctx, c.http, "PROPFIND", c.base, "0", `<D:propfind xmlns:D="DAV:"><D:prop><D:current-user-principal/></D:prop></D:propfind>`)
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
	principal, err := resolve(c.base, principalRef)
	if err != nil {
		return nil, err
	}
	found, err = do(ctx, c.http, "PROPFIND", principal, "0", `<D:propfind `+k.namespaces+`><D:prop>`+k.homeProp+`</D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	homeRef := ""
	for _, r := range found {
		p := r.prop()
		switch k.homeProp {
		case Calendars.homeProp:
			homeRef = p.CalHome.Href
		case Contacts.homeProp:
			homeRef = p.CardHome.Href
		}
		if homeRef != "" {
			break
		}
	}
	if homeRef == "" {
		return nil, errors.New("dav: no home set")
	}
	home, err := resolve(principal, homeRef)
	if err != nil {
		return nil, err
	}
	found, err = do(ctx, c.http, "PROPFIND", home, "1", `<D:propfind `+k.namespaces+`><D:prop><D:resourcetype/><D:displayname/>`+k.collect+`</D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	var out []CollectionRef
	for _, r := range found {
		p := r.prop()
		if !k.isCollect(p.ResourceType) {
			continue
		}
		collection, err := resolve(home, r.Href)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(p.DisplayName)
		pathName := path.Base(strings.TrimRight(collection.Path, "/"))
		if name == "" {
			name = pathName
		}
		ref := CollectionRef{Name: name, PathName: pathName, URL: collection}
		for _, component := range p.Components {
			ref.Components = append(ref.Components, component.Name)
		}
		out = append(out, ref)
	}
	return out, nil
}

func (c *Client) Fetch(ctx context.Context, k Kind) ([]Collection, error) {
	refs, err := c.Collections(ctx, k)
	if err != nil {
		return nil, err
	}
	var out []Collection
	for _, ref := range refs {
		objects, err := do(ctx, c.http, "REPORT", ref.URL, "1", k.report)
		if err != nil {
			return nil, err
		}
		coll := Collection{Name: ref.PathName}
		for _, o := range objects {
			if data := k.dataOf(o.prop()); data != "" {
				coll.Objects = append(coll.Objects, Object{Name: path.Base(o.Href), Body: []byte(data)})
			}
		}
		out = append(out, coll)
	}
	return out, nil
}

func (c *Client) Query(ctx context.Context, collection *url.URL, k Kind, uid string) ([]ObjectRef, error) {
	body := k.report
	if k.query != nil {
		body = k.query(uid)
	}
	found, err := do(ctx, c.http, "REPORT", collection, "1", body)
	if err != nil {
		return nil, err
	}
	var out []ObjectRef
	for _, r := range found {
		data := k.dataOf(r.prop())
		if data == "" {
			continue
		}
		target, err := resolve(collection, r.Href)
		if err != nil {
			return nil, err
		}
		out = append(out, ObjectRef{URL: target, Body: []byte(data)})
	}
	return out, nil
}

func (c *Client) Put(ctx context.Context, target *url.URL, body []byte, contentType string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	return c.expect(req, http.MethodPut, target)
}

func (c *Client) Delete(ctx context.Context, target *url.URL) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target.String(), nil)
	if err != nil {
		return err
	}
	return c.expect(req, http.MethodDelete, target)
}

func (c *Client) expect(req *http.Request, method string, target *url.URL) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return &StatusError{Method: method, URL: target.Redacted(), Status: resp.StatusCode}
	}
	return nil
}

func Fetch(ctx context.Context, client *http.Client, endpoint string, k Kind) ([]Collection, error) {
	c, err := New(client, endpoint)
	if err != nil {
		return nil, err
	}
	return c.Fetch(ctx, k)
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
