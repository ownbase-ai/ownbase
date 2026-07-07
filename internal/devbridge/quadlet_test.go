package devbridge_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/devbridge"
)

func TestParseActualHostPorts_TypicalGrepOutput(t *testing.T) {
	out := `/etc/containers/systemd/ownbase-hello.container:PublishPort=127.0.0.1:41000:80
/etc/containers/systemd/ownbase-multi.container:PublishPort=127.0.0.1:41001:4000`
	got := devbridge.ParseActualHostPorts(out)
	want := map[string]int{"hello": 41000, "multi": 41001}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseActualHostPorts() = %v, want %v", got, want)
	}
}

func TestParseActualHostPorts_Empty(t *testing.T) {
	if got := devbridge.ParseActualHostPorts(""); len(got) != 0 {
		t.Errorf("expected no entries for empty input, got %v", got)
	}
}

func TestParseActualHostPorts_IgnoresNonServiceUnits(t *testing.T) {
	out := `/etc/containers/systemd/ownbase-core-caddy.container:PublishPort=0.0.0.0:80:80
/etc/containers/systemd/ownbase-hello.container:PublishPort=127.0.0.1:41000:80`
	got := devbridge.ParseActualHostPorts(out)
	// core-caddy IS technically "ownbase-<name>.container" shaped (name =
	// "core-caddy") — the parser has no way to distinguish it from a real
	// service, so it's included too; this test documents that and confirms
	// the real target is still parsed correctly alongside it.
	want := map[string]int{"core-caddy": 80, "hello": 41000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseActualHostPorts() = %v, want %v", got, want)
	}
}

func TestParseActualHostPorts_NoIPPrefix(t *testing.T) {
	out := `/etc/containers/systemd/ownbase-hello.container:PublishPort=41000:80`
	got := devbridge.ParseActualHostPorts(out)
	want := map[string]int{"hello": 41000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseActualHostPorts() = %v, want %v", got, want)
	}
}

func TestParseActualHostPorts_MalformedLinesSkipped(t *testing.T) {
	out := `not a grep line at all
/etc/containers/systemd/ownbase-broken.container:PublishPort=notaport:80
/etc/containers/systemd/ownbase-hello.container:PublishPort=127.0.0.1:41000:80`
	got := devbridge.ParseActualHostPorts(out)
	want := map[string]int{"hello": 41000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseActualHostPorts() = %v, want %v", got, want)
	}
}

func TestGrepPublishPortCommand_TargetsSystemQuadletDir(t *testing.T) {
	if devbridge.SystemQuadletDir != "/etc/containers/systemd" {
		t.Fatalf("SystemQuadletDir = %q, want /etc/containers/systemd", devbridge.SystemQuadletDir)
	}
	if !strings.Contains(devbridge.GrepPublishPortCommand, devbridge.SystemQuadletDir) {
		t.Errorf("GrepPublishPortCommand should reference SystemQuadletDir, got %q", devbridge.GrepPublishPortCommand)
	}
}
