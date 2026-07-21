package compiler_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/schema"
)

func testInputMinimal(t *testing.T) compiler.Input {
	t.Helper()
	cfg, err := schema.ParseConfigFile("../../testdata/minimal/ownbase.yaml")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return compiler.Input{Config: cfg}
}

func testInputFull(t *testing.T) compiler.Input {
	t.Helper()
	cfg, err := schema.ParseConfigFile("../../testdata/valid/full-config.yaml")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return compiler.Input{Config: cfg}
}

// ---------------------------------------------------------------------------
// Byte-identical determinism
// ---------------------------------------------------------------------------

func TestCompile_ByteIdenticalOnSameInput(t *testing.T) {
	in := testInputMinimal(t)
	out1 := compiler.Compile(in)
	out2 := compiler.Compile(in)
	for filename := range out1.QuadletUnits {
		if out1.QuadletUnits[filename] != out2.QuadletUnits[filename] {
			t.Errorf("unit %q differs between two compiles", filename)
		}
	}
	if out1.Caddyfile != out2.Caddyfile {
		t.Error("Caddyfile differs between two compiles")
	}
}

func TestCompile_ByteIdenticalFullInput(t *testing.T) {
	in := testInputFull(t)
	out1 := compiler.Compile(in)
	out2 := compiler.Compile(in)
	for filename := range out1.QuadletUnits {
		if out1.QuadletUnits[filename] != out2.QuadletUnits[filename] {
			t.Errorf("unit %q differs between two compiles (full input)", filename)
		}
	}
}

// ---------------------------------------------------------------------------
// Golden tests
// ---------------------------------------------------------------------------

func TestCompile_Golden(t *testing.T) {
	in := testInputMinimal(t)
	out := compiler.Compile(in)

	dir := t.TempDir()
	written, err := compiler.WriteOutput(out, dir)
	if err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("WriteOutput returned no files")
	}

	goldenDir := "../../testdata/golden/minimal"
	if _, err := os.Stat(goldenDir); os.IsNotExist(err) {
		t.Logf("golden dir %s does not exist; seeding it now", goldenDir)
		if err := os.MkdirAll(filepath.Join(goldenDir, "runtime"), 0o755); err != nil {
			t.Fatalf("mkdir golden: %v", err)
		}
		runtimeDir := filepath.Join(dir, "runtime")
		entries, _ := os.ReadDir(runtimeDir)
		for _, e := range entries {
			data, _ := os.ReadFile(filepath.Join(runtimeDir, e.Name()))
			_ = os.WriteFile(filepath.Join(goldenDir, "runtime", e.Name()), data, 0o644)
		}
		return
	}

	runtimeDir := filepath.Join(dir, "runtime")
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		t.Fatalf("read runtime dir: %v", err)
	}
	for _, e := range entries {
		actual, _ := os.ReadFile(filepath.Join(runtimeDir, e.Name()))
		goldenPath := filepath.Join(goldenDir, "runtime", e.Name())
		golden, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Errorf("golden file %s not found: %v", goldenPath, err)
			continue
		}
		if string(actual) != string(golden) {
			t.Errorf("output %s differs from golden:\nactual:\n%s\ngolden:\n%s",
				e.Name(), actual, golden)
		}
	}
}

// ---------------------------------------------------------------------------
// Generated-header enforcement
// ---------------------------------------------------------------------------

func TestCompile_GeneratedHeaderInAllFiles(t *testing.T) {
	out := compiler.Compile(testInputFull(t))
	for name, content := range out.QuadletUnits {
		if !strings.Contains(content, "do not hand-edit") {
			t.Errorf("unit %q missing generated header", name)
		}
	}
	if !strings.Contains(out.Caddyfile, "do not hand-edit") {
		t.Error("Caddyfile missing generated header")
	}
}

// ---------------------------------------------------------------------------
// Build provenance
// ---------------------------------------------------------------------------

