package backup

// postgres_recovery.go proves that a backed-up pgBackRest repository can bring
// a real Postgres back, as part of the verified-restore drill.
//
// Everything else the drill checks is about files: the restic repository is
// internally consistent, the expected paths came back, the data directory's
// control file parses. None of that answers the only question that matters on
// the day it matters, which is whether the database starts. A pgBackRest
// repository can restore cleanly and still fail to recover — a gap in the WAL
// archive, a stanza whose last full backup aged out from under its
// incrementals, a version mismatch between the client that wrote it and the
// server that must read it. Each of those is invisible to a file-level check
// and fatal to a recovery.
//
// So the drill does the recovery. It restores the repository into a throwaway
// data directory inside a container built from the same image production runs,
// starts Postgres, waits for recovery to finish, and asks the database a
// question. Production is never touched: the repository is a restic-restored
// copy in a temporary directory, the container has no network, and Postgres
// listens on nothing but a Unix socket inside it.
//
// The restored repository is a plain directory, which makes this much simpler
// than the production topology — no SSH, no repository host, just
// repo1-path=/repo.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/schema"
)

// PostgresRecovery describes how to prove a restored pgBackRest repository
// recoverable. The daemon fills it in from ownbase.yaml; a zero value disables
// the check.
type PostgresRecovery struct {
	// Image is the container image to recover into. Use the Postgres service's
	// own built image, so the drill exercises the exact binaries production
	// runs rather than a stand-in that might differ in version.
	Image string

	// SuperUser is the cluster's bootstrap superuser — the POSTGRES_USER the
	// service was created with. It cannot be discovered from the restored
	// files, and it is not necessarily "postgres": a cluster initialised with
	// POSTGRES_USER=revolve has no "postgres" role at all, so connecting as
	// one would fail with "role does not exist" and look like a corrupt
	// backup.
	SuperUser string
}

// Configured reports whether enough is known to attempt a recovery.
func (p PostgresRecovery) Configured() bool {
	return strings.TrimSpace(p.Image) != ""
}

// FindPostgresRecovery locates the service that pushes to a pgBackRest
// repository host and describes how to recover its backups.
//
// The signal is PGBACKREST_HOST in the service's env: — a service that names a
// repository host is, by construction, a Postgres that archives to it, and its
// own built image therefore carries both the server and the pgBackRest client.
// That is deliberately narrower than "a service called postgres": the drill
// must recover with the same binaries production writes with, and guessing at
// the service by name would happily pick one that cannot.
//
// Returns a zero value (Configured() == false) when no such service exists, or
// when Postgres verification is switched off via core.backup.verify_postgres.
func FindPostgresRecovery(oc *schema.OwnbaseConfig) PostgresRecovery {
	if oc == nil || !oc.Core.Backup.PostgresVerifyEnabled() {
		return PostgresRecovery{}
	}
	for _, name := range sortedServiceNames(oc) {
		svc := oc.Services[name]
		if envValue(svc.Env, "PGBACKREST_HOST") == "" {
			continue
		}
		return PostgresRecovery{
			Image:     fmt.Sprintf("localhost/ownbase-%s:local", name),
			SuperUser: envValue(svc.Env, "POSTGRES_USER"),
		}
	}
	return PostgresRecovery{}
}

// envValue returns the value of key in a list of "KEY=VALUE" entries, or "".
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(e, prefix))
		}
	}
	return ""
}

func (p PostgresRecovery) superUser() string {
	if u := strings.TrimSpace(p.SuperUser); u != "" {
		return u
	}
	return "postgres"
}

// postgresRecoveryTimeout bounds the whole recovery: restore, start, replay,
// and query. Generous, because a large repository takes real time to restore,
// but bounded because a recovery that has not finished in this long is not
// going to.
const postgresRecoveryTimeout = 30 * time.Minute

// recoveryReadyTimeout bounds the wait for pg_is_in_recovery() to go false.
//
// pg_ctl -w is not sufficient on its own: it returns once the postmaster
// accepts connections, which happens while WAL replay is still running. A drill
// that trusted it would query the database mid-recovery and report a
// half-replayed cluster as a success.
const recoveryReadyTimeout = 10 * time.Minute

// pgBackRestRepo is a pgBackRest repository found inside a restored tree.
type pgBackRestRepo struct {
	// Path is the repository root — the directory holding backup/ and archive/.
	Path string

	// Stanza is the stanza name found under backup/.
	Stanza string
}

// findPGBackRestRepo locates a pgBackRest repository in a restored tree by
// looking for a stanza's backup.info, which is the file pgBackRest itself uses
// to identify a repository. Returns the first one found.
func findPGBackRestRepo(restoreDir string) *pgBackRestRepo {
	var found *pgBackRestRepo
	_ = filepath.WalkDir(restoreDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "backup.info" {
			return nil
		}
		// .../<repo>/backup/<stanza>/backup.info
		stanzaDir := filepath.Dir(path)
		backupDir := filepath.Dir(stanzaDir)
		if filepath.Base(backupDir) != "backup" {
			return nil
		}
		found = &pgBackRestRepo{
			Path:   filepath.Dir(backupDir),
			Stanza: filepath.Base(stanzaDir),
		}
		return filepath.SkipAll
	})
	return found
}

