package webfetch

import (
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/tool"
)

func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", tool.Error("invalid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" {
		return "", tool.Error("only absolute http(s) URLs are allowed")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "0" {
		port = ""
	}
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	u.Scheme = scheme
	u.Host = host
	u.User = nil
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = ""
	return u.String(), nil
}