// TestCompile_BuildDockerfileAnnotation verifies that a service declaring
// dockerfile: gets a # BuildDockerfile= annotation, and that a service
// without dockerfile: does not (the agent uses the default "Dockerfile").
func TestCompile_BuildDockerfileAnnotation(t *testing.T) {
	out := compiler.Compile(testInputSecrets(t))

	// api declares dockerfile: Dockerfile.prod
	apiUnit, ok := out.QuadletUnits["ownbase-api.container"]
	if !ok {
		t.Fatal("ownbase-api.container not in output")
	}
	if !strings.Contains(apiUnit, "# BuildDockerfile=Dockerfile.prod") {
		t.Errorf("ownbase-api.container: missing BuildDockerfile annotation\nunit:\n%s", apiUnit)
	}

	// api-worker declares dockerfile: Dockerfile.worker
	workerUnit, ok := out.QuadletUnits["ownbase-api-worker.container"]
	if !ok {
		t.Fatal("ownbase-api-worker.container not in output")
	}
	if !strings.Contains(workerUnit, "# BuildDockerfile=Dockerfile.worker") {
		t.Errorf("ownbase-api-worker.container: missing BuildDockerfile annotation\nunit:\n%s", workerUnit)
	}

	// auth has no dockerfile: — must not emit a BuildDockerfile line.
	authUnit, ok := out.QuadletUnits["ownbase-auth.container"]
	if !ok {
		t.Fatal("ownbase-auth.container not in output")
	}
	if strings.Contains(authUnit, "# BuildDockerfile=") {
		t.Errorf("ownbase-auth.container should have no BuildDockerfile annotation\nunit:\n%s", authUnit)
	}
}

func TestCompile_BuildSourceInAllContainerUnits(t *testing.T) {
	out := compiler.Compile(testInputFull(t))
	for name, content := range out.QuadletUnits {
		if !strings.HasSuffix(name, ".container") {
			continue
		}
		if !strings.Contains(content, "# BuildSource=") {
			t.Errorf("container unit %q missing BuildSource comment", name)
		}
	}
}

func TestCompile_LocalhostImageInAllContainerUnits(t *testing.T) {
	out := compiler.Compile(testInputFull(t))
	for name, content := range out.QuadletUnits {
		if !strings.HasSuffix(name, ".container") {
			continue
		}
		if !strings.Contains(content, "Image=localhost/") {
			t.Errorf("container unit %q does not use a localhost image reference", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Isolation invariants (Architecture Principle 14)
// ---------------------------------------------------------------------------

func TestCompile_NoSharedVolumes(t *testing.T) {
	model := compiler.CompileToModel(testInputFull(t))
	seen := map[string]bool{}
	for _, vol := range model.Volumes {
		if seen[vol.Name] {
			t.Errorf("volume %q appears more than once", vol.Name)
		}
		seen[vol.Name] = true
	}
}

func TestCompile_ContainersJoinOnlyDeclaredNetworks(t *testing.T) {
	model := compiler.CompileToModel(testInputFull(t))
	declaredNets := map[string]bool{"ownbase-internal": true}
	for _, net := range model.Networks {
		declaredNets[net.Name] = true
	}
	for _, c := range model.Containers {
		for _, net := range c.Networks {
			if !declaredNets[net] {
				t.Errorf("container %q joins undeclared network %q", c.Name, net)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Caddy routes
// ---------------------------------------------------------------------------

func TestCompile_CaddyRouteOnlyForDomainAndPort(t *testing.T) {
	model := compiler.CompileToModel(testInputFull(t))
	for _, r := range model.Routes {
		if r.Host == "" || r.Upstream == "" {
			t.Errorf("route has empty host or upstream: %+v", r)
		}
	}
	// postgres has no domain — should have no route.
	for _, r := range model.Routes {
		if strings.Contains(r.Upstream, "postgres") {
			t.Errorf("postgres should have no Caddy route, but found: %+v", r)
		}
	}
}

// ---------------------------------------------------------------------------
// Multi-domain support
// ---------------------------------------------------------------------------

// TestCompile_MultiDomain_OneRoutePerDomain verifies that a service declaring
// both domain: (deprecated single-value form) and domains: gets one Caddy
// route per effective (deduplicated) domain, all pointing at the same
// container:port upstream.
func TestCompile_MultiDomain_OneRoutePerDomain(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"app": {
					Repo:    "apps/app",
					Port:    3000,
					Domain:  "app.example.com",
					Domains: []string{"app.example.org", "app.example.com"}, // duplicate must be deduped
				},
			},
		},
	}
	model := compiler.CompileToModel(in)

	wantHosts := map[string]bool{"app.example.com": false, "app.example.org": false}
	if len(model.Routes) != len(wantHosts) {
		t.Fatalf("expected %d routes, got %d: %+v", len(wantHosts), len(model.Routes), model.Routes)
	}
	for _, r := range model.Routes {
		if _, ok := wantHosts[r.Host]; !ok {
			t.Errorf("unexpected route host %q", r.Host)
			continue
		}
		wantHosts[r.Host] = true
		if r.Upstream != "ownbase-app:3000" {
			t.Errorf("route %q: upstream = %q, want ownbase-app:3000", r.Host, r.Upstream)
		}
	}
	for host, seen := range wantHosts {
		if !seen {
			t.Errorf("expected a route for domain %q", host)
		}
	}
}

// TestCompile_RouteUpstreamUsesContainerName verifies that Caddy routes
// address the backend by Podman container name rather than "localhost" —
// Caddy runs isolated on the ownbase-internal network and cannot reach
// host-loopback ports.
func TestCompile_RouteUpstreamUsesContainerName(t *testing.T) {
	model := compiler.CompileToModel(testInputMinimal(t))
	for _, r := range model.Routes {
		if strings.HasPrefix(r.Upstream, "localhost:") {
			t.Errorf("route %q: upstream %q must not use localhost — Caddy cannot reach host loopback", r.Host, r.Upstream)
		}
	}
}

// ---------------------------------------------------------------------------
// Dev-bridge loopback port allocation (decoupled from container port)
// ---------------------------------------------------------------------------

// TestCompile_TunnelPort_DistinctFromContainerPort verifies that a
// domain'd + port'd service gets a PublishPort= line with distinct host and
// container ports — the host side is the deterministic dev-bridge
// allocation (schema.TunnelBasePort-based), the container side remains
// the service's own port:.
func TestCompile_TunnelPort_DistinctFromContainerPort(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"hello": {
					Repo:   "apps/hello",
					Domain: "hello.example.com",
					Port:   80,
				},
			},
		},
	}
	out := compiler.Compile(in)
	unit, ok := out.QuadletUnits["ownbase-hello.container"]
	if !ok {
		t.Fatal("ownbase-hello.container not found in output")
	}
	want := fmt.Sprintf("PublishPort=127.0.0.1:%d:80", schema.TunnelBasePort)
	if !strings.Contains(unit, want) {
		t.Errorf("unit missing %q\nunit:\n%s", want, unit)
	}
	if strings.Contains(unit, "PublishPort=127.0.0.1:80:80") {
		t.Errorf("unit must not publish host port 80 (would collide with Caddy)\nunit:\n%s", unit)
	}
}

// TestCompile_TunnelPort_NoDomainStillPublishes verifies that a service
// with a port: but no domain STILL gets a PublishPort= line, at a
// decoupled host port. `ownbasectl dev` itself never bridges a domain-less
// service (see internal/bridge.Discover), but the daemon's own HTTP
// health_probe (internal/podman's waitForContainer) needs this loopback
// publish to dial for ANY port'd service, domain or not — omitting it here
// would silently skip the HTTP health-check phase for internal services.
func TestCompile_TunnelPort_NoDomainStillPublishes(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"worker": {
					Repo: "apps/worker",
					Port: 8080,
				},
			},
		},
	}
	out := compiler.Compile(in)
	unit, ok := out.QuadletUnits["ownbase-worker.container"]
	if !ok {
		t.Fatal("ownbase-worker.container not found in output")
	}
	want := fmt.Sprintf("PublishPort=127.0.0.1:%d:8080", schema.TunnelBasePort)
	if !strings.Contains(unit, want) {
		t.Errorf("domain-less port'd service should still get %q\nunit:\n%s", want, unit)
	}
}

