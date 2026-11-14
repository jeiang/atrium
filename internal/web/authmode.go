package web

import (
	"fmt"
	"io"
	"net/netip"
	"strings"
)

const authWarning = "warning: no authentication is configured. Authentication is recommended because this instance holds API keys and mailbox access. Configure proxy or OIDC authentication or bind a loopback address."

func isLoopbackBind(host string) bool {
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]"))
	return err == nil && addr.Unmap().IsLoopback()
}

func resolveAuth(stderr io.Writer, host string, modes, hosts []string) (Policy, bool) {
	policy, err := parseAuth(modes)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return Policy{}, false
	}
	if policy.Local() {
		if !isLoopbackBind(host) {
			_, _ = fmt.Fprintf(stderr, "error: local auth mode needs a numeric loopback --host such as 127.0.0.1 or ::1; %q is not a loopback address\n", host)
			return Policy{}, false
		}
		return policy, true
	}
	if len(hosts) > 0 {
		return policy, true
	}
	_, _ = fmt.Fprintln(stderr, authWarning)
	name := AuthProxy
	if !policy.Proxy() {
		name = AuthOIDC
	}
	_, _ = fmt.Fprintf(stderr, "%s mode needs --allowed-host or ATRIUM_PUBLIC_HOST\n", name)
	return Policy{}, false
}
