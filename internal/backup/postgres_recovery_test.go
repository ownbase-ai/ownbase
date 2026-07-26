package backup

// These tests live in the backup package (not backup_test) because the repo
// detection, failure summarisation, and the recovery script are unexported
// details of the drill.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
)

// writeRepoFixture lays out a pgBackRest repository the way it appears inside a
// restic restore: under the restored path of the repository host's volume.
func writeRepoFixture(t *testing.T, root, stanza string) string {
	t.Helper()
	repo := filepath.Join(root, "var", "lib", "containers", "storage",
		"volumes", "ownbase-pgbackrest-repo", "_data")
	for _, dir := range []string{
		filepath.Join(repo, "backup", stanza),
		filepath.Join(repo, "archive", stanza),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "backup", stanza, "backup.info"), []byte("[backrest]\n"), 0o644); err != nil {
		t.Fatalf("write backup.info: %v", err)
	}
	return repo
}

func TestFindPGBackRestRepo_FindsRepoAndStanza(t *testing.T) {
	root := t.TempDir()
	want := writeRepoFixture(t, root, "main")

	got := findPGBackRestRepo(root)
	if got == nil {
		t.Fatal("findPGBackRestRepo returned nil for a tree containing a repository")
	}
	if got.Path != want {
		t.Errorf("Path = %q, want %q", got.Path, want)
	}
	if got.Stanza != "main" {
		t.Errorf("Stanza = %q, want %q", got.Stanza, "main")
	}
}

func TestFindPGBackRestRepo_NoRepo(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "opt", "ownbase", "data"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := findPGBackRestRepo(root); got != nil {
		t.Errorf("findPGBackRestRepo = %+v, want nil for a tree with no repository", got)
	}
}

// A backup.info that is not under a backup/<stanza>/ directory is some other
// file that happens to share the name, not a stanza.
func TestFindPGBackRestRepo_IgnoresMisplacedBackupInfo(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "opt", "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup.info"), []byte("notes\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := findPGBackRestRepo(root); got != nil {
		t.Errorf("findPGBackRestRepo = %+v, want nil", got)
	}
}