// TestCompile_TunnelPort_NoPortNoDomainNoPublish verifies that a service
// with neither port: nor domain: gets no PublishPort= line — there is
// nothing to publish.
func TestCompile_TunnelPort_NoPortNoDomainNoPublish(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"worker": {
					Repo: "apps/worker",
				},
			},
		},
	}
	out := compiler.Compile(in)
	unit, ok := out.QuadletUnits["ownbase-worker.container"]
	if !ok {
		t.Fatal("ownbase-worker.container not found in output")
	}
	if strings.Contains(unit, "PublishPort=") {
		t.Errorf("port-less service must not get a PublishPort= line\nunit:\n%s", unit)
	}
}

// TestCompile_TunnelPort_MultipleServicesGetDistinctPorts verifies that
// two eligible services with the SAME container port: get distinct,
// collision-free host-side dev-bridge ports, assigned deterministically by
// sorted service name.
func TestCompile_TunnelPort_MultipleServicesGetDistinctPorts(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"alpha": {Repo: "apps/alpha", Domain: "alpha.example.com", Port: 3000},
				"beta":  {Repo: "apps/beta", Domain: "beta.example.com", Port: 3000},
			},
		},
	}
	out := compiler.Compile(in)

	alphaUnit := out.QuadletUnits["ownbase-alpha.container"]
	betaUnit := out.QuadletUnits["ownbase-beta.container"]

	wantAlpha := fmt.Sprintf("PublishPort=127.0.0.1:%d:3000", schema.TunnelBasePort)
	wantBeta := fmt.Sprintf("PublishPort=127.0.0.1:%d:3000", schema.TunnelBasePort+1)
	if !strings.Contains(alphaUnit, wantAlpha) {
		t.Errorf("alpha unit missing %q\nunit:\n%s", wantAlpha, alphaUnit)
	}
	if !strings.Contains(betaUnit, wantBeta) {
		t.Errorf("beta unit missing %q\nunit:\n%s", wantBeta, betaUnit)
	}
}

