package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

type Options struct {
	Timeout         time.Duration
	CAFile          string
	Proxy           string
	FollowRedirects bool
}

type Error string

func (e Error) Error() string { return string(e) }

func New(o Options) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, Error(fmt.Sprintf("CA bundle %s cannot be read: %v", o.CAFile, err))
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, Error(fmt.Sprintf("CA bundle %s holds no PEM certificates", o.CAFile))
		}
		tlsConfig.RootCAs = pool
	}
	proxy := http.ProxyFromEnvironment
	if o.Proxy != "" {
		u, err := url.Parse(o.Proxy)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") || u.Host == "" {
			return nil, Error("proxy must be an http, https or socks5 URL")
		}
		proxy = http.ProxyURL(u)
	}
	client := &http.Client{
		Timeout: o.Timeout,
		Transport: &http.Transport{
			Proxy:                 proxy,
			DialContext:           (&net.Dialer{Timeout: o.Timeout}).DialContext,
			TLSClientConfig:       tlsConfig,
			TLSHandshakeTimeout:   o.Timeout,
			ResponseHeaderTimeout: o.Timeout,
		},
	}
	if !o.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return client, nil
}
