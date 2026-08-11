package compiler_test

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/schema"
)

func TestCompile_OwnbaseAccessBindMount(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"opencode": {
				Repo:          "https://github.com/example/opencode.git",
				Port:          4096,
				OwnbaseAccess: []string{"status:read", "service:web:deploy"},
			},
			"plain": {
				Repo: "https://github.com/example/plain.git",
				Port: 8080,
			},
		},
	}
	model := compiler.CompileToModel(compiler.Input{Config: cfg})
	out := compiler.Compile(compiler.Input{Config: cfg})

	var opencode, plain *compiler.ContainerModel
	for i := range model.Containers {
		c := &model.Containers[i]
		switch c.Name {
		case "ownbase-opencode":
			opencode = c
		case "ownbase-plain":
			plain = c
		}
	}
	if opencode == nil || plain == nil {
		t.Fatalf("containers not found")
	}

	found := false
	for _, vm := range opencode.VolumeMounts {
		if vm.HostPath == compiler.OwnbaseAPISocketHost("opencode") &&
			vm.MountPath == compiler.OwnbaseAPISocketContainer {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("opencode missing API socket bind: %+v", opencode.VolumeMounts)
	}
	for _, vm := range plain.VolumeMounts {
		if vm.HostPath != "" {
			t.Errorf("plain should have no host binds: %+v", vm)
		}
	}

	unit, ok := out.QuadletUnits["ownbase-opencode.container"]
	if !ok {
		t.Fatal("missing opencode unit")
	}
	want := "Volume=/run/ownbase/svc/opencode.sock:/run/ownbase.sock"
	if !strings.Contains(unit, want) {
		t.Errorf("unit missing %q\n%s", want, unit)
	}
}