// ---------------------------------------------------------------------------
// Compose export
// ---------------------------------------------------------------------------

func TestCompile_ComposeExport(t *testing.T) {
	out := compiler.Compile(testInputFull(t))
	if out.ComposeFile == "" {
		t.Fatal("ComposeFile is empty")
	}
	if !strings.Contains(out.ComposeFile, "do not hand-edit") {
		t.Error("ComposeFile missing generated header")
	}
}

// ---------------------------------------------------------------------------
// Secret bindings (M2)
// ---------------------------------------------------------------------------

func testInputSecrets(t *testing.T) compiler.Input {
	t.Helper()
	cfg, err := schema.ParseConfigFile("../../testdata/valid/secrets-config.yaml")
	if err != nil {
		t.Fatalf("parse secrets config: %v", err)
	}
	return compiler.Input{Config: cfg}
}

// TestCompile_NoSecretsFileAnnotationInContainerUnits verifies that compiled
// Quadlet units never contain a # SecretsFile= comment. Secrets are stored
// at a conventional path on the Base (/opt/ownbase/secrets/<service>.yaml.age)
// and are unknown to the compiler.
func TestCompile_NoSecretsFileAnnotationInContainerUnits(t *testing.T) {
	out := compiler.Compile(testInputSecrets(t))

	for unitName, content := range out.QuadletUnits {
		if strings.Contains(content, "SecretsFile") {
			t.Errorf("%s: must not contain SecretsFile annotation\nunit:\n%s", unitName, content)
		}
		if strings.Contains(content, "Secret=") {
			t.Errorf("%s: must not contain Secret= directives\nunit:\n%s", unitName, content)
		}
	}
}

