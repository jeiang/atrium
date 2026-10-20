package py

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

func valueError(msg string) error { return &Exception{"ValueError", msg} }

type URLParts struct {
	Scheme, Netloc, Path, Query, Fragment       string
	HasScheme, HasNetloc, HasQuery, HasFragment bool
}

const (
	whatwgControlOrSpace = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\x0b\x0c\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f "
	schemeChars          = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+-."
)

var (
	usesRelative = []string{"", "ftp", "http", "gopher", "nntp", "imap", "wais", "file", "https", "shttp", "mms", "prospero",
		"rtsp", "rtsps", "rtspu", "sftp", "svn", "svn+ssh", "ws", "wss"}
	usesNetloc = []string{"", "ftp", "http", "gopher", "nntp", "telnet", "imap", "wais", "file", "mms", "https", "shttp",
		"snews", "prospero", "rtsp", "rtsps", "rtspu", "rsync", "svn", "svn+ssh", "sftp", "nfs", "git", "git+ssh", "ws", "wss",
		"itms-services"}
)

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isASCIIAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func splitNetloc(url string, start int) (netloc, rest string) {
	delim := len(url)
	for _, c := range "/?#" {
		if i := strings.IndexRune(url[start:], c); i >= 0 && start+i < delim {
			delim = start + i
		}
	}
	return url[start:delim], url[delim:]
}

func checkNetloc(netloc string) error {
	if netloc == "" || isASCII(netloc) {
		return nil
	}
	n := strings.NewReplacer("@", "", ":", "", "#", "", "?", "").Replace(netloc)
	folded := norm.NFKC.String(n)
	if n == folded {
		return nil
	}
	if strings.ContainsAny(folded, "/?#@:") {
		return valueError("netloc '" + netloc + "' contains invalid characters under NFKC normalization")
	}
	return nil
}

func checkBracketedNetloc(netloc string) error {
	hostAndPort := netloc[strings.LastIndexByte(netloc, '@')+1:]
	var host string
	if before, bracketed, ok := strings.Cut(hostAndPort, "["); ok {
		if before != "" {
			return valueError("Invalid IPv6 URL")
		}
		var port string
		host, port, _ = strings.Cut(bracketed, "]")
		if port != "" && !strings.HasPrefix(port, ":") {
			return valueError("Invalid IPv6 URL")
		}
	} else {
		host, _, _ = strings.Cut(hostAndPort, ":")
	}
	return checkBracketedHost(host)
}

