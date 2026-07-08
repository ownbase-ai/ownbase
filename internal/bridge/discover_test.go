package bridge_test

import (
	"reflect"
	"testing"

	"github.com/ownbase/ownbase/internal/bridge"
	"github.com/ownbase/ownbase/internal/schema"
)

const sampleYAML = `schema_version: v1
services:
  auth:
    source: services/auth
    port: 8080

  hello:
    source: apps/hello
    domain: hello.example.com
    port: 3000

  multi:
    source: apps/multi
    domains:
      - multi.example.com
      - multi.example.org
    port: 4000

  noport:
    source: apps/noport
    domain: noport.example.com
`

func TestDiscover_SkipsServicesWithNoDomainOrNoPort(t *testing.T) {
	targets, err := bridge.Discover(sampleYAML)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	got := make(map[string]bridge.Target, len(targets))
	for _, tg := range targets {
		got[tg.Service] = tg
	}

	if _, ok := got["auth"]; ok {
		t.Error("auth has no domain — must not be bridged")
	}
	if _, ok := got["noport"]; ok {
		t.Error("noport has no port — must not be bridged")
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 bridgeable services, got %d: %v", len(got), got)
	}

	hello, ok := got["hello"]
	if !ok {
		t.Fatal("expected hello to be bridged")
	}
	if hello.Port != 3000 {
		t.Errorf("hello.Port = %d, want 3000", hello.Port)
	}
	if !reflect.DeepEqual(hello.Domains, []string{"hello.example.com"}) {
		t.Errorf("hello.Domains = %v, want [hello.example.com]", hello.Domains)
	}

	multi, ok := got["multi"]
	if !ok {
		t.Fatal("expected multi to be bridged")
	}
	if !reflect.DeepEqual(multi.Domains, []string{"multi.example.com", "multi.example.org"}) {
		t.Errorf("multi.Domains = %v, want [multi.example.com multi.example.org]", multi.Domains)
	}

	// HostPort is the deterministic loopback port — assigned by sorted
	// service name (across ALL port'd services, including domain-less ones
	// like "auth", since the health_probe consumer needs an entry for
	// those too — see schema.OwnbaseConfig.DevBridgePorts) starting at
	// schema.TunnelBasePort — and must differ from Port (the
	// container's own listening port). Sorted order here is
	// auth < hello < multi, so "auth" (filtered out of targets above, but
	// still present in the underlying allocation) claims the base port
	// first, pushing hello/multi up by one each.
	if hello.HostPort != schema.TunnelBasePort+1 {
		t.Errorf("hello.HostPort = %d, want %d", hello.HostPort, schema.TunnelBasePort+1)
	}
	if multi.HostPort != schema.TunnelBasePort+2 {
		t.Errorf("multi.HostPort = %d, want %d", multi.HostPort, schema.TunnelBasePort+2)
	}
	if hello.HostPort == hello.Port {
		t.Errorf("hello.HostPort (%d) must differ from hello.Port (%d)", hello.HostPort, hello.Port)
	}
}

func TestDiscover_SortedByServiceName(t *testing.T) {
	targets, err := bridge.Discover(sampleYAML)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var names []string
	for _, tg := range targets {
		names = append(names, tg.Service)
	}
	if !reflect.DeepEqual(names, []string{"hello", "multi"}) {
		t.Errorf("got service order %v, want [hello multi]", names)
	}
}

func TestDiscover_NoDomainAnywhereReturnsEmptyNoError(t *testing.T) {
	const noDomains = `schema_version: v1
services:
  a:
    source: apps/a
    port: 8080
`
	targets, err := bridge.Discover(noDomains)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("expected no bridgeable services, got %v", targets)
	}
}

func TestDiscover_InvalidYAMLReturnsError(t *testing.T) {
	if _, err := bridge.Discover("not: valid: yaml: at: all:"); err == nil {
		t.Error("expected error for invalid ownbase.yaml, got nil")
	}
}

// TestDiscover_InternalServiceIsIncluded verifies that a service with
// internal: true is included in tunnel targets even though it has no Caddy
// route. The tunnel is the only access path for internal services.
func TestDiscover_InternalServiceIsIncluded(t *testing.T) {
	const yaml = `schema_version: v1
services:
  admin:
    source: services/admin
    domain: admin.example.com
    port: 3000
    internal: true
  web:
    source: services/web
    domain: web.example.com
    port: 8080
`
	targets, err := bridge.Discover(yaml)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := make(map[string]bridge.Target, len(targets))
	for _, tg := range targets {
		got[tg.Service] = tg
	}
	if _, ok := got["admin"]; !ok {
		t.Error("internal: true service with domain and port must be included in tunnel targets")
	}
	if _, ok := got["web"]; !ok {
		t.Error("public service must also be included in tunnel targets")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 targets, got %d: %v", len(got), got)
	}
	admin := got["admin"]
	if !reflect.DeepEqual(admin.Domains, []string{"admin.example.com"}) {
		t.Errorf("admin.Domains = %v, want [admin.example.com]", admin.Domains)
	}
}

func TestTarget_LocalHostnames(t *testing.T) {
	tg := bridge.Target{
		Service: "app",
		Port:    3000,
		Domains: []string{"app.example.com", "app.example.org"},
	}
	got := tg.LocalHostnames()
	want := []string{"app.example.com.localhost", "app.example.org.localhost"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LocalHostnames() = %v, want %v", got, want)
	}
}

func TestAllLocalHostnames_DedupedAndSorted(t *testing.T) {
	targets := []bridge.Target{
		{Service: "b", Domains: []string{"b.example.com"}},
		{Service: "a", Domains: []string{"a.example.com", "shared.example.com"}},
		{Service: "c", Domains: []string{"shared.example.com"}}, // duplicate across services
	}
	got := bridge.AllLocalHostnames(targets)
	want := []string{
		"a.example.com.localhost",
		"b.example.com.localhost",
		"shared.example.com.localhost",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllLocalHostnames() = %v, want %v", got, want)
	}
}

func TestAllLocalHostnames_Empty(t *testing.T) {
	if got := bridge.AllLocalHostnames(nil); len(got) != 0 {
		t.Errorf("expected empty result for no targets, got %v", got)
	}
}