// TestCompile_SecretBindingsDeterministic verifies that secret binding
// emission is byte-identical across two compiles (sorted order).
func TestCompile_SecretBindingsDeterministic(t *testing.T) {
	in := testInputSecrets(t)
	out1 := compiler.Compile(in)
	out2 := compiler.Compile(in)
	for name := range out1.QuadletUnits {
		if out1.QuadletUnits[name] != out2.QuadletUnits[name] {
			t.Errorf("unit %q differs between two compiles (secret binding order non-deterministic)", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Repo services (built from an external git URL via a local bare clone)
// ---------------------------------------------------------------------------

func testInputRepo(t *testing.T) compiler.Input {
	t.Helper()
	cfg, err := schema.ParseConfigFile("../../testdata/repo/ownbase.yaml")
	if err != nil {
		t.Fatalf("parse repo config: %v", err)
	}
	return compiler.Input{Config: cfg}
}

// TestCompile_RepoService_BuildSourceIsServiceName verifies that a repo:
// service's local bare-clone directory (BuildSource) is keyed by the service
// name — collision-free even when two services share an upstream URL.
func TestCompile_RepoService_BuildSourceIsServiceName(t *testing.T) {
	out := compiler.Compile(testInputRepo(t))

	unit, ok := out.QuadletUnits["ownbase-postgres.container"]
	if !ok {
		t.Fatal("ownbase-postgres.container not found in output")
	}
	if !strings.Contains(unit, "# BuildSource=postgres") {
		t.Errorf("repo unit must have BuildSource=postgres\nunit:\n%s", unit)
	}
}

// TestCompile_RepoService_UsesLocalhostImage verifies that repo services
// build locally (not pulled from a registry).
func TestCompile_RepoService_UsesLocalhostImage(t *testing.T) {
	out := compiler.Compile(testInputRepo(t))

	unit, ok := out.QuadletUnits["ownbase-postgres.container"]
	if !ok {
		t.Fatal("ownbase-postgres.container not found in output")
	}
	if !strings.Contains(unit, "Image=localhost/ownbase-postgres:local") {
		t.Errorf("repo unit must use a localhost image\nunit:\n%s", unit)
	}
	if strings.Contains(unit, "Image=") && strings.Contains(unit, "github.com") {
		t.Errorf("repo unit must not reference an external registry\nunit:\n%s", unit)
	}
}

// TestCompile_RepoService_HasBuildContext verifies that context: appears as
// a BuildContext annotation (for versioned directories like docker-library/postgres).
func TestCompile_RepoService_HasBuildContext(t *testing.T) {
	out := compiler.Compile(testInputRepo(t))
	unit := out.QuadletUnits["ownbase-postgres.container"]

	if !strings.Contains(unit, "# BuildContext=17/alpine") {
		t.Errorf("repo unit missing BuildContext annotation\nunit:\n%s", unit)
	}
}

// TestCompile_RepoService_HasVolume verifies the data volume is mounted.
func TestCompile_RepoService_HasVolume(t *testing.T) {
	out := compiler.Compile(testInputRepo(t))
	unit := out.QuadletUnits["ownbase-postgres.container"]

	if !strings.Contains(unit, "Volume=ownbase-postgres-data:/data") {
		t.Errorf("repo unit missing volume mount\nunit:\n%s", unit)
	}
}

// TestCompile_RepoService_HasHealthProbe verifies health_probe.http is emitted.
func TestCompile_RepoService_HasHealthProbe(t *testing.T) {
	out := compiler.Compile(testInputRepo(t))
	unit := out.QuadletUnits["ownbase-postgres.container"]

	if !strings.Contains(unit, "# HealthProbeHTTP=/health") {
		t.Errorf("repo unit missing health probe comment\nunit:\n%s", unit)
	}
}

// TestCompile_RepoService_SourceBuiltTimeout verifies that repo services
// get the 30s timeout for source-built services (not the 120s for core
// packages).
func TestCompile_RepoService_SourceBuiltTimeout(t *testing.T) {
	out := compiler.Compile(testInputRepo(t))
	unit := out.QuadletUnits["ownbase-postgres.container"]

	if !strings.Contains(unit, "TimeoutStartSec=30") {
		t.Errorf("repo unit should have TimeoutStartSec=30\nunit:\n%s", unit)
	}
}

// TestCompile_RepoService_Deterministic verifies byte-identical output.
func TestCompile_RepoService_Deterministic(t *testing.T) {
	in := testInputRepo(t)
	out1 := compiler.Compile(in)
	out2 := compiler.Compile(in)
	for name := range out1.QuadletUnits {
		if out1.QuadletUnits[name] != out2.QuadletUnits[name] {
			t.Errorf("unit %q not deterministic", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Multi-volume support (M12)
// ---------------------------------------------------------------------------

// TestCompile_MultiVolume_VolumeMounts verifies that when Volumes is declared
// the compiler emits one VolumeMount per declared volume, named
// "ownbase-<service>-<volume.name>", and does NOT emit the old single
// "ownbase-<service>-data" volume.
func TestCompile_MultiVolume_VolumeMounts(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"jellyfin": {
					Repo: "local/jellyfin",
					Volumes: []schema.VolumeDecl{
						{Name: "config", Mount: "/config", Backup: []string{"."}},
						{Name: "media", Mount: "/media"},
						{Name: "cache", Mount: "/cache"},
					},
				},
			},
		},
	}
	out := compiler.Compile(in)

	unit, ok := out.QuadletUnits["ownbase-jellyfin.container"]
	if !ok {
		t.Fatal("expected ownbase-jellyfin.container in output")
	}

	for _, vol := range []struct{ name, mount string }{
		{"ownbase-jellyfin-config", "/config"},
		{"ownbase-jellyfin-media", "/media"},
		{"ownbase-jellyfin-cache", "/cache"},
	} {
		want := vol.name + ":" + vol.mount
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing Volume=%s\nunit content:\n%s", want, unit)
		}
	}

	// The legacy single-volume name must not appear.
	if strings.Contains(unit, "ownbase-jellyfin-data") {
		t.Error("unit should not contain legacy ownbase-jellyfin-data when Volumes is declared")
	}
}

// TestCompile_MultiVolume_VolumeModels verifies that build emits a VolumeModel
// for each declared volume.
func TestCompile_MultiVolume_VolumeModels(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"svc": {
					Repo: "local/svc",
					Volumes: []schema.VolumeDecl{
						{Name: "data", Mount: "/data", Backup: []string{"."}},
						{Name: "tmp", Mount: "/tmp"},
					},
				},
			},
		},
	}
	out := compiler.Compile(in)

	// Expect .volume Quadlet units for each declared volume.
	for _, volName := range []string{"ownbase-svc-data", "ownbase-svc-tmp"} {
		unitFile := volName + ".volume"
		if _, ok := out.QuadletUnits[unitFile]; !ok {
			t.Errorf("expected %s in QuadletUnits; got keys: %v",
				unitFile, unitKeys(out))
		}
	}
	// Legacy volume must not appear.
	if _, ok := out.QuadletUnits["ownbase-svc-data-data.volume"]; ok {
		t.Error("unexpected ownbase-svc-data-data.volume — looks like double-suffix bug")
	}
}

// TestCompile_DataPathBackwardCompat verifies that services using the old
// data_path field (no Volumes) still produce the single "ownbase-<name>-data"
// volume.
func TestCompile_DataPathBackwardCompat(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"myapp": {
					Repo:     "local/myapp",
					DataPath: "/app/storage",
				},
			},
		},
	}
	out := compiler.Compile(in)

	unit, ok := out.QuadletUnits["ownbase-myapp.container"]
	if !ok {
		t.Fatal("expected ownbase-myapp.container in output")
	}
	if !strings.Contains(unit, "ownbase-myapp-data:/app/storage") {
		t.Errorf("unit missing Volume=ownbase-myapp-data:/app/storage\nunit:\n%s", unit)
	}
	if _, ok := out.QuadletUnits["ownbase-myapp-data.volume"]; !ok {
		t.Error("expected ownbase-myapp-data.volume in output")
	}
}

