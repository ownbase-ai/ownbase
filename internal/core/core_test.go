package core_test

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/core"
	"github.com/ownbase/ownbase/internal/schema"
)

func TestBuildForgejoModel_DefaultPort(t *testing.T) {
	cfg := schema.CoreConfig{}
	m := core.CoreManifest{ForgejoImage: "codeberg.org/forgejo/forgejo:10"}
	model := core.BuildForgejoModel(cfg, m)

	if model.PublicPort != core.DefaultForgejoPort {
		t.Errorf("PublicPort = %d, want %d", model.PublicPort, core.DefaultForgejoPort)
	}
	if model.Name != core.ForgejoContainerName {
		t.Errorf("Name = %q, want %q", model.Name, core.ForgejoContainerName)
	}
	if !model.IsImageBundled {
		t.Error("IsImageBundled must be true for core Forgejo")
	}
	if model.Image != "codeberg.org/forgejo/forgejo:10" {
		t.Errorf("Image = %q", model.Image)
	}
}

func TestBuildForgejoModel_CustomPort(t *testing.T) {
	cfg := schema.CoreConfig{Forgejo: schema.ForgejoCoreConfig{Port: 4000}}
	model := core.BuildForgejoModel(cfg, core.Current)
	if model.PublicPort != 4000 {
		t.Errorf("PublicPort = %d, want 4000", model.PublicPort)
	}
}

func TestBuildForgejoModel_DomainSetsRootURL(t *testing.T) {
	cfg := schema.CoreConfig{Forgejo: schema.ForgejoCoreConfig{Domain: "git.mysite.com"}}
	model := core.BuildForgejoModel(cfg, core.Current)

	if model.PublicDomain != "git.mysite.com" {
		t.Errorf("PublicDomain = %q", model.PublicDomain)
	}
	var rootURLFound bool
	for _, env := range model.Env {
		if env == "FORGEJO__server__ROOT_URL=https://git.mysite.com" {
			rootURLFound = true
		}
	}
	if !rootURLFound {
		t.Errorf("ROOT_URL not set to https domain: %v", model.Env)
	}
}

func TestBuildForgejoModel_HealthProbe(t *testing.T) {
	model := core.BuildForgejoModel(schema.CoreConfig{}, core.Current)
	if model.HealthProbe == nil || model.HealthProbe.HTTPPath != "/api/healthz" {
		t.Errorf("expected health probe /api/healthz, got %+v", model.HealthProbe)
	}
}

func TestBuildForgejoModel_DigestPinned(t *testing.T) {
	m := core.CoreManifest{
		ForgejoImage:  "codeberg.org/forgejo/forgejo:10",
		ForgejoDigest: "sha256:abc123",
	}
	out := core.BuildCoreOutput(schema.CoreConfig{}, m)
	unit := out.QuadletUnits[core.ForgejoContainerName+".container"]
	if !strings.Contains(unit, "Image=codeberg.org/forgejo/forgejo:10@sha256:abc123") {
		t.Errorf("digest-pinned core unit must contain Image=ref@digest\nunit:\n%s", unit)
	}
}

func TestBuildCoreOutput_ContainsAllUnits(t *testing.T) {
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current)
	required := []string{
		core.ForgejoContainerName + ".container",
		core.CaddyContainerName + ".container",
		core.ForgejoDataVolume + ".volume",
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
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current)

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
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current)
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

