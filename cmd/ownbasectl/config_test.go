package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
)

// TestConfigSet_DryRunAndJSONFlags documents that config set accepts the same
// --dry-run/--json pair as deploy so the app can show a DiffPreview.
func TestConfigSet_DryRunAndJSONFlags(t *testing.T) {
	cmd := newConfigSetCmd()
	for _, flag := range []string{"json", "dry-run", "file", "message"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("config set missing --%s flag", flag)
		}
	}
}

// TestConfigSet_InvalidYAMLFailsBeforeNetwork ensures validation rejects a
// bad document without needing a Base — dry-run and apply share this gate.
func TestConfigSet_InvalidYAMLFailsBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("not: valid: yaml: [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runConfigSet("anybase", path, "", false, true)
	if err == nil {
		t.Fatal("expected invalid ownbase.yaml to fail")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention invalid config, got: %v", err)
	}
}

// TestDefaultOwnbaseYAML_IsValid guards the seed written by
// `config setup --init`: it must parse and validate as an ownbase.yaml so the
// first reconcile on a freshly-seeded config repo never fails.
func TestDefaultOwnbaseYAML_IsValid(t *testing.T) {
	cfg, err := schema.ParseConfig(strings.NewReader(defaultOwnbaseYAML))
	if err != nil {
		t.Fatalf("seeded default ownbase.yaml is invalid: %v", err)
	}
	if cfg.SchemaVersion != schema.CurrentSchemaVersion {
		t.Errorf("schema_version = %q, want %q", cfg.SchemaVersion, schema.CurrentSchemaVersion)
	}
}

// TestDefaultOwnbaseYAML_PostgresIsRecoverable pins the settings that make the
// scaffolded Postgres actually recoverable. Each assertion here corresponds to
// a failure mode that is silent until the day someone needs a restore, so they
// are checked rather than left to a comment in the template.
func TestDefaultOwnbaseYAML_PostgresIsRecoverable(t *testing.T) {
	cfg, err := schema.ParseConfig(strings.NewReader(defaultOwnbaseYAML))
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}

	pg, ok := cfg.Services["postgres"]
	if !ok {
		t.Fatal("seed has no postgres service")
	}
	repo, ok := cfg.Services["pgbackrest"]
	if !ok {
		t.Fatal("seed has no pgbackrest service")
	}

	// Both halves must build from the same pinned commit, or the client and
	// the repository host can disagree about the repo format.
	if pg.Ref != pgBackRestRef || repo.Ref != pgBackRestRef {
		t.Errorf("refs = (%q, %q), want both %q", pg.Ref, repo.Ref, pgBackRestRef)
	}

	// Postgres cannot complete its end-of-recovery checkpoint under the
	// default AppArmor profile.
	if !containsString(pg.SecurityOpt, "apparmor=unconfined") {
		t.Errorf("postgres security_opt = %v, must include apparmor=unconfined", pg.SecurityOpt)
	}

	// sshd on the repository host resets every connection without SYS_CHROOT.
	for _, want := range []string{"SETUID", "SETGID", "SYS_CHROOT"} {
		if !containsString(repo.AddCapabilities, want) {
			t.Errorf("pgbackrest add_capabilities = %v, must include %s", repo.AddCapabilities, want)
		}
	}

	// The pgBackRest repository is what makes recovery possible off-machine;
	// the live data directory must NOT be copied, since a file-level copy of it
	// is crash-inconsistent and cannot do point-in-time recovery.
	if got := backupPaths(repo, "repo"); len(got) == 0 {
		t.Error("pgbackrest repo volume has no backup: paths — nothing would reach the restic repository")
	}
	if got := backupPaths(pg, "data"); len(got) != 0 {
		t.Errorf("postgres data volume declares backup: %v — a live data directory must not be file-copied", got)
	}

	// Neither a keypair nor a password should have to be produced by hand.
	if len(repo.GeneratedSecrets) == 0 {
		t.Error("pgbackrest declares no generated_secrets — the SSH keypair would need ssh-keygen by hand")
	}
	if len(pg.GeneratedSecrets) == 0 {
		t.Error("postgres declares no generated_secrets — POSTGRES_PASSWORD would have to be invented")
	}
}

func backupPaths(svc schema.ServiceDecl, volume string) []string {
	for _, v := range svc.Volumes {
		if v.Name == volume {
			return v.Backup
		}
	}
	return nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
