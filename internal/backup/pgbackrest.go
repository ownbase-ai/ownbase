package backup

// pgbackrest.go implements Postgres point-in-time recovery support via
// pgBackRest. This is conditional: the functions are no-ops when pgBackRest
// is not installed or no Postgres container is running.
//
// pgBackRest decision (M6): chosen over wal-g for its richer PITR UI,
// explicit stanza model (maps cleanly to OwnBase service names), and broader
// community support on Ubuntu.
//
// V1 scope: WAL archiving from a running Postgres container + restore to a
// specific time. Full pgBackRest setup (stanza create, archive-push) requires
// an existing Postgres service in ownbase.yaml; these functions are a no-op
// when Postgres is not present.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PGBackRestConfig configures pgBackRest for one Postgres service.
type PGBackRestConfig struct {
	// StanzaName is the pgBackRest stanza (maps to the service name, e.g. "postgres").
	StanzaName string

	// ContainerName is the name of the Podman container running Postgres.
	ContainerName string

	// BackupPath is where pgBackRest stores its backups.
	// Default: /opt/ownbase/data/pgbackrest/<stanza>.
	BackupPath string

	// PGDataPath is the Postgres data directory inside the container.
	// Default: /var/lib/postgresql/data.
	PGDataPath string
}

// PGBackRestAvailable returns true when pgBackRest is installed.
func PGBackRestAvailable() bool {
	_, err := exec.LookPath("pgbackrest")
	return err == nil
}

// PGBackRestArchiveWAL triggers a WAL segment archive from the running
// Postgres container. Used after each backup cycle to keep PITR current.
// No-op when pgBackRest is not installed or the container is not running.
func PGBackRestArchiveWAL(ctx context.Context, cfg PGBackRestConfig) error {
	if !PGBackRestAvailable() {
		return nil // pgBackRest not installed; skip silently
	}
	if !containerRunning(ctx, cfg.ContainerName) {
		return nil // Postgres container not running; skip silently
	}

	// Run pgbackrest --stanza=<name> archive-push inside the container.
	out, err := exec.CommandContext(ctx,
		"podman", "exec", cfg.ContainerName,
		"pgbackrest", "--stanza="+cfg.StanzaName, "archive-push",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pgbackrest archive-push: %w\n%s", err, out)
	}
	return nil
}

// PGBackRestPITR restores Postgres data to a specific point in time.
// The targetTime is in RFC 3339 format ("2006-01-02T15:04:05Z07:00").
// This function must be called on a stopped Postgres instance.
func PGBackRestPITR(ctx context.Context, cfg PGBackRestConfig, targetTime string) error {
	if !PGBackRestAvailable() {
		return fmt.Errorf("pgbackrest is not installed; cannot perform PITR")
	}

	args := []string{
		"--stanza=" + cfg.StanzaName,
		"--type=time",
		"--target=" + targetTime,
		"--target-action=promote",
		"restore",
	}
	out, err := exec.CommandContext(ctx, "pgbackrest", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pgbackrest restore --type=time: %w\n%s", err, out)
	}
	return nil
}

// containerRunning returns true when a named Podman container is in Running state.
func containerRunning(ctx context.Context, containerName string) bool {
	out, err := exec.CommandContext(ctx,
		"podman", "inspect", "--format={{.State.Running}}", containerName,
	).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}