// checkPostgresRecovery restores repo into a throwaway Postgres and reports
// whether a live database came back.
func checkPostgresRecovery(ctx context.Context, cfg Config, repo pgBackRestRepo, target PostgresRecovery) CheckResult {
	const name = "postgres-recovery"

	if _, err := exec.LookPath("podman"); err != nil {
		// Nothing can be said about recoverability either way, so this is
		// skipped rather than failed. Failing it would peg Restorable at false
		// forever and teach operators to ignore the one flag meant to mean
		// something.
		return CheckResult{
			Name:   name,
			Passed: true,
			Detail: "skipped: podman is not available on this Base",
		}
	}

	pgData, err := os.MkdirTemp("", "ownbase-verify-pgdata-*")
	if err != nil {
		return CheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("create scratch data directory: %v", err)}
	}
	defer func() {
		if rmErr := os.RemoveAll(pgData); rmErr != nil {
			fmt.Fprintf(os.Stderr, "verify: cleanup %s: %v\n", pgData, rmErr)
		}
	}()

	cfg.progressf("    recovering stanza %q from the restored repository into a throwaway Postgres\n", repo.Stanza)

	runCtx, cancel := context.WithTimeout(ctx, postgresRecoveryTimeout)
	defer cancel()

	// --network none: the recovered database must not be reachable, and the
	//   restore needs no network since the repository is a local directory.
	// --user 0: the script takes ownership of the restored repository copy and
	//   the scratch directory before dropping to postgres, because the restored
	//   files carry whatever UIDs they had in the repository-host container.
	// --security-opt apparmor=unconfined: Podman's containers-default profile
	//   denies signals between a container's own processes, and Postgres dies
	//   with "could not signal for checkpoint: Permission denied" at the
	//   end-of-recovery checkpoint without this. It is the same exception the
	//   production Postgres service needs, for the same reason.
	args := []string{
		"run", "--rm",
		"--network", "none",
		"--user", "0",
		"--security-opt", "apparmor=unconfined",
		"--shm-size", "256m",
		"-v", repo.Path + ":/repo",
		"-v", pgData + ":/pgdata",
		"-e", "OWNBASE_STANZA=" + repo.Stanza,
		"-e", "OWNBASE_SUPERUSER=" + target.superUser(),
		"-e", fmt.Sprintf("OWNBASE_RECOVERY_WAIT_SECONDS=%d", int(recoveryReadyTimeout.Seconds())),
		"--entrypoint", "/bin/bash",
		target.Image,
		"-c", postgresRecoveryScript,
	}

	out, runErr := exec.CommandContext(runCtx, "podman", args...).CombinedOutput()
	output := string(out)

	// The script's last word is the verdict. Parsing it rather than trusting
	// the exit status alone means a container that died between a successful
	// recovery and a clean shutdown is not misreported as a failure.
	if summary, ok := parseRecoverySummary(output); ok {
		return CheckResult{Name: name, Passed: true, Detail: summary}
	}

	detail := summarizeRecoveryFailure(output)
	if runErr != nil {
		if runCtx.Err() != nil {
			detail = fmt.Sprintf("recovery did not finish within %s: %s", postgresRecoveryTimeout, detail)
		} else {
			detail = fmt.Sprintf("%v: %s", runErr, detail)
		}
	}
	return CheckResult{Name: name, Passed: false, Detail: detail}
}

// recoveryOKPrefix marks the script's success line.
const recoveryOKPrefix = "OWNBASE_RECOVERY_OK "

// parseRecoverySummary extracts the script's success line, if it emitted one.
func parseRecoverySummary(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, recoveryOKPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, recoveryOKPrefix)), true
		}
	}
	return "", false
}

// summarizeRecoveryFailure picks the most useful few lines out of what can be
// hundreds of lines of pgBackRest and Postgres logs. The whole log goes to the
// drill's progress stream; this is what lands in the one-line check detail an
// operator reads first, and in the daemon's log for a scheduled run.
func summarizeRecoveryFailure(output string) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "OWNBASE_RECOVERY_FAILED"):
			// The script's own diagnosis, which is more specific than
			// anything guessable from the surrounding log.
			return strings.TrimSpace(strings.TrimPrefix(line, "OWNBASE_RECOVERY_FAILED"))
		case strings.Contains(line, "ERROR:"),
			strings.Contains(line, "FATAL:"),
			strings.Contains(line, "PANIC:"):
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return "recovery produced no error, but never reported a live database"
	}
	// The first error is the cause; later ones are usually its consequences.
	if len(kept) > 3 {
		kept = kept[:3]
	}
	return strings.Join(kept, " | ")
}