func checkBracketedHost(host string) error {
	if strings.HasPrefix(host, "v") || strings.HasPrefix(host, "V") {
		if !ipvFuture(host) {
			return valueError("IPvFuture address is invalid")
		}
		return nil
	}
	addr, zone, hasZone := strings.Cut(host, "%")
	if hasZone && (zone == "" || strings.Contains(zone, "%")) {
		return valueError(Repr(host) + " does not appear to be an IPv4 or IPv6 address")
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil || hasZone && ip.Is4() {
		return valueError(Repr(host) + " does not appear to be an IPv4 or IPv6 address")
	}
	if ip.Is4() {
		return valueError("An IPv4 address cannot be in brackets")
	}
	return nil
}

func ipvFuture(host string) bool {
	i := 1
	for i < len(host) && isHex(host[i]) {
		i++
	}
	return i > 1 && i < len(host) && host[i] == '.' && i+1 < len(host) && !strings.Contains(host[i+1:], "\n")
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func URLSplit(url string) (URLParts, error) {
	var p URLParts
	url = strings.TrimLeft(url, whatwgControlOrSpace)
	url = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(url)
	if i := strings.IndexByte(url, ':'); i > 0 && isASCIIAlpha(url[0]) {
		valid := true
		for j := range i {
			if strings.IndexByte(schemeChars, url[j]) < 0 {
				valid = false
				break
			}
		}
		if valid {
			p.Scheme, p.HasScheme = strings.ToLower(url[:i]), true
			url = url[i+1:]
		}
	}
	if strings.HasPrefix(url, "//") {
		p.Netloc, url = splitNetloc(url, 2)
		p.HasNetloc = true
		open, closed := strings.Contains(p.Netloc, "["), strings.Contains(p.Netloc, "]")
		if open != closed {
			return URLParts{}, valueError("Invalid IPv6 URL")
		}
		if open && closed {
			if err := checkBracketedNetloc(p.Netloc); err != nil {
				return URLParts{}, err
			}
		}
	}
	if i := strings.IndexByte(url, '#'); i >= 0 {
		url, p.Fragment, p.HasFragment = url[:i], url[i+1:], true
	}
	if i := strings.IndexByte(url, '?'); i >= 0 {
		url, p.Query, p.HasQuery = url[:i], url[i+1:], true
	}
	p.Path = url
	if err := checkNetloc(p.Netloc); err != nil {
		return URLParts{}, err
	}
	return p, nil
}

func (p URLParts) hostinfo() (host, port string, hasPort bool) {
	info := p.Netloc[strings.LastIndexByte(p.Netloc, '@')+1:]
	if _, bracketed, ok := strings.Cut(info, "["); ok {
		host, rest, _ := strings.Cut(bracketed, "]")
		_, port, _ = strings.Cut(rest, ":")
		return host, port, port != ""
	}
	host, port, _ = strings.Cut(info, ":")
	return host, port, port != ""
}

func (p URLParts) Hostname() (string, bool) {
	host, _, _ := p.hostinfo()
	if host == "" {
		return "", false
	}
	name, zone, hasZone := strings.Cut(host, "%")
	name = Lower(name)
	if hasZone {
		return name + "%" + zone, true
	}
	return name, true
}

func (p URLParts) Port() (port int, ok bool, err error) {
	_, text, has := p.hostinfo()
	if !has {
		return 0, false, nil
	}
	for i := range len(text) {
		if text[i] < '0' || text[i] > '9' {
			return 0, false, valueError("Port could not be cast to integer value as " + Repr(text))
		}
	}
	n, convErr := strconv.Atoi(text)
	if convErr != nil || n > 65535 {
		return 0, false, valueError("Port out of range 0-65535")
	}
	return n, true, nil
}

func URLUnsplit(scheme, netloc, path, query, fragment string) string {
	hasNetloc := netloc != ""
	if !hasNetloc && scheme != "" && slices.Contains(usesNetloc, scheme) && (path == "" || path[0] == '/') {
		hasNetloc = true
	}
	return unsplit(scheme, netloc, hasNetloc, path, query, query != "", fragment, fragment != "")
}

func unsplit(scheme, netloc string, hasNetloc bool, path, query string, hasQuery bool, fragment string, hasFragment bool) string {
	if hasNetloc {
		if path != "" && path[0] != '/' {
			path = "/" + path
		}
		path = "//" + netloc + path
	} else if strings.HasPrefix(path, "//") {
		path = "//" + path
	}
	if scheme != "" {
		path = scheme + ":" + path
	}
	if hasQuery {
		path += "?" + query
	}
	if hasFragment {
		path += "#" + fragment
	}
	return path
}

func URLJoin(base, url string) (string, error) {
	if base == "" {
		return url, nil
	}
	if url == "" {
		return base, nil
	}
	b, err := URLSplit(base)
	if err != nil {
		return "", err
	}
	u, err := URLSplit(url)
	if err != nil {
		return "", err
	}
	scheme, hasScheme := u.Scheme, u.HasScheme
	if !hasScheme {
		scheme = b.Scheme
	}
	if scheme != b.Scheme || scheme != "" && !slices.Contains(usesRelative, scheme) {
		return url, nil
	}
	netloc, hasNetloc := u.Netloc, u.HasNetloc
	if scheme == "" || slices.Contains(usesNetloc, scheme) {
		if netloc != "" {
			return unsplit(scheme, netloc, hasNetloc, u.Path, u.Query, u.HasQuery, u.Fragment, u.HasFragment), nil
		}
		netloc, hasNetloc = b.Netloc, b.HasNetloc
	}
	path, query, hasQuery, fragment, hasFragment := u.Path, u.Query, u.HasQuery, u.Fragment, u.HasFragment
	if path == "" {
		path = b.Path
		if !hasQuery {
			query, hasQuery = b.Query, b.HasQuery
			if !hasFragment {
				fragment, hasFragment = b.Fragment, b.HasFragment
			}
		}
		return unsplit(scheme, netloc, hasNetloc, path, query, hasQuery, fragment, hasFragment), nil
	}
	baseParts := strings.Split(b.Path, "/")
	if baseParts[len(baseParts)-1] != "" {
		baseParts = baseParts[:len(baseParts)-1]
	}
	var segments []string
	if path[0] == '/' {
		segments = strings.Split(path, "/")
	} else {
		segments = append(baseParts, strings.Split(path, "/")...)
		if len(segments) > 2 {
			kept := segments[:1]
			for _, s := range segments[1 : len(segments)-1] {
				if s != "" {
					kept = append(kept, s)
				}
			}
			segments = append(kept, segments[len(segments)-1])
		}
	}
	var resolved []string
	for _, seg := range segments {
		switch seg {
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
		case ".":
		default:
			resolved = append(resolved, seg)
		}
	}
	if last := segments[len(segments)-1]; last == "." || last == ".." {
		resolved = append(resolved, "")
	}
	joined := strings.Join(resolved, "/")
	if joined == "" {
		joined = "/"
	}
	return unsplit(scheme, netloc, hasNetloc, joined, query, hasQuery, fragment, hasFragment), nil
}
