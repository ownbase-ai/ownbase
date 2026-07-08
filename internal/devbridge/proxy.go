package devbridge

// proxy.go implements the local HTTPS reverse proxy: one
// httputil.ReverseProxy per bridged local hostname, dispatched by the
// incoming request's Host header. TLS termination happens in the caller
// (cmd/ownbasectl/tunnel.go), which loads the mkcert-generated certificate.

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// NewProxyHandler returns an http.Handler that dispatches each incoming
// request to a backend reverse proxy chosen by the request's Host header
// (any ":port" suffix is stripped before lookup). routes maps a local
// ".localhost" hostname to the "127.0.0.1:<port>" address of the SSH tunnel
// serving that service (see internal/tunnel.Tunnel.LocalAddr) — every
// hostname of a multi-domain service maps to the same tunnel address, since
// a service gets exactly one tunnel no matter how many domains it has.
//
// A request for an unrecognized Host returns 404 with a short explanation
// rather than silently proxying nowhere or falling through to an arbitrary
// backend.
func NewProxyHandler(routes map[string]string) (http.Handler, error) {
	proxies := make(map[string]*httputil.ReverseProxy, len(routes))
	for host, tunnelAddr := range routes {
		target, err := url.Parse("http://" + tunnelAddr)
		if err != nil {
			return nil, fmt.Errorf("parse tunnel address %q for host %q: %w", tunnelAddr, host, err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)

		// Forward the real production domain as the Host header, not the
		// local "<domain>.localhost:<port>" one the browser actually sent.
		// host is always "<domain>.localhost" (see Target.LocalHostnames),
		// so stripping the suffix recovers the exact domain Caddy would
		// send in production. Without this, a service that validates the
		// Host header or builds absolute URLs from it (common for strict
		// virtual-hosting or CSRF origin checks) sees a hostname it never
		// sees in production and can mis-route or reject the request.
		realDomain := strings.TrimSuffix(host, ".localhost")
		baseDirector := proxy.Director
		proxy.Director = func(r *http.Request) {
			baseDirector(r)
			r.Host = realDomain
		}
		proxies[host] = proxy
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := stripPort(r.Host)
		proxy, ok := proxies[host]
		if !ok {
			http.Error(w, fmt.Sprintf("ownbasectl tunnel: no bridged service for host %q", host), http.StatusNotFound)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

// stripPort removes a trailing ":<port>" from a Host header value, if
// present, leaving IPv6 literals (which contain colons within brackets)
// untouched unless they too carry an explicit port suffix.
func stripPort(host string) string {
	if strings.HasPrefix(host, "[") {
		// IPv6 literal: "[::1]" or "[::1]:8443".
		if i := strings.Index(host, "]"); i != -1 {
			if i+1 < len(host) && host[i+1] == ':' {
				return host[:i+1]
			}
			return host
		}
		return host
	}
	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i]
	}
	return host
}
