package compiler_test

import (
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
// Mirror services (source built from external git URL via local bare mirror)
// ---------------------------------------------------------------------------

func testInputMirror(t *testing.T) compiler.Input {
	t.Helper()
	cfg, err := schema.ParseConfigFile("../../testdata/mirror/ownbase.yaml")
	if err != nil {
		t.Fatalf("parse mirror config: %v", err)
	}
	return compiler.Input{Config: cfg}
}

// TestCompile_MirrorService_BuildSourceFromURL verifies that a mirror: service
// resolves to mirrors-<basename> as the local bare-repo name (flat name, no
// nested directories) in the Quadlet unit.
func TestCompile_MirrorService_BuildSourceFromURL(t *testing.T) {
	out := compiler.Compile(testInputMirror(t))

	unit, ok := out.QuadletUnits["ownbase-postgres.container"]
	if !ok {
		t.Fatal("ownbase-postgres.container not found in output")
	}
	if !strings.Contains(unit, "# BuildSource=mirrors-postgres") {
		t.Errorf("mirror unit must have BuildSource=mirrors/postgres\nunit:\n%s", unit)
	}
}

// TestCompile_MirrorService_UsesLocalhostImage verifies that mirror services
// build locally (not pulled from a registry).
func TestCompile_MirrorService_UsesLocalhostImage(t *testing.T) {
	out := compiler.Compile(testInputMirror(t))

	unit, ok := out.QuadletUnits["ownbase-postgres.container"]
	if !ok {
		t.Fatal("ownbase-postgres.container not found in output")
	}
	if !strings.Contains(unit, "Image=localhost/ownbase-postgres:local") {
		t.Errorf("mirror unit must use a localhost image\nunit:\n%s", unit)
	}
	if strings.Contains(unit, "Image=") && strings.Contains(unit, "github.com") {
		t.Errorf("mirror unit must not reference an external registry\nunit:\n%s", unit)
	}
}

// TestCompile_MirrorService_HasBuildContext verifies that context: appears as
// a BuildContext annotation (for versioned directories like docker-library/postgres).
func TestCompile_MirrorService_HasBuildContext(t *testing.T) {
	out := compiler.Compile(testInputMirror(t))
	unit := out.QuadletUnits["ownbase-postgres.container"]

	if !strings.Contains(unit, "# BuildContext=17/alpine") {
		t.Errorf("mirror unit missing BuildContext annotation\nunit:\n%s", unit)
	}
}

// TestCompile_MirrorService_HasVolume verifies the data volume is mounted.
func TestCompile_MirrorService_HasVolume(t *testing.T) {
	out := compiler.Compile(testInputMirror(t))
	unit := out.QuadletUnits["ownbase-postgres.container"]

	if !strings.Contains(unit, "Volume=ownbase-postgres-data:/data") {
		t.Errorf("mirror unit missing volume mount\nunit:\n%s", unit)
	}
}

// TestCompile_MirrorService_HasHealthProbe verifies health_probe.http is emitted.
func TestCompile_MirrorService_HasHealthProbe(t *testing.T) {
	out := compiler.Compile(testInputMirror(t))
	unit := out.QuadletUnits["ownbase-postgres.container"]

	if !strings.Contains(unit, "# HealthProbeHTTP=/health") {
		t.Errorf("mirror unit missing health probe comment\nunit:\n%s", unit)
	}
}

// TestCompile_MirrorService_SourceBuiltTimeout verifies that mirror services
// get the same 30s timeout as source-built services (not the 120s for core
// packages).
func TestCompile_MirrorService_SourceBuiltTimeout(t *testing.T) {
	out := compiler.Compile(testInputMirror(t))
	unit := out.QuadletUnits["ownbase-postgres.container"]

	if !strings.Contains(unit, "TimeoutStartSec=30") {
		t.Errorf("mirror unit should have TimeoutStartSec=30\nunit:\n%s", unit)
	}
}

// TestCompile_MirrorService_Deterministic verifies byte-identical output.
func TestCompile_MirrorService_Deterministic(t *testing.T) {
	in := testInputMirror(t)
	out1 := compiler.Compile(in)
	out2 := compiler.Compile(in)
	for name := range out1.QuadletUnits {
		if out1.QuadletUnits[name] != out2.QuadletUnits[name] {
			t.Errorf("unit %q not deterministic", name)
		}
	}
}

// TestCompile_MirrorRepoName verifies the URL → local bare-repo name derivation.
func TestCompile_MirrorRepoName(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/docker-library/postgres", "mirrors-postgres"},
		{"https://github.com/org/crm.git", "mirrors-crm"},
		{"http://github.com/org/auth", "mirrors-auth"},
		{"git@github.com:org/myapp.git", "mirrors-myapp"},
		{"git://github.com/org/tool", "mirrors-tool"},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			got := compiler.MirrorRepoName(tc.url)
			if got != tc.want {
				t.Errorf("MirrorRepoName(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
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
					Source: "local/jellyfin",
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
					Source: "local/svc",
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
					Source:   "local/myapp",
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