// unitKeys returns a sorted list of unit filenames for test error messages.
func unitKeys(out compiler.RuntimeOutput) []string {
	keys := make([]string, 0, len(out.QuadletUnits))
	for k := range out.QuadletUnits {
		keys = append(keys, k)
	}
	return keys
}

// ---------------------------------------------------------------------------
// WriteOutput
// ---------------------------------------------------------------------------

func TestWriteOutput_WritesRuntimeDir(t *testing.T) {
	out := compiler.Compile(testInputFull(t))
	dir := t.TempDir()
	written, err := compiler.WriteOutput(out, dir)
	if err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	runtimeDir := filepath.Join(dir, "runtime")
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		t.Fatalf("read runtime dir: %v", err)
	}
	if len(entries) != len(written) {
		t.Errorf("written count %d != dir entry count %d", len(written), len(entries))
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, required := range []string{"Caddyfile", "docker-compose.yml"} {
		if !names[required] {
			t.Errorf("expected %s in runtime dir", required)
		}
	}
}

// ---------------------------------------------------------------------------
// internal: true — tunnel-only services (no Caddy route)
// ---------------------------------------------------------------------------

// TestCompile_InternalService_NoCaddyRoute verifies that a service with
// internal: true gets NO Caddy route even though it has a domain and port
// configured. The loopback publish must still be emitted so that
// `ownbasectl tunnel` and the daemon's health_probe can both reach it.
func TestCompile_InternalService_NoCaddyRoute(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"admin": {
					Repo:     "services/admin",
					Domain:   "admin.example.com",
					Port:     3000,
					Internal: true,
				},
			},
		},
	}
	model := compiler.CompileToModel(in)

	if len(model.Routes) != 0 {
		t.Errorf("internal: true service must have no Caddy routes, got %v", model.Routes)
	}

	out := compiler.Compile(in)
	unit, ok := out.QuadletUnits["ownbase-admin.container"]
	if !ok {
		t.Fatal("ownbase-admin.container not found in output")
	}
	want := fmt.Sprintf("PublishPort=127.0.0.1:%d:3000", schema.TunnelBasePort)
	if !strings.Contains(unit, want) {
		t.Errorf("internal service should still get loopback publish %q\nunit:\n%s", want, unit)
	}
}

// TestCompile_InternalService_MixedWithPublic verifies that when an
// internal: true service and a public service coexist, only the public
// service gets a Caddy route.
func TestCompile_InternalService_MixedWithPublic(t *testing.T) {
	in := compiler.Input{
		Config: &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"admin": {
					Repo:     "services/admin",
					Domain:   "admin.example.com",
					Port:     3000,
					Internal: true,
				},
				"web": {
					Repo:   "services/web",
					Domain: "web.example.com",
					Port:   8080,
				},
			},
		},
	}
	model := compiler.CompileToModel(in)

	if len(model.Routes) != 1 {
		t.Fatalf("expected exactly 1 Caddy route (for web only), got %d: %v", len(model.Routes), model.Routes)
	}
	if model.Routes[0].Host != "web.example.com" {
		t.Errorf("expected route for web.example.com, got %q", model.Routes[0].Host)
	}
}

// ---------------------------------------------------------------------------
// Scheduled jobs (jobs:)
// ---------------------------------------------------------------------------

func testInputJobs(t *testing.T) compiler.Input {
	t.Helper()
	cfg, err := schema.ParseConfigFile("../../testdata/valid/jobs.yaml")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return compiler.Input{Config: cfg}
}

