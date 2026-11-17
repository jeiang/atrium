package davmirror

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/jeiang/atrium/internal/logx"
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
	wellKnown  string
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
	wellKnown:  "/.well-known/caldav",
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
	wellKnown:  "/.well-known/carddav",
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

const (
	StepRoot        = "root"
	StepPrincipal   = "principal"
	StepWellKnown   = "well-known"
	StepCollections = "home set"
	StepObjects     = "report"
	maxRedirects    = 3
)

var log = logx.Logger("davmirror")

type StatusError struct {
	Method, URL string
	Status      int
	Step        string
	location    *url.URL
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("dav: %s %s: HTTP %d", e.Method, e.URL, e.Status)
}

func (e *StatusError) Refused() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

type UntrustedError struct {
	Host string
	Step string
}

func (e *UntrustedError) Error() string {
	return fmt.Sprintf("dav: the %s step pointed to %s, which is not the DAV host or its sibling; the credential was not sent", e.Step, e.Host)
}

type guard struct {
	base *url.URL
	next http.RoundTripper
}

func (g guard) RoundTrip(r *http.Request) (*http.Response, error) {
	if !trusted(g.base, r.URL) {
		return nil, &UntrustedError{Host: r.URL.Hostname()}
	}
	return g.next.RoundTrip(r)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func trusted(base, u *url.URL) bool {
	host, home := strings.ToLower(u.Hostname()), strings.ToLower(base.Hostname())
	if u.Scheme != base.Scheme || effectivePort(u) != effectivePort(base) {
		return false
	}
	if host == home {
		return true
	}
	if u.Scheme != "https" || net.ParseIP(host) != nil || net.ParseIP(home) != nil {
		return false
	}
	_, parent, ok := strings.Cut(home, ".")
	_, other, found := strings.Cut(host, ".")
	return ok && found && parent == other && strings.Contains(parent, ".")
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
	guarded := *httpClient
	next := guarded.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	guarded.Transport = guard{base: base, next: next}
	guarded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &guarded, base: base}, nil
}

func do(ctx context.Context, client *http.Client, step, method string, target *url.URL, depth, body string) ([]response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target.String(), strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>`+body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", depth)
	resp, err := client.Do(req)
	if untrusted, ok := errors.AsType[*UntrustedError](err); ok {
		untrusted.Step = step
		log.Warn("dav request refused", "step", step, "host", untrusted.Host)
		return nil, untrusted
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusMultiStatus {
		log.Warn("dav request failed", "step", step, "method", method, "status", resp.StatusCode)
		status := &StatusError{Method: method, URL: target.Redacted(), Status: resp.StatusCode, Step: step}
		if location := resp.Header.Get("Location"); location != "" {
			status.location, _ = resolve(target, location)
		}
		return nil, status
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

func (k Kind) homeOf(p prop) string {
	if k.homeProp == Calendars.homeProp {
		return p.CalHome.Href
	}
	return p.CardHome.Href
}

func (c *Client) discover(ctx context.Context, k Kind, step string, target *url.URL) (principal, home *url.URL, err error) {
	found, err := do(ctx, c.http, step, "PROPFIND", target, "0", `<D:propfind `+k.namespaces+`><D:prop><D:current-user-principal/>`+k.homeProp+`</D:prop></D:propfind>`)
	if err != nil {
		return nil, nil, err
	}
	principalRef, homeRef := "", ""
	for _, r := range found {
		p := r.prop()
		if h := p.Principal.Href; h != "" {
			principalRef = h
		}
		if h := k.homeOf(p); h != "" {
			homeRef = h
		}
	}
	if homeRef != "" {
		home, err = resolve(target, homeRef)
		return nil, home, err
	}
	if principalRef == "" {
		return nil, nil, errors.New("dav: no current-user-principal")
	}
	principal, err = resolve(target, principalRef)
	return principal, nil, err
}

func (c *Client) homeSet(ctx context.Context, k Kind, principal *url.URL) (*url.URL, error) {
	found, err := do(ctx, c.http, StepPrincipal, "PROPFIND", principal, "0", `<D:propfind `+k.namespaces+`><D:prop>`+k.homeProp+`</D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	for _, r := range found {
		if h := k.homeOf(r.prop()); h != "" {
			return resolve(principal, h)
		}
	}
	return nil, errors.New("dav: no home set")
}

func (c *Client) wellKnown(ctx context.Context, k Kind) (*url.URL, error) {
	target := c.base.ResolveReference(&url.URL{Path: k.wellKnown})
	for hop := 0; ; hop++ {
		principal, home, err := c.discover(ctx, k, StepWellKnown, target)
		status, isStatus := errors.AsType[*StatusError](err)
		switch {
		case isStatus && status.Status/100 == 3 && status.location != nil && hop < maxRedirects:
			target = status.location
			continue
		case isStatus && status.Status/100 == 3:
			return nil, fmt.Errorf("dav: the %s step redirected more than %d times", StepWellKnown, maxRedirects)
		case err != nil:
			return nil, err
		case home != nil:
			return home, nil
		}
		return c.homeSet(ctx, k, principal)
	}
}

func (c *Client) home(ctx context.Context, k Kind) (*url.URL, error) {
	principal, home, err := c.discover(ctx, k, StepRoot, c.base)
	if err != nil || home != nil {
		return home, err
	}
	home, err = c.homeSet(ctx, k, principal)
	if status, ok := errors.AsType[*StatusError](err); ok && status.Refused() {
		found, retry := c.wellKnown(ctx, k)
		if retry == nil {
			return found, nil
		}
		if _, untrusted := errors.AsType[*UntrustedError](retry); untrusted {
			return nil, retry
		}
	}
	return home, err
}

func (c *Client) Collections(ctx context.Context, k Kind) ([]CollectionRef, error) {
	home, err := c.home(ctx, k)
	if err != nil {
		return nil, err
	}
	found, err := do(ctx, c.http, StepCollections, "PROPFIND", home, "1", `<D:propfind `+k.namespaces+`><D:prop><D:resourcetype/><D:displayname/>`+k.collect+`</D:prop></D:propfind>`)
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
		objects, err := do(ctx, c.http, StepObjects, "REPORT", ref.URL, "1", k.report)
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
	found, err := do(ctx, c.http, StepObjects, "REPORT", collection, "1", body)
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
	if untrusted, ok := errors.AsType[*UntrustedError](err); ok {
		untrusted.Step = strings.ToLower(method)
		return untrusted
	}
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
