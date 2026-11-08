package urlx

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const MaxReturnLen = 2048

func SafeReturn(target string) string {
	if target == "" || len(target) > MaxReturnLen || target[0] != '/' || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	path := u.Path
	if path == "" || path[0] != '/' || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n\t") || strings.HasPrefix(path, "/auth/") {
		return "/"
	}
	return target
}

type URLParts struct {
	Scheme, Netloc, Path, Query, Fragment       string
	HasScheme, HasNetloc, HasQuery, HasFragment bool
}

const (
	whatwgControlOrSpace = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\x0b\x0c\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f "
	schemeChars          = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+-."
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
		return fmt.Errorf("netloc %s contains invalid characters under NFKC normalization", strconv.Quote(netloc))
	}
	return nil
}

func checkBracketedNetloc(netloc string) error {
	hostAndPort := netloc[strings.LastIndexByte(netloc, '@')+1:]
	var host string
	if before, bracketed, ok := strings.Cut(hostAndPort, "["); ok {
		if before != "" {
			return fmt.Errorf("invalid IPv6 URL")
		}
		var port string
		host, port, _ = strings.Cut(bracketed, "]")
		if port != "" && !strings.HasPrefix(port, ":") {
			return fmt.Errorf("invalid IPv6 URL")
		}
	} else {
		host, _, _ = strings.Cut(hostAndPort, ":")
	}
	return checkBracketedHost(host)
}

func checkBracketedHost(host string) error {
	if strings.HasPrefix(host, "v") || strings.HasPrefix(host, "V") {
		if !ipvFuture(host) {
			return fmt.Errorf("IPvFuture address is invalid")
		}
		return nil
	}
	addr, zone, hasZone := strings.Cut(host, "%")
	if hasZone && (zone == "" || strings.Contains(zone, "%")) {
		return fmt.Errorf("%s does not appear to be an IPv4 or IPv6 address", strconv.Quote(host))
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil || hasZone && ip.Is4() {
		return fmt.Errorf("%s does not appear to be an IPv4 or IPv6 address", strconv.Quote(host))
	}
	if ip.Is4() {
		return fmt.Errorf("an IPv4 address cannot be in brackets")
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
			return URLParts{}, fmt.Errorf("invalid IPv6 URL")
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
	name = strings.ToLower(name)
	if hasZone {
		return name + "%" + zone, true
	}
	return name, true
}