func TestFindPostgresRecovery(t *testing.T) {
	yes := true
	no := false

	tests := []struct {
		name          string
		yaml          string
		wantImage     string
		wantSuperUser string
	}{
		{
			// The service that names a repository host is the one whose image
			// carries both the server and the pgBackRest client.
			name: "picks the service that archives to a repo host",
			yaml: `schema_version: v1
services:
  pgbackrest:
    repo: https://github.com/ownbase-ai/pgbackrest
  db:
    repo: https://github.com/ownbase-ai/pgbackrest
    env:
      - POSTGRES_USER=revolve
      - PGBACKREST_HOST=ownbase-pgbackrest
`,
			wantImage:     "localhost/ownbase-db:local",
			wantSuperUser: "revolve",
		},
		{
			// A cluster initialised without POSTGRES_USER has "postgres" as its
			// bootstrap superuser.
			name: "defaults the superuser when none is declared",
			yaml: `schema_version: v1
services:
  postgres:
    repo: https://github.com/ownbase-ai/pgbackrest
    env:
      - PGBACKREST_HOST=ownbase-pgbackrest
`,
			wantImage:     "localhost/ownbase-postgres:local",
			wantSuperUser: "",
		},
		{
			// Naming a service "postgres" is not enough: without a repository
			// host there is no pgBackRest client to recover with.
			name: "no repo host means nothing to recover with",
			yaml: `schema_version: v1
services:
  postgres:
    repo: https://github.com/docker-library/postgres
    env:
      - POSTGRES_USER=app
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := schema.ParseConfig(strings.NewReader(tc.yaml))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := FindPostgresRecovery(cfg)
			if got.Image != tc.wantImage {
				t.Errorf("Image = %q, want %q", got.Image, tc.wantImage)
			}
			if got.SuperUser != tc.wantSuperUser {
				t.Errorf("SuperUser = %q, want %q", got.SuperUser, tc.wantSuperUser)
			}
			if tc.wantImage == "" && got.Configured() {
				t.Error("Configured() = true with no image")
			}
		})
	}

	// verify_postgres: false opts out even when a recoverable service exists.
	withHost := `schema_version: v1
core:
  backup:
    repo: s3:s3.amazonaws.com/b/base
    verify_postgres: %s
services:
  postgres:
    repo: https://github.com/ownbase-ai/pgbackrest
    env:
      - PGBACKREST_HOST=ownbase-pgbackrest
`
	for _, tc := range []struct {
		value *bool
		want  bool
	}{{&yes, true}, {&no, false}, {nil, true}} {
		literal := "true"
		if tc.value == nil {
			literal = "" // omit the key entirely
		} else if !*tc.value {
			literal = "false"
		}
		yamlText := strings.Replace(withHost, "    verify_postgres: %s\n", "", 1)
		if literal != "" {
			yamlText = strings.Replace(withHost, "%s", literal, 1)
		}
		cfg, err := schema.ParseConfig(strings.NewReader(yamlText))
		if err != nil {
			t.Fatalf("parse (verify_postgres=%q): %v", literal, err)
		}
		if got := FindPostgresRecovery(cfg).Configured(); got != tc.want {
			t.Errorf("verify_postgres=%q: Configured() = %v, want %v", literal, got, tc.want)
		}
	}
}

func TestParseRecoverySummary(t *testing.T) {
	output := "==> pgbackrest restore\nsome log\n" +
		recoveryOKPrefix + "recovered on timeline 3: 2 database(s), 412 relations\n"
	got, ok := parseRecoverySummary(output)
	if !ok {
		t.Fatal("parseRecoverySummary did not find the success line")
	}
	if got != "recovered on timeline 3: 2 database(s), 412 relations" {
		t.Errorf("summary = %q", got)
	}

	if _, ok := parseRecoverySummary("==> pgbackrest restore\nERROR: no such stanza\n"); ok {
		t.Error("parseRecoverySummary reported success for a failed run")
	}
}

func TestSummarizeRecoveryFailure(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			// The script's own diagnosis beats anything inferable from the log.
			name:   "script diagnosis wins",
			output: "ERROR: something\nOWNBASE_RECOVERY_FAILED recovery never completed\n",
			want:   "recovery never completed",
		},
		{
			// The first error is the cause; later ones are its consequences.
			name: "keeps the first errors only",
			output: strings.Join([]string{
				"P00   ERROR: [055]: unable to load info file",
				"FATAL:  could not locate a valid checkpoint record",
				"FATAL:  the database system is starting up",
				"FATAL:  and another",
			}, "\n"),
			want: "P00   ERROR: [055]: unable to load info file | " +
				"FATAL:  could not locate a valid checkpoint record | " +
				"FATAL:  the database system is starting up",
		},
		{
			name:   "silent failure still says something",
			output: "==> pgbackrest restore\ncompleted\n",
			want:   "recovery produced no error, but never reported a live database",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeRecoveryFailure(tc.output); got != tc.want {
				t.Errorf("summarizeRecoveryFailure = %q, want %q", got, tc.want)
			}
		})
	}
}

// The recovery script only ever runs inside a container on a Base, so a syntax
// error in it would surface as a mystifying drill failure in production rather
// than at build time. Parse it here instead.
func TestPostgresRecoveryScript_IsValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(postgresRecoveryScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recovery script is not valid bash: %v\n%s", err, out)
	}
}

// Each of these is a production failure the script exists to avoid. A refactor
// that drops one would still pass a syntax check and still recover a small
// database on a developer's machine, then fail on a real Base.
func TestPostgresRecoveryScript_KeepsLoadBearingBehaviour(t *testing.T) {
	musts := map[string]string{
		"repo1-path=/repo":              "must restore from the local restored copy, not the production repo host over SSH",
		"pg_is_in_recovery":             "must wait out WAL replay; pg_ctl -w returns while recovery is still running",
		"listen_addresses=''":           "the recovered cluster must not be reachable over TCP",
		"cmd=/usr/local/bin/pgbackrest": "restore_command must use the env-scrubbing wrapper",
		"chown -R postgres:postgres":    "restored files carry the repo host's UIDs and are unreadable to postgres without this",
		"OWNBASE_SUPERUSER":             "the bootstrap superuser is not always \"postgres\" and cannot be read from the files",
		"archive_mode=off":              "a promoted throwaway cluster must never push its own WAL into a backup repository",
	}
	for fragment, why := range musts {
		if !strings.Contains(postgresRecoveryScript, fragment) {
			t.Errorf("recovery script no longer contains %q — %s", fragment, why)
		}
	}

	// A recovery target would reintroduce the "recovery ended before configured
	// recovery target was reached" failure, which reads like data loss and is
	// not. The drill recovers to the end of the archive instead.
	for _, forbidden := range []string{"--type=time", "--target="} {
		if strings.Contains(postgresRecoveryScript, forbidden) {
			t.Errorf("recovery script uses %q — the drill must recover to the end of the archive", forbidden)
		}
	}
}
