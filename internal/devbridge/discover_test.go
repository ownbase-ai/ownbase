package devbridge_test

import (
	"reflect"
	"testing"

	"github.com/ownbase/ownbase/internal/devbridge"
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
	targets, err := devbridge.Discover(sampleYAML)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	got := make(map[string]devbridge.Target, len(targets))
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

	// HostPort is the deterministic dev-bridge loopback port — assigned by
	// sorted service name starting at schema.DevBridgeBasePort — and must
	// differ from Port (the container's own listening port).
	if hello.HostPort != schema.DevBridgeBasePort {
		t.Errorf("hello.HostPort = %d, want %d", hello.HostPort, schema.DevBridgeBasePort)
	}
	if multi.HostPort != schema.DevBridgeBasePort+1 {
		t.Errorf("multi.HostPort = %d, want %d", multi.HostPort, schema.DevBridgeBasePort+1)
	}
	if hello.HostPort == hello.Port {
		t.Errorf("hello.HostPort (%d) must differ from hello.Port (%d)", hello.HostPort, hello.Port)
	}
}

func TestDiscover_SortedByServiceName(t *testing.T) {
	targets, err := devbridge.Discover(sampleYAML)
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
	targets, err := devbridge.Discover(noDomains)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("expected no bridgeable services, got %v", targets)
	}
}

func TestDiscover_InvalidYAMLReturnsError(t *testing.T) {
	if _, err := devbridge.Discover("not: valid: yaml: at: all:"); err == nil {
		t.Error("expected error for invalid ownbase.yaml, got nil")
	}
}

func TestTarget_LocalHostnames(t *testing.T) {
	tg := devbridge.Target{
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
	targets := []devbridge.Target{
		{Service: "b", Domains: []string{"b.example.com"}},
		{Service: "a", Domains: []string{"a.example.com", "shared.example.com"}},
		{Service: "c", Domains: []string{"shared.example.com"}}, // duplicate across services
	}
	got := devbridge.AllLocalHostnames(targets)
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
	if got := devbridge.AllLocalHostnames(nil); len(got) != 0 {
		t.Errorf("expected empty result for no targets, got %v", got)
	}
}
