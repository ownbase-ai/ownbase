package vmhost

import (
	"context"
	"strings"
	"testing"
)

// fakeRunner records every invocation and returns canned responses keyed by
// the joined argument string prefix.
type fakeRunner struct {
	calls     [][]string
	responses map[string]string
	errs      map[string]error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{responses: map[string]string{}, errs: map[string]error{}}
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args, " ")
	for k, err := range f.errs {
		if strings.HasPrefix(key, k) {
			return f.responses[k], err
		}
	}
	for k, resp := range f.responses {
		if strings.HasPrefix(key, k) {
			return resp, nil
		}
	}
	return "", nil
}

func TestLaunch_Defaults(t *testing.T) {
	r := newFakeRunner()
	m := &Multipass{Runner: r}

	if err := m.Launch(context.Background(), "ownbase-fresh", LaunchOptions{}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	got := strings.Join(r.calls[0], " ")
	for _, want := range []string{"launch 24.04", "--name ownbase-fresh", "--cpus 2", "--memory 2G", "--disk 15G"} {
		if !strings.Contains(got, want) {
			t.Errorf("launch args %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "--cloud-init") {
		t.Errorf("expected no --cloud-init when unset, got %q", got)
	}
}

func TestLaunch_CustomOptions(t *testing.T) {
	r := newFakeRunner()
	m := &Multipass{Runner: r}

	opts := LaunchOptions{Image: "22.04", CPUs: 4, MemoryGB: 8, DiskGB: 40, CloudInitPath: "/tmp/ci.yaml"}
	if err := m.Launch(context.Background(), "myvm", opts); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	got := strings.Join(r.calls[0], " ")
	for _, want := range []string{"launch 22.04", "--cpus 4", "--memory 8G", "--disk 40G", "--cloud-init /tmp/ci.yaml"} {
		if !strings.Contains(got, want) {
			t.Errorf("launch args %q missing %q", got, want)
		}
	}
}

func TestDelete(t *testing.T) {
	r := newFakeRunner()
	m := &Multipass{Runner: r}
	if err := m.Delete(context.Background(), "ownbase-fresh"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got := strings.Join(r.calls[0], " ")
	if got != "delete --purge ownbase-fresh" {
		t.Errorf("unexpected delete args: %q", got)
	}
}

func TestDeleteIfExists_SkipsWhenAbsent(t *testing.T) {
	r := newFakeRunner()
	r.responses["list"] = `{"list":[{"name":"other","state":"Running"}]}`
	m := &Multipass{Runner: r}

	if err := m.DeleteIfExists(context.Background(), "ownbase-fresh"); err != nil {
		t.Fatalf("DeleteIfExists: %v", err)
	}
	for _, c := range r.calls {
		if c[0] == "delete" {
			t.Errorf("expected no delete call, got %v", c)
		}
	}
}

func TestDeleteIfExists_DeletesWhenPresent(t *testing.T) {
	r := newFakeRunner()
	r.responses["list"] = `{"list":[{"name":"ownbase-fresh","state":"Running"}]}`
	m := &Multipass{Runner: r}

	if err := m.DeleteIfExists(context.Background(), "ownbase-fresh"); err != nil {
		t.Fatalf("DeleteIfExists: %v", err)
	}
	var sawDelete bool
	for _, c := range r.calls {
		if c[0] == "delete" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Error("expected a delete call")
	}
}

func TestStop(t *testing.T) {
	r := newFakeRunner()
	m := &Multipass{Runner: r}
	if err := m.Stop(context.Background(), "ownbase-fresh"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got := strings.Join(r.calls[0], " ")
	if got != "stop ownbase-fresh" {
		t.Errorf("unexpected stop args: %q", got)
	}
}

func TestStart(t *testing.T) {
	r := newFakeRunner()
	m := &Multipass{Runner: r}
	if err := m.Start(context.Background(), "ownbase-fresh"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := strings.Join(r.calls[0], " ")
	if got != "start ownbase-fresh" {
		t.Errorf("unexpected start args: %q", got)
	}
}

func TestList(t *testing.T) {
	r := newFakeRunner()
	r.responses["list"] = `{"list":[{"name":"a","state":"Running"},{"name":"b","state":"Stopped"}]}`
	m := &Multipass{Runner: r}

	vms, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != 2 || vms[0].Name != "a" || vms[1].State != "Stopped" {
		t.Errorf("unexpected list result: %+v", vms)
	}
}

func TestExists(t *testing.T) {
	r := newFakeRunner()
	r.responses["list"] = `{"list":[{"name":"ownbase-fresh","state":"Running"}]}`
	m := &Multipass{Runner: r}

	exists, err := m.Exists(context.Background(), "ownbase-fresh")
	if err != nil || !exists {
		t.Fatalf("expected exists=true, got %v err=%v", exists, err)
	}
	exists, err = m.Exists(context.Background(), "nope")
	if err != nil || exists {
		t.Fatalf("expected exists=false, got %v err=%v", exists, err)
	}
}

func TestIPv4(t *testing.T) {
	r := newFakeRunner()
	r.responses["info ownbase-fresh"] = `{"info":{"ownbase-fresh":{"ipv4":["192.168.1.50"],"state":"Running"}}}`
	m := &Multipass{Runner: r}

	ip, err := m.IPv4(context.Background(), "ownbase-fresh")
	if err != nil {
		t.Fatalf("IPv4: %v", err)
	}
	if ip != "192.168.1.50" {
		t.Errorf("expected 192.168.1.50, got %q", ip)
	}
}

func TestIPv4_NoAddress(t *testing.T) {
	r := newFakeRunner()
	r.responses["info ownbase-fresh"] = `{"info":{"ownbase-fresh":{"ipv4":[],"state":"Starting"}}}`
	m := &Multipass{Runner: r}

	if _, err := m.IPv4(context.Background(), "ownbase-fresh"); err == nil {
		t.Error("expected error when no IPv4 address is present")
	}
}

func TestState(t *testing.T) {
	r := newFakeRunner()
	r.responses["info ownbase-fresh"] = `{"info":{"ownbase-fresh":{"ipv4":["1.2.3.4"],"state":"Running"}}}`
	m := &Multipass{Runner: r}

	state, err := m.State(context.Background(), "ownbase-fresh")
	if err != nil || state != "Running" {
		t.Fatalf("expected Running, got %q err=%v", state, err)
	}
}

func TestExec(t *testing.T) {
	r := newFakeRunner()
	m := &Multipass{Runner: r}
	if _, err := m.Exec(context.Background(), "ownbase-fresh", "hostname"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := strings.Join(r.calls[0], " ")
	if got != "exec ownbase-fresh -- hostname" {
		t.Errorf("unexpected exec args: %q", got)
	}
}

func TestTransfer(t *testing.T) {
	r := newFakeRunner()
	m := &Multipass{Runner: r}
	if err := m.Transfer(context.Background(), "/local/install.sh", "ownbase-fresh", "/home/ubuntu/install.sh"); err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	got := strings.Join(r.calls[0], " ")
	if got != "transfer /local/install.sh ownbase-fresh:/home/ubuntu/install.sh" {
		t.Errorf("unexpected transfer args: %q", got)
	}
}

func TestRunSudoScript(t *testing.T) {
	r := newFakeRunner()
	m := &Multipass{Runner: r}
	env := map[string]string{"FOO": "bar baz"}
	if _, err := m.RunSudoScript(context.Background(), "ownbase-fresh", "/home/ubuntu/install.sh", env); err != nil {
		t.Fatalf("RunSudoScript: %v", err)
	}
	got := strings.Join(r.calls[0], " ")
	if !strings.HasPrefix(got, "exec ownbase-fresh -- sudo bash -c ") {
		t.Errorf("unexpected prefix: %q", got)
	}
	if !strings.Contains(got, `FOO='bar baz'`) {
		t.Errorf("expected quoted env var, got %q", got)
	}
	if !strings.Contains(got, "bash /home/ubuntu/install.sh") {
		t.Errorf("expected script invocation, got %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"simple":    "'simple'",
		"has space": "'has space'",
		"o'brien":   `'o'\''brien'`,
		"":          "''",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