// postgresRecoveryScript restores the stanza and proves the result is a live
// database. It runs as root inside the throwaway container.
//
// Written as a script rather than a sequence of podman exec calls because the
// container must not outlive the check: a single `podman run --rm` cannot leak
// a container if the daemon is killed mid-drill.
const postgresRecoveryScript = `
set -uo pipefail

fail() { echo "OWNBASE_RECOVERY_FAILED $*"; exit 1; }

# The restored repository copy carries the UIDs it had on the repository host,
# and the scratch directory is owned by whoever created it. Both must belong to
# postgres before it can read the backup or initialise the data directory.
# Safe to rewrite: /repo is a throwaway restic restore, not the live repository.
chown -R postgres:postgres /repo /pgdata || fail "cannot take ownership of the restored repository"
chmod 0700 /pgdata

install -d -o postgres -g postgres /tmp/ownbase-verify

# A config of our own rather than the image's: the baked one points repo1-host
# at the production repository host over SSH, which is exactly what this
# recovery must not depend on.
cat > /tmp/ownbase-verify/pgbackrest.conf <<CONF
[global]
repo1-path=/repo
log-level-console=detail
log-level-file=off
log-path=/tmp/ownbase-verify

[global:restore]
# Recorded verbatim into the restored postgresql.auto.conf as restore_command,
# so it must be the wrapper that strips this image's PGBACKREST_* variables.
cmd=/usr/local/bin/pgbackrest

[${OWNBASE_STANZA}]
pg1-path=/pgdata
CONF
chown postgres:postgres /tmp/ownbase-verify/pgbackrest.conf

echo "==> pgbackrest restore (stanza ${OWNBASE_STANZA})"
# No --type/--target: recover to the end of the WAL archive. A target time
# beyond the last archived segment fails with "recovery ended before configured
# recovery target was reached", which would read as a failed drill when the
# backup is in fact fine.
gosu postgres /usr/local/bin/pgbackrest \
    --config=/tmp/ownbase-verify/pgbackrest.conf \
    --stanza="${OWNBASE_STANZA}" \
    restore || fail "pgbackrest restore failed"

echo "==> starting Postgres on a Unix socket only"
# listen_addresses='': no TCP listener at all, so nothing can reach this
#   cluster even if the network namespace were shared.
# archive_mode=off: the restored postgresql.conf archives to pgBackRest, so a
#   promoted throwaway cluster would start pushing its own WAL into the
#   repository. Harmless against a restic-restored copy, but the drill has no
#   business writing to a backup repository under any circumstances.
gosu postgres pg_ctl -D /pgdata -l /tmp/ownbase-verify/postgres.log \
    -o "-c listen_addresses='' -c unix_socket_directories=/tmp/ownbase-verify -c archive_mode=off" \
    -w -t 300 start
start_status=$?
if [ -f /tmp/ownbase-verify/postgres.log ]; then
    tail -n 40 /tmp/ownbase-verify/postgres.log
fi
if [ "$start_status" -ne 0 ]; then
    fail "Postgres did not start from the restored data directory"
fi

psql_q() {
    gosu postgres psql -tAX --no-psqlrc \
        -h /tmp/ownbase-verify -U "${OWNBASE_SUPERUSER}" -d postgres \
        -c "$1" 2>/dev/null | tr -d '[:space:]'
}

# pg_ctl -w returns as soon as connections are accepted, which is well before
# WAL replay finishes. Waiting for pg_is_in_recovery() to go false is what
# actually proves the archive replayed and the cluster promoted.
echo "==> waiting for recovery to finish"
recovered=no
deadline=$(( $(date +%s) + OWNBASE_RECOVERY_WAIT_SECONDS ))
while [ "$(date +%s)" -lt "$deadline" ]; do
    case "$(psql_q 'select pg_is_in_recovery()')" in
        f) recovered=yes; break ;;
    esac
    sleep 2
done

if [ "$recovered" != yes ]; then
    tail -n 40 /tmp/ownbase-verify/postgres.log 2>/dev/null
    gosu postgres pg_ctl -D /pgdata -m immediate stop >/dev/null 2>&1
    fail "recovery never completed — pg_is_in_recovery() stayed true"
fi

# A cluster that is out of recovery but has no catalog is not a recovered
# database. Counting relations and databases is the cheapest question that
# cannot be answered by an empty or half-replayed cluster.
relations=$(psql_q "select count(*) from pg_class")
databases=$(psql_q "select count(*) from pg_database where not datistemplate")
timeline=$(psql_q "select timeline_id from pg_control_checkpoint()")

gosu postgres pg_ctl -D /pgdata -m fast -w -t 60 stop >/dev/null 2>&1

if [ -z "$relations" ] || [ "$relations" = 0 ]; then
    fail "recovered cluster has no relations — the restore produced an empty catalog"
fi

echo "OWNBASE_RECOVERY_OK recovered on timeline ${timeline}: ${databases} database(s), ${relations} relations"
`
