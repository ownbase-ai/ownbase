// Package devbridge implements the core logic behind `ownbasectl dev`: a
// human-run local HTTPS bridge that reaches each of a Base's domain'd
// services directly over an SSH tunnel (bypassing Caddy entirely), serving
// them locally under their real, already-configured domain with
// ".localhost" appended — e.g. a service configured with
// "domain: myapp.example.com" is served at
// "https://myapp.example.com.localhost:8443". Per RFC 6761 every OS and
// browser resolves any hostname ending in ".localhost" straight to the
// loopback interface, no matter how many labels precede it — no DNS lookup,
// no /etc/hosts entry, and no internet connection required.
//
// This package holds the testable, I/O-light core: parsing which services
// are eligible to be bridged, deriving their local hostnames, generating an
// mkcert certificate that covers them, and a Host-header-dispatching
// reverse proxy. The actual SSH tunnels (internal/tunnel, reused completely
// unmodified) and process wiring (flag parsing, signal handling) live in
// cmd/ownbasectl/dev.go.
//
// There is no code-sync mechanism of any kind here: this package only
// tunnels and proxies traffic to whatever is currently deployed. The only
// way to change a service's code remains the standard git-push-to-deploy
// flow (see docs/development.md) — pushing to the service's bare repo and
// updating ref: — exactly as in production.
package devbridge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ownbase/ownbase/internal/schema"
)

// Target describes one service eligible to be bridged: its container port
// and every domain it is configured to serve.
type Target struct {
	// Service is the ownbase.yaml service key (the map key in services:).
	Service string
	// Port is the container's listening port — the same port the compiler
	// would route Caddy to (see internal/compiler.RouteModel). Kept for
	// display/informational use; the tunnel itself connects to HostPort.
	Port int
	// HostPort is the loopback port the compiler publishes this service's
	// container to on the Base, assigned deterministically by
	// schema.OwnbaseConfig.DevBridgePorts() — the same computation the
	// compiler runs when rendering the Quadlet unit, so both sides always
	// agree without coordinating. This is what the SSH tunnel actually
	// connects to; it is deliberately a different number than Port so that
	// a service can declare port: 80/443 (or share a port with another
	// service) without a loopback-publish collision.
	HostPort int
	// Domains is the service's EffectiveDomains(), in declared order.
	// Always non-empty for a discovered Target — see Discover.
	Domains []string
}

// LocalHostnames returns each of t.Domains with ".localhost" appended,
// preserving order, e.g. "myapp.example.com" -> "myapp.example.com.localhost".
func (t Target) LocalHostnames() []string {
	out := make([]string, len(t.Domains))
	for i, d := range t.Domains {
		out[i] = d + ".localhost"
	}
	return out
}

// Discover parses raw ownbase.yaml content and returns every service
// eligible to be bridged: services with both a port and at least one
// EffectiveDomains() entry. A service with no domain configured is skipped
// entirely — not bridged, not tunneled, not returned — because the dev
// bridge mirrors exactly what is already intentionally publicly reachable
// in production; it never invents local-only access to something the
// operator hasn't chosen to expose via a domain. Results are sorted by
// service name for determinism; a config with no domain'd service returns
// an empty (nil) slice and no error — that is an expected state, not a
// failure.
func Discover(raw string) ([]Target, error) {
	cfg, err := schema.ParseConfig(strings.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse ownbase.yaml: %w", err)
	}

	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	hostPorts := cfg.DevBridgePorts()

	var targets []Target
	for _, name := range names {
		svc := cfg.Services[name]
		domains := svc.EffectiveDomains()
		if svc.Port == 0 || len(domains) == 0 {
			continue
		}
		targets = append(targets, Target{
			Service:  name,
			Port:     svc.Port,
			HostPort: hostPorts[name],
			Domains:  domains,
		})
	}
	return targets, nil
}

// AllLocalHostnames returns the deduplicated, sorted union of every
// target's LocalHostnames() — the full SAN list to hand to mkcert in one
// invocation (see GenerateCert).
func AllLocalHostnames(targets []Target) []string {
	seen := make(map[string]bool)
	var all []string
	for _, t := range targets {
		for _, h := range t.LocalHostnames() {
			if !seen[h] {
				seen[h] = true
				all = append(all, h)
			}
		}
	}
	sort.Strings(all)
	return all
}
