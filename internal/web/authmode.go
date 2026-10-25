package web

import (
	"fmt"
	"io"
	"net"
	"strings"
)

const authWarning = "warning: no authentication is configured. Authentication is recommended because this instance holds API keys and mailbox access. Configure proxy or OIDC authentication or bind a loopback address, or pass --force-disable-auth to start without authentication."

const authDisabledLog = "starting without authentication: every client that can reach the listener has full access to API keys and mailbox access"

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func resolveAuth(stderr io.Writer, a serveArgs, hosts []string) (serveArgs, bool) {
	set, err := ParseAuth(a.auth)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return a, false
	}
	configured := set.Local && isLoopbackHost(a.host) || !set.Local && len(hosts) > 0
	if configured {
		return a, true
	}
	if !a.forceDisableAuth {
		_, _ = fmt.Fprintln(stderr, authWarning)
		if !set.Local {
			name := AuthProxy
			if !set.Proxy {
				name = AuthOIDC
			}
			_, _ = fmt.Fprintf(stderr, "%s mode needs --allowed-host or ATRIUM_PUBLIC_HOST\n", name)
		}
		return a, false
	}
	if len(hosts) == 0 && !isLoopbackHost(a.host) {
		_, _ = fmt.Fprintln(stderr, "error: --force-disable-auth on a non-loopback address needs --allowed-host or ATRIUM_PUBLIC_HOST, otherwise every non-loopback Host is refused")
		return a, false
	}
	log.Warn(authDisabledLog)
	a.auth = []string{AuthLocal}
	a.authDisabled = true
	return a, true
}