// TestCompile_Job_ReusesServiceImageAndNetworks verifies that a job
// container's image and networks are copied verbatim from the referenced
// service, rather than building anything of its own.
func TestCompile_Job_ReusesServiceImageAndNetworks(t *testing.T) {
	model := compiler.CompileToModel(testInputJobs(t))

	var api, job *compiler.ContainerModel
	for i := range model.Containers {
		switch model.Containers[i].Name {
		case "ownbase-api":
			api = &model.Containers[i]
		case "ownbase-job-nightly-ingest":
			job = &model.Containers[i]
		}
	}
	if api == nil {
		t.Fatal("ownbase-api container not found")
	}
	if job == nil {
		t.Fatal("ownbase-job-nightly-ingest container not found")
	}

	if job.Image != api.Image {
		t.Errorf("job image = %q, want service image %q", job.Image, api.Image)
	}
	if !job.IsJob {
		t.Error("expected IsJob to be true for a job container")
	}
	if job.JobService != "api" {
		t.Errorf("JobService = %q, want %q", job.JobService, "api")
	}
	if len(job.Networks) != len(api.Networks) {
		t.Fatalf("job networks = %v, want same as service %v", job.Networks, api.Networks)
	}
	for i := range api.Networks {
		if job.Networks[i] != api.Networks[i] {
			t.Errorf("job network[%d] = %q, want %q", i, job.Networks[i], api.Networks[i])
		}
	}
	if job.BuildSource != "" {
		t.Errorf("job container must not carry its own build provenance, got BuildSource=%q", job.BuildSource)
	}
}

// TestCompile_Job_EnvAppendedAfterServiceEnv verifies that a job's env: is
// layered after (not instead of) the referenced service's own env:.
func TestCompile_Job_EnvAppendedAfterServiceEnv(t *testing.T) {
	model := compiler.CompileToModel(testInputJobs(t))
	var job *compiler.ContainerModel
	for i := range model.Containers {
		if model.Containers[i].Name == "ownbase-job-nightly-ingest" {
			job = &model.Containers[i]
		}
	}
	if job == nil {
		t.Fatal("ownbase-job-nightly-ingest container not found")
	}
	want := []string{"SHARED_FLAG=1", "REVOLVE_FEED_URL_MX=https://example.com/feed.csv"}
	if len(job.Env) != len(want) {
		t.Fatalf("Env = %v, want %v", job.Env, want)
	}
	for i := range want {
		if job.Env[i] != want[i] {
			t.Errorf("Env[%d] = %q, want %q", i, job.Env[i], want[i])
		}
	}
}

// TestCompile_Job_NeverPublicOrHealthProbed verifies that a job container
// never gets a Caddy route, tunnel/loopback publish, or health probe — it
// is not a server, and waitForContainer must never be asked to poll it.
func TestCompile_Job_NeverPublicOrHealthProbed(t *testing.T) {
	model := compiler.CompileToModel(testInputJobs(t))
	for _, r := range model.Routes {
		if strings.Contains(r.Upstream, "job") {
			t.Errorf("job must have no Caddy route, got %+v", r)
		}
	}
	out := compiler.Compile(testInputJobs(t))
	unit, ok := out.QuadletUnits["ownbase-job-nightly-ingest.container"]
	if !ok {
		t.Fatal("ownbase-job-nightly-ingest.container not in output")
	}
	if strings.Contains(unit, "PublishPort=") {
		t.Errorf("job container must not publish any port\nunit:\n%s", unit)
	}
	if strings.Contains(unit, "# HealthProbeHTTP=") {
		t.Errorf("job container must not declare a health probe\nunit:\n%s", unit)
	}
}