// TestBuildForgejoModel_S6OverlayCapabilities documents the capability design
// required by Forgejo's s6-overlay process supervisor:
//
//   - No User= override: s6-svscan must start as root to initialise its lock
//     file and service directory. Forcing User=1000 from outside causes s6 to
//     fail immediately with "unable to open .s6-svscan/lock: Permission denied".
//
//   - SETUID + SETGID added back: after DropCapability=ALL, s6-applyuidgid
//     needs CAP_SETUID/CAP_SETGID to call setresuid/setresgid and drop to the
//     git user internally. NoNewPrivileges=true still prevents exec-based
//     re-escalation, so these caps are not exploitable from inside the container.
func TestBuildForgejoModel_S6OverlayCapabilities(t *testing.T) {
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current)
	unit := out.QuadletUnits[core.ForgejoContainerName+".container"]

	// Must NOT pin a user — s6-overlay manages the UID switch internally.
	if strings.Contains(unit, "User=") {
		t.Errorf("Forgejo unit must not set User= (breaks s6-overlay entrypoint):\n%s", unit)
	}
	// Four capabilities required by the s6-overlay init in the Forgejo image:
	//   SETUID/SETGID  — privilege drop to git user
	//   CHOWN          — chown /data dirs to git
	//   DAC_OVERRIDE   — root needs it to overwrite git-owned app.ini on 2nd
	//                    environment-to-ini call; without it INSTALL_LOCK stays
	//                    false and the CLI admin commands refuse to run
	for _, cap := range []string{
		"AddCapability=SETUID",
		"AddCapability=SETGID",
		"AddCapability=CHOWN",
		"AddCapability=DAC_OVERRIDE",
	} {
		if !strings.Contains(unit, cap) {
			t.Errorf("Forgejo unit missing %q:\n%s", cap, unit)
		}
	}
	// Core hardening must still be present.
	if !strings.Contains(unit, "NoNewPrivileges=true") {
		t.Errorf("Forgejo unit must set NoNewPrivileges=true:\n%s", unit)
	}
	if !strings.Contains(unit, "DropCapability=ALL") {
		t.Errorf("Forgejo unit must set DropCapability=ALL:\n%s", unit)
	}
}

// TestBuildForgejoModel_SSHDaemonDisabled asserts that START_SSH_SERVER=false
// is set (not DISABLE_SSH=true). The distinction matters: DISABLE_SSH also
// kills the `forgejo serv` and `forgejo keys` CLI commands used by the
// host-side AuthorizedKeysCommand shims for git-over-SSH.
func TestBuildForgejoModel_SSHDaemonDisabled(t *testing.T) {
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current)
	unit := out.QuadletUnits[core.ForgejoContainerName+".container"]
	if !strings.Contains(unit, "FORGEJO__server__START_SSH_SERVER=false") {
		t.Errorf("Forgejo unit must set START_SSH_SERVER=false:\n%s", unit)
	}
	// Must explicitly set DISABLE_SSH=false so environment-to-ini overwrites any
	// stale DISABLE_SSH=true previously written to app.ini in the volume.
	if !strings.Contains(unit, "FORGEJO__server__DISABLE_SSH=false") {
		t.Errorf("Forgejo unit must set DISABLE_SSH=false (explicit override for app.ini):\n%s", unit)
	}
	if !strings.Contains(unit, "FORGEJO__server__SSH_PORT=22") {
		t.Errorf("Forgejo unit must set SSH_PORT=22 for correct clone URLs:\n%s", unit)
	}
}

func TestBuildCoreOutput_LongerTimeout(t *testing.T) {
	out := core.BuildCoreOutput(schema.CoreConfig{}, core.Current)
	unit := out.QuadletUnits[core.ForgejoContainerName+".container"]
	if !strings.Contains(unit, "TimeoutStartSec=120") {
		t.Errorf("core unit should have TimeoutStartSec=120\nunit:\n%s", unit)
	}
}

func TestBuildCoreOutput_Deterministic(t *testing.T) {
	cfg := schema.CoreConfig{Forgejo: schema.ForgejoCoreConfig{Domain: "git.example.com"}}
	out1 := core.BuildCoreOutput(cfg, core.Current)
	out2 := core.BuildCoreOutput(cfg, core.Current)
	for name := range out1.QuadletUnits {
		if out1.QuadletUnits[name] != out2.QuadletUnits[name] {
			t.Errorf("core unit %q not deterministic", name)
		}
	}
}

func TestForgejoURL_DefaultPort(t *testing.T) {
	url := core.ForgejoURL(schema.CoreConfig{})
	if url != "http://localhost:3000" {
		t.Errorf("ForgejoURL() = %q, want http://localhost:3000", url)
	}
}

func TestForgejoURL_CustomPort(t *testing.T) {
	cfg := schema.CoreConfig{Forgejo: schema.ForgejoCoreConfig{Port: 4000}}
	url := core.ForgejoURL(cfg)
	if url != "http://localhost:4000" {
		t.Errorf("ForgejoURL() = %q, want http://localhost:4000", url)
	}
}
