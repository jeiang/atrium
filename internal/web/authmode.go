package web

import (
	"fmt"
	"io"
	"net"
	"strings"
)

const authWarning = "warning: no authentication is configured. Authentication is recommended because this instance holds API keys and mailbox access. Configure proxy authentication or bind a loopback address, or pass --force-disable-auth to start without authentication."

const authDisabledLog = "starting without authentication: every client that can reach the listener has full access to API keys and mailbox access"

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func resolveAuth(stderr io.Writer, a serveArgs, hosts []string) (serveArgs, bool) {
	configured := false
	switch a.auth {
	case AuthProxy:
		configured = len(hosts) > 0
	case AuthLocal:
		configured = isLoopbackHost(a.host)
	}
	if configured {
		return a, true
	}
	if !a.forceDisableAuth {
		_, _ = fmt.Fprintln(stderr, authWarning)
		if a.auth == AuthProxy {
			_, _ = fmt.Fprintln(stderr, "proxy mode needs --allowed-host or ATRIUM_PUBLIC_HOST")
		}
		return a, false
	}
	log.Warn(authDisabledLog)
	a.auth = AuthLocal
	a.authDisabled = true
	return a, true
}