// TestCompile_Job_OneshotNoRestartNoInstall verifies the systemd semantics
// that make a job safe to install without reconcile accidentally crash-
// looping or auto-starting it at boot: Type=oneshot, Restart=no, and no
// [Install] section (see internal/reconcile.isJobContainer for why "not
// running" must never be read as "needs a start" for these units).
func TestCompile_Job_OneshotNoRestartNoInstall(t *testing.T) {
	out := compiler.Compile(testInputJobs(t))
	unit, ok := out.QuadletUnits["ownbase-job-nightly-ingest.container"]
	if !ok {
		t.Fatal("ownbase-job-nightly-ingest.container not in output")
	}
	for _, want := range []string{"Type=oneshot", "Restart=no"} {
		if !strings.Contains(unit, want) {
			t.Errorf("job unit missing %q\nunit:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "Restart=always") {
		t.Errorf("job unit must not carry Restart=always\nunit:\n%s", unit)
	}
	if strings.Contains(unit, "[Install]") {
		t.Errorf("job unit must not carry an [Install] section\nunit:\n%s", unit)
	}
}

// TestCompile_Job_ExecOverridesCommand verifies the command: override is
// rendered as a Quadlet Exec= line, quoting any argument containing spaces.
func TestCompile_Job_ExecOverridesCommand(t *testing.T) {
	out := compiler.Compile(testInputJobs(t))
	unit, ok := out.QuadletUnits["ownbase-job-nightly-ingest.container"]
	if !ok {
		t.Fatal("ownbase-job-nightly-ingest.container not in output")
	}
	want := `Exec=python scripts/nightly_ingest.py --region mx`
	if !strings.Contains(unit, want) {
		t.Errorf("job unit missing %q\nunit:\n%s", want, unit)
	}
}

// TestCompile_Job_JobServiceProvenanceComment verifies the applier-facing
// "# JobService=" comment used by internal/podman's injectSecrets to merge
// in the referenced service's secrets.
func TestCompile_Job_JobServiceProvenanceComment(t *testing.T) {
	out := compiler.Compile(testInputJobs(t))
	unit, ok := out.QuadletUnits["ownbase-job-nightly-ingest.container"]
	if !ok {
		t.Fatal("ownbase-job-nightly-ingest.container not in output")
	}
	if !strings.Contains(unit, "# JobService=api") {
		t.Errorf("job unit missing \"# JobService=api\"\nunit:\n%s", unit)
	}
}

// TestCompile_Job_NoVolumeMounts verifies that v1 jobs get no volume mounts
// (see JobDecl's doc comment) — a job must not silently start writing into
// the referenced service's own data volume.
func TestCompile_Job_NoVolumeMounts(t *testing.T) {
	model := compiler.CompileToModel(testInputJobs(t))
	for i := range model.Containers {
		if model.Containers[i].Name == "ownbase-job-nightly-ingest" {
			if len(model.Containers[i].VolumeMounts) != 0 {
				t.Errorf("job container must have no volume mounts, got %v", model.Containers[i].VolumeMounts)
			}
			return
		}
	}
	t.Fatal("ownbase-job-nightly-ingest container not found")
}

// TestCompile_Job_TimerRendersOnCalendarAndPersistent verifies the
// companion .timer unit carries the job's schedule and persistent: setting.
func TestCompile_Job_TimerRendersOnCalendarAndPersistent(t *testing.T) {
	out := compiler.Compile(testInputJobs(t))

	timer, ok := out.QuadletUnits["ownbase-job-nightly-ingest.timer"]
	if !ok {
		t.Fatal("ownbase-job-nightly-ingest.timer not in output")
	}
	if !strings.Contains(timer, "OnCalendar=*-*-* 08:00:00 UTC") {
		t.Errorf("timer missing OnCalendar\nunit:\n%s", timer)
	}
	if !strings.Contains(timer, "Persistent=true") {
		t.Errorf("nightly-ingest timer should default persistent: true\nunit:\n%s", timer)
	}
	if !strings.Contains(timer, "WantedBy=timers.target") {
		t.Errorf("timer missing WantedBy=timers.target\nunit:\n%s", timer)
	}

	cleanupTimer, ok := out.QuadletUnits["ownbase-job-cleanup.timer"]
	if !ok {
		t.Fatal("ownbase-job-cleanup.timer not in output")
	}
	if !strings.Contains(cleanupTimer, "Persistent=false") {
		t.Errorf("cleanup timer set persistent: false, want Persistent=false\nunit:\n%s", cleanupTimer)
	}
}

// TestCompile_Job_NoExtraNetworkOrVolumeCreated verifies that jobs don't
// cause the compiler to emit any extra Network/Volume model beyond what the
// referenced service already needed — a job piggybacks entirely on the
// service's own capability network.
func TestCompile_Job_NoExtraNetworkOrVolumeCreated(t *testing.T) {
	model := compiler.CompileToModel(testInputJobs(t))
	seen := map[string]bool{}
	for _, n := range model.Networks {
		if seen[n.Name] {
			t.Errorf("network %q appears more than once", n.Name)
		}
		seen[n.Name] = true
	}
	// jobs.yaml has exactly one service ("api"), so exactly its own
	// capability network + the shared internal network are expected —
	// nothing job-specific.
	want := map[string]bool{"ownbase-api-net": true, "ownbase-internal": true}
	if len(model.Networks) != len(want) {
		t.Fatalf("networks = %v, want exactly %v", model.Networks, want)
	}
	for _, n := range model.Networks {
		if !want[n.Name] {
			t.Errorf("unexpected network %q", n.Name)
		}
	}
}
