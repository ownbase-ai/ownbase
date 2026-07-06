package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// upsertHostsBlock
// ---------------------------------------------------------------------------

func TestUpsertHostsBlock_InsertsNewBlock(t *testing.T) {
	current := "127.0.0.1 localhost\n"
	got := string(upsertHostsBlock([]byte(current), "mybase", "192.168.64.10", []string{"forgejo.mybase.test"}))

	if !strings.Contains(got, "127.0.0.1 localhost") {
		t.Errorf("expected existing content preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "# BEGIN OWNBASE mybase") || !strings.Contains(got, "# END OWNBASE mybase") {
		t.Errorf("expected marker lines, got:\n%s", got)
	}
	if !strings.Contains(got, "192.168.64.10 forgejo.mybase.test") {
		t.Errorf("expected host entry line, got:\n%s", got)
	}
}

func TestUpsertHostsBlock_ReplacesExistingBlock(t *testing.T) {
	current := strings.Join([]string{
		"127.0.0.1 localhost",
		"",
		"# BEGIN OWNBASE mybase",
		"192.168.64.10 forgejo.mybase.test",
		"# END OWNBASE mybase",
	}, "\n") + "\n"

	got := string(upsertHostsBlock([]byte(current), "mybase", "192.168.64.99", []string{"forgejo.mybase.test", "app.mybase.test"}))

	if strings.Contains(got, "192.168.64.10") {
		t.Errorf("expected old IP removed, got:\n%s", got)
	}
	if !strings.Contains(got, "192.168.64.99 app.mybase.test forgejo.mybase.test") {
		t.Errorf("expected new entry line with both hostnames, got:\n%s", got)
	}
	// Only one block for this Base — no duplicate markers.
	if strings.Count(got, "# BEGIN OWNBASE mybase") != 1 {
		t.Errorf("expected exactly one BEGIN marker, got:\n%s", got)
	}
}

func TestUpsertHostsBlock_LeavesOtherBasesUntouched(t *testing.T) {
	current := strings.Join([]string{
		"127.0.0.1 localhost",
		"",
		"# BEGIN OWNBASE other",
		"192.168.64.5 forgejo.other.test",
		"# END OWNBASE other",
	}, "\n") + "\n"

	got := string(upsertHostsBlock([]byte(current), "mybase", "192.168.64.10", []string{"forgejo.mybase.test"}))

	if !strings.Contains(got, "# BEGIN OWNBASE other") || !strings.Contains(got, "192.168.64.5 forgejo.other.test") {
		t.Errorf("expected the other Base's block to survive untouched, got:\n%s", got)
	}
	if !strings.Contains(got, "# BEGIN OWNBASE mybase") {
		t.Errorf("expected mybase's new block to be added, got:\n%s", got)
	}
}

func TestUpsertHostsBlock_EmptyHostnamesRemovesBlock(t *testing.T) {
	current := strings.Join([]string{
		"127.0.0.1 localhost",
		"",
		"# BEGIN OWNBASE mybase",
		"192.168.64.10 forgejo.mybase.test",
		"# END OWNBASE mybase",
	}, "\n") + "\n"

	got := string(upsertHostsBlock([]byte(current), "mybase", "", nil))

	if strings.Contains(got, "OWNBASE mybase") {
		t.Errorf("expected block fully removed, got:\n%s", got)
	}
	if !strings.Contains(got, "127.0.0.1 localhost") {
		t.Errorf("expected unrelated content preserved, got:\n%s", got)
	}
}

func TestUpsertHostsBlock_IdempotentNoChangeOnRerun(t *testing.T) {
	first := upsertHostsBlock([]byte("127.0.0.1 localhost\n"), "mybase", "192.168.64.10", []string{"forgejo.mybase.test"})
	second := upsertHostsBlock(first, "mybase", "192.168.64.10", []string{"forgejo.mybase.test"})
	if string(first) != string(second) {
		t.Errorf("expected idempotent output, got:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// ---------------------------------------------------------------------------
// isCoveredByWildcard
// ---------------------------------------------------------------------------

func TestIsCoveredByWildcard(t *testing.T) {
	cases := []struct {
		host, domain string
		want         bool
	}{
		{"mybase.test", "mybase.test", true},
		{"forgejo.mybase.test", "mybase.test", true},
		{"app.mybase.test", "mybase.test", true},
		{"a.b.mybase.test", "mybase.test", false},
		{"mybase.test.evil.com", "mybase.test", false},
		{"otherbase.test", "mybase.test", false},
	}
	for _, c := range cases {
		if got := isCoveredByWildcard(c.host, c.domain); got != c.want {
			t.Errorf("isCoveredByWildcard(%q, %q) = %v, want %v", c.host, c.domain, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// devTLSHostnamesFromYAML
// ---------------------------------------------------------------------------

func TestDevTLSHostnamesFromYAML_ForgejoAndServiceDomains(t *testing.T) {
	yaml := `schema_version: v1
core:
  forgejo:
    domain: forgejo.mybase.test
services:
  app:
    source: services/app
    port: 8080
    domain: app.mybase.test
  internal:
    source: services/internal
    port: 9090
`
	all, uncovered, err := devTLSHostnamesFromYAML(yaml, "mybase.test")
	if err != nil {
		t.Fatalf("devTLSHostnamesFromYAML: %v", err)
	}
	want := []string{"app.mybase.test", "forgejo.mybase.test"}
	if len(all) != len(want) {
		t.Fatalf("all = %v, want %v", all, want)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Errorf("all[%d] = %q, want %q", i, all[i], want[i])
		}
	}
	if len(uncovered) != 0 {
		t.Errorf("expected no uncovered hostnames, got %v", uncovered)
	}
}

func TestDevTLSHostnamesFromYAML_FlagsUncoveredDomain(t *testing.T) {
	yaml := `schema_version: v1
services:
  app:
    source: services/app
    port: 8080
    domain: app.otherdomain.test
`
	all, uncovered, err := devTLSHostnamesFromYAML(yaml, "mybase.test")
	if err != nil {
		t.Fatalf("devTLSHostnamesFromYAML: %v", err)
	}
	if len(all) != 1 || all[0] != "app.otherdomain.test" {
		t.Fatalf("all = %v", all)
	}
	if len(uncovered) != 1 || uncovered[0] != "app.otherdomain.test" {
		t.Fatalf("uncovered = %v", uncovered)
	}
}

// ---------------------------------------------------------------------------
// rewriteEtcHosts / existingHostsForBase (via the etcHostsPath test seam)
// ---------------------------------------------------------------------------

func withTempEtcHosts(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp hosts file: %v", err)
	}
	orig := etcHostsPath
	etcHostsPath = path
	t.Cleanup(func() { etcHostsPath = orig })
	return path
}

func TestRewriteEtcHosts_NoopWhenUnchanged(t *testing.T) {
	path := withTempEtcHosts(t, "127.0.0.1 localhost\n")

	err := rewriteEtcHosts(func(current []byte) []byte { return current })
	if err != nil {
		t.Fatalf("rewriteEtcHosts: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "127.0.0.1 localhost\n" {
		t.Errorf("expected file untouched, got: %q", data)
	}
}

func TestExistingHostsForBase_ReturnsRecordedHostnames(t *testing.T) {
	withTempEtcHosts(t, strings.Join([]string{
		"127.0.0.1 localhost",
		"",
		"# BEGIN OWNBASE mybase",
		"192.168.64.10 forgejo.mybase.test app.mybase.test",
		"# END OWNBASE mybase",
	}, "\n")+"\n")

	got, err := existingHostsForBase("mybase")
	if err != nil {
		t.Fatalf("existingHostsForBase: %v", err)
	}
	want := []string{"forgejo.mybase.test", "app.mybase.test"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExistingHostsForBase_NoBlock(t *testing.T) {
	withTempEtcHosts(t, "127.0.0.1 localhost\n")

	got, err := existingHostsForBase("mybase")
	if err != nil {
		t.Fatalf("existingHostsForBase: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no hostnames, got %v", got)
	}
}

func TestDevTLSHostnamesFromYAML_NoDomains(t *testing.T) {
	yaml := `schema_version: v1
services:
  internal:
    source: services/internal
    port: 9090
`
	all, uncovered, err := devTLSHostnamesFromYAML(yaml, "mybase.test")
	if err != nil {
		t.Fatalf("devTLSHostnamesFromYAML: %v", err)
	}
	if len(all) != 0 || len(uncovered) != 0 {
		t.Errorf("expected no hostnames, got all=%v uncovered=%v", all, uncovered)
	}
}
