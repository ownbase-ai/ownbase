package core_test

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/core"
	"github.com/ownbase/ownbase/internal/schema"
)

func TestBuildCoreOutput_ContainsAllUnits(t *testing.T) {
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current, true)
	required := []string{
		core.CaddyContainerName + ".container",
		core.CaddyDataVolume + ".volume",
		core.OwnbaseInternalNetwork + ".network",
	}
	for _, name := range required {
		if _, ok := out.QuadletUnits[name]; !ok {
			t.Errorf("missing core unit %q", name)
		}
	}
}

// TestBuildCoreOutput_NetworkUnitEnablesCaddy ensures the .network Quadlet unit
// is present so that Caddy's Network= directive resolves to a real systemd
// service (ownbase-internal-network.service). Without this unit Caddy fails to
// start with "Unit ownbase-internal-network.service not found".
func TestBuildCoreOutput_NetworkUnitEnablesCaddy(t *testing.T) {
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current, true)

	netKey := core.OwnbaseInternalNetwork + ".network"
	unit, ok := out.QuadletUnits[netKey]
	if !ok {
		t.Fatalf("core output missing %q — Caddy will fail to start without it", netKey)
	}
	if !strings.Contains(unit, "[Network]") {
		t.Errorf("network unit %q does not contain [Network] section:\n%s", netKey, unit)
	}

	// Caddy container must reference the network by the canonical name.
	caddyUnit := out.QuadletUnits[core.CaddyContainerName+".container"]
	if !strings.Contains(caddyUnit, "Network="+core.OwnbaseInternalNetwork+".network") {
		t.Errorf("Caddy unit does not reference %s.network:\n%s", core.OwnbaseInternalNetwork, caddyUnit)
	}
}

// caddyUnit is a helper that renders the core output and returns the Caddy
// container unit content.
func caddyUnit(t *testing.T) string {
	t.Helper()
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current, true)
	unit, ok := out.QuadletUnits[core.CaddyContainerName+".container"]
	if !ok {
		t.Fatalf("core output missing Caddy container unit")
	}
	return unit
}

// TestBuildCaddyModel_PublishesWebPorts asserts that Caddy publishes 80 and
// 443 on the host. Without these, the reverse proxy is unreachable from the
// internet and the UFW forwarding rules have nothing to forward to.
func TestBuildCaddyModel_PublishesWebPorts(t *testing.T) {
	unit := caddyUnit(t)
	for _, want := range []string{"PublishPort=80:80", "PublishPort=443:443"} {
		if !strings.Contains(unit, want) {
			t.Errorf("Caddy unit missing %q — proxy cannot serve public web traffic:\n%s", want, unit)
		}
	}
}

// TestBuildCaddyModel_HasNetBindCapability asserts that Caddy gets
// NET_BIND_SERVICE back after DropCapability=ALL, otherwise it cannot bind
// privileged ports 80/443 and will crash-loop on start.
func TestBuildCaddyModel_HasNetBindCapability(t *testing.T) {
	unit := caddyUnit(t)
	if !strings.Contains(unit, "DropCapability=ALL") {
		t.Errorf("Caddy unit must drop all capabilities by default:\n%s", unit)
	}
	if !strings.Contains(unit, "AddCapability=NET_BIND_SERVICE") {
		t.Errorf("Caddy unit must add NET_BIND_SERVICE to bind 80/443:\n%s", unit)
	}
}

// TestBuildCaddyModel_RunsAsImageDefaultUser asserts that Caddy is NOT forced
// to a non-root UID. caddy:2-alpine runs as root, which (with only
// NET_BIND_SERVICE) is required to bind 80/443 and write to the cert store.
func TestBuildCaddyModel_RunsAsImageDefaultUser(t *testing.T) {
	unit := caddyUnit(t)
	if strings.Contains(unit, "User=") {
		t.Errorf("Caddy unit must not pin User= (breaks privileged-port bind + cert writes):\n%s", unit)
	}
}

// TestBuildCaddyModel_StaysHardened guards the rest of the hardening posture
// so a future change can't quietly drop it while fixing ports.
func TestBuildCaddyModel_StaysHardened(t *testing.T) {
	unit := caddyUnit(t)
	if !strings.Contains(unit, "NoNewPrivileges=true") {
		t.Errorf("Caddy unit must set NoNewPrivileges=true:\n%s", unit)
	}
}

func TestBuildCoreOutput_LongerTimeout(t *testing.T) {
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current, true)
	unit := out.QuadletUnits[core.CaddyContainerName+".container"]
	if !strings.Contains(unit, "TimeoutStartSec=120") {
		t.Errorf("core unit should have TimeoutStartSec=120\nunit:\n%s", unit)
	}
}

func TestBuildCoreOutput_Deterministic(t *testing.T) {
	cfg := schema.CoreConfig{Caddy: schema.CaddyCoreConfig{Email: "admin@example.com"}}
	out1 := core.BuildCoreOutput(cfg, core.Current, true)
	out2 := core.BuildCoreOutput(cfg, core.Current, true)
	for name := range out1.QuadletUnits {
		if out1.QuadletUnits[name] != out2.QuadletUnits[name] {
			t.Errorf("core unit %q not deterministic", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Domain-gated port publishing (sudo-free create / dev bridge)
// ---------------------------------------------------------------------------

// TestBuildCaddyModel_NoDomainPublishesNoPorts asserts that a Base with no
// domain'd service (the default state of a fresh Base) does not publish 80
// or 443 at all — there is nothing for Caddy to route yet, and
// `ownbasectl dev` reaches services directly over SSH instead.
func TestBuildCaddyModel_NoDomainPublishesNoPorts(t *testing.T) {
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current, false)
	unit := out.QuadletUnits[core.CaddyContainerName+".container"]
	for _, unwanted := range []string{"PublishPort=80:80", "PublishPort=443:443"} {
		if strings.Contains(unit, unwanted) {
			t.Errorf("Caddy unit must not contain %q when no service has a domain:\n%s", unwanted, unit)
		}
	}
}
