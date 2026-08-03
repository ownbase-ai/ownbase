package backup

// pgbackrest_restore.go performs point-in-time recovery, either into a scratch
// instance alongside production or over production itself.
//
// Both paths restore from the repository volume mounted directly, rather than
// through the repository host over SSH. The repository is on this machine, and a
// restore that depends on neither the network nor a key is one less thing to
// fail at the moment it is needed most. The volume is mounted read-only, so a
// restore cannot damage the thing it is restoring from — including in the
// production case, where the operator is already having a bad day.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// RestoreTarget selects where a point-in-time restore lands.
type RestoreTarget string

const (
	// RestoreIntoScratch restores beside production, into a separate instance
	// on its own loopback port, and leaves it running to be inspected.
	// Production is untouched.
	RestoreIntoScratch RestoreTarget = "scratch"

	// RestoreIntoProduction restores over the live data directory.
	RestoreIntoProduction RestoreTarget = "production"
)

// RestoreOptions configures one point-in-time restore.
type RestoreOptions struct {
	// Target is the recovery target. Zero means recover everything the
	// repository holds.
	Target time.Time

	// Into selects scratch (default) or production.
	Into RestoreTarget

	// ScratchPort is the loopback port the scratch instance publishes on.
	// Zero means DefaultScratchPort.
	ScratchPort int

	// Progress, when non-nil, receives progress lines. A restore takes minutes
	// and its failure modes are all mid-flight, so this is how the operator
	// finds out where it got to.
	Progress io.Writer
}

// DefaultScratchPort is the loopback port a scratch restore publishes on.
// Deliberately not 5432: the point of a scratch restore is that production
// keeps serving on its own port throughout.
const DefaultScratchPort = 5433

// ScratchContainer is the name of the container a scratch restore leaves
// running. Fixed rather than random so a second restore replaces the first
// instead of accumulating instances nobody remembers to remove.
const ScratchContainer = "ownbase-db-scratch"

// scratchDataDir is where a scratch instance's data directory lives on the
// host. Under /var/lib rather than /tmp so it is not swept away by a
// tmpfiles cleanup while an operator is still looking at it.
const scratchDataDir = "/var/lib/ownbase/db-scratch"

// recoveryPollInterval is how often pg_is_in_recovery() is checked.
const recoveryPollInterval = 2 * time.Second

// recoveryStallTimeout is how long replay may sit at the same position before
// the wait gives up. It bounds a recovery that has stopped, not one that is
// merely long: the whole point of point-in-time recovery is that it can be
// asked to replay a lot.
const recoveryStallTimeout = 10 * time.Minute

// recoveryReportInterval is how often the replay position is printed while
// waiting. Silence for an hour is indistinguishable from a hang.
const recoveryReportInterval = 30 * time.Second

// RestoreOutcome describes what a restore produced.
type RestoreOutcome struct {
	// Into is where it landed.
	Into RestoreTarget `json:"into"`

	// Target is the recovery target that was requested, if any.
	Target time.Time `json:"target,omitempty"`

	// Timeline is the timeline the recovered cluster promoted onto.
	Timeline string `json:"timeline,omitempty"`

	// Databases and Relations describe the recovered catalog. Row counts are
	// deliberately absent: Postgres resets its statistics views during
	// recovery, so every table reads as empty regardless of its contents.
	Databases int `json:"databases"`
	Relations int `json:"relations"`

	// LastTransaction is the log time of the last transaction replayed, as
	// Postgres reported it. This is the honest answer to "what point am I
	// actually looking at", which can be earlier than Target when the
	// repository held nothing newer.
	LastTransaction string `json:"last_transaction,omitempty"`

	// ScratchEndpoint is where a scratch instance is listening, e.g.
	// "127.0.0.1:5433" on the Base.
	ScratchEndpoint string `json:"scratch_endpoint,omitempty"`

	// BackupAfterPromote reports whether the post-promote full backup ran.
	BackupAfterPromote bool `json:"backup_after_promote,omitempty"`
}

// Restore performs a point-in-time restore.
//
// The caller is expected to have validated opts.Target with ValidateTarget
// first; Restore also translates the specific Postgres failure that a bad
// target produces, since a restore can be started from something other than
// the CLI.
func Restore(ctx context.Context, pb PGBackRest, opts RestoreOptions) (RestoreOutcome, error) {
	if opts.Into == RestoreIntoProduction {
		return restoreIntoProduction(ctx, pb, opts)
	}
	return restoreIntoScratch(ctx, pb, opts)
}

func progressf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format, args...)
	}
}

// restoreIntoScratch brings up a second Postgres from the repository and leaves
// it running for inspection.
//
// The container's main process is Postgres itself, so the instance lives exactly
// as long as the container and `podman rm -f` is a complete teardown. Starting
// Postgres in the background and letting the script exit instead would leave
// podman waiting on a process nobody is watching.
func restoreIntoScratch(ctx context.Context, pb PGBackRest, opts RestoreOptions) (RestoreOutcome, error) {
	out := RestoreOutcome{Into: RestoreIntoScratch, Target: opts.Target}

	port := opts.ScratchPort
	if port == 0 {
		port = DefaultScratchPort
	}

	repoPath, err := PodmanVolumeResolver{}.Resolve(ctx, pb.RepoVolume)
	if err != nil {
		return out, err
	}

	// A previous scratch instance is replaced rather than added to.
	progressf(opts.Progress, "==> Removing any previous scratch instance\n")
	_ = exec.CommandContext(ctx, "podman", "rm", "-f", ScratchContainer).Run()
	if err := os.RemoveAll(scratchDataDir); err != nil {
		return out, fmt.Errorf("clear %s: %w", scratchDataDir, err)
	}
	if err := os.MkdirAll(scratchDataDir, 0o700); err != nil {
		return out, fmt.Errorf("create %s: %w", scratchDataDir, err)
	}

	progressf(opts.Progress, "==> Restoring stanza %q into a scratch instance on 127.0.0.1:%d\n", pb.Stanza, port)
	args := []string{
		"run", "-d", "--name", ScratchContainer,
		"--user", "0",
		"--security-opt", "apparmor=unconfined",
		"--shm-size", "256m",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"-v", repoPath + ":/repo:ro",
		"-v", scratchDataDir + ":/pgdata",
		"-e", "OWNBASE_STANZA=" + pb.Stanza,
		"-e", "OWNBASE_LISTEN=*",
	}
	if !opts.Target.IsZero() {
		args = append(args, "-e", "OWNBASE_TARGET="+FormatTarget(opts.Target))
	}
	args = append(args,
		"--entrypoint", "/bin/bash",
		scratchImage(pb),
		"-c", pgBackRestRestoreScript,
	)

	if runOut, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		return out, fmt.Errorf("start scratch instance: %w\n%s", err, runOut)
	}

	// Tear the instance down on failure. Leaving a half-recovered Postgres
	// running would be read as a successful restore by anyone who looked.
	succeeded := false
	defer func() {
		if !succeeded {
			_ = exec.Command("podman", "rm", "-f", ScratchContainer).Run()
		}
	}()

	if err := waitForRecovery(ctx, pb, ScratchContainer, opts); err != nil {
		return out, err
	}

	if err := describeRecovered(ctx, pb, ScratchContainer, &out); err != nil {
		return out, err
	}
	out.ScratchEndpoint = fmt.Sprintf("127.0.0.1:%d", port)
	succeeded = true
	return out, nil
}

// restoreIntoProduction restores over the live data directory.
//
// Postgres is stopped for the restore rather than restored around, and its
// dependants are stopped first: a client holding a connection through a
// data-directory swap sees corruption, not an outage. The restore itself runs in
// a one-shot container because the service's own container is, necessarily, not
// running at that point.
func restoreIntoProduction(ctx context.Context, pb PGBackRest, opts RestoreOptions) (RestoreOutcome, error) {
	out := RestoreOutcome{Into: RestoreIntoProduction, Target: opts.Target}

	// Checked before anything is resolved or stopped: this is the one path that
	// takes the Base down, so it fails while that is still free.
	if pb.DataVolume == "" {
		// Names the path that was actually looked for, which is not the default
		// on a Base that sets PGBACKREST_PG1_PATH or PGDATA — being told about
		// a directory you do not use is worse than not being told at all.
		return out, fmt.Errorf("service %q declares no volume mounted at its data directory (%s) — a production restore replaces that directory, so it has to be a volume ownbase manages; a scratch restore works either way",
			pb.Service, orElseString(pb.DataDir, DefaultPostgresDataDir))
	}

	repoPath, err := PodmanVolumeResolver{}.Resolve(ctx, pb.RepoVolume)
	if err != nil {
		return out, err
	}
	dataPath, err := PodmanVolumeResolver{}.Resolve(ctx, pb.DataVolume)
	if err != nil {
		return out, err
	}

	// Registered before the first thing is stopped, so a shutdown that fails
	// half-way still brings back what it already took down.
	back := newBringBack(pb, opts.Progress)
	defer back.all()

	for _, dep := range pb.Dependants {
		progressf(opts.Progress, "==> Stopping %s (depends on %s)\n", dep, pb.Service)
	}
	for _, unit := range dependantUnits(pb) {
		if err := systemctlUnit(ctx, "stop", unit); err != nil {
			return out, fmt.Errorf("stop dependant unit %s: %w", unit, err)
		}
	}

	progressf(opts.Progress, "==> Stopping %s\n", pb.Service)
	// Set before the call, not after: a stop that reports an error may still
	// have taken the database down.
	back.stoppedPostgres = true
	if err := systemctlUnit(ctx, "stop", pb.Unit()); err != nil {
		return out, fmt.Errorf("stop %s: %w", pb.Service, err)
	}

	progressf(opts.Progress, "==> Restoring stanza %q over the live data directory (--delta)\n", pb.Stanza)
	args := []string{
		"run", "--rm",
		"--user", "0",
		"--security-opt", "apparmor=unconfined",
		"-v", repoPath + ":/repo:ro",
		"-v", dataPath + ":/pgdata",
		"-e", "OWNBASE_STANZA=" + pb.Stanza,
		// --delta reuses the files already present instead of restoring the
		// whole cluster, which is the difference between minutes and hours on a
		// large database.
		"-e", "OWNBASE_DELTA=1",
		"-e", "OWNBASE_RESTORE_ONLY=1",
	}
	if !opts.Target.IsZero() {
		args = append(args, "-e", "OWNBASE_TARGET="+FormatTarget(opts.Target))
	}
	args = append(args,
		"--entrypoint", "/bin/bash",
		scratchImage(pb),
		"-c", pgBackRestRestoreScript,
	)

	runOut, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	streamOutput(opts.Progress, string(runOut))
	if err != nil {
		return out, fmt.Errorf("pgbackrest restore: %w", err)
	}

	progressf(opts.Progress, "==> Starting %s to replay the archive\n", pb.Service)
	// Cleared only once the start succeeds — the opposite of the stop above,
	// and for the same reason. A start that failed leaves the database down,
	// which is precisely when cleanup needs to try again.
	if err := systemctlUnit(ctx, "start", pb.Unit()); err != nil {
		return out, fmt.Errorf("start %s: %w", pb.Service, err)
	}
	back.stoppedPostgres = false

	if err := waitForRecovery(ctx, pb, pb.Container(), opts); err != nil {
		return out, err
	}
	if err := describeRecovered(ctx, pb, pb.Container(), &out); err != nil {
		return out, err
	}

	// The database is serving, so the outage ends here rather than after the
	// backup below, which takes as long as the database is large.
	back.dependants()

	// A promotion starts a new timeline, and no backup in the repository is on
	// it. Until a full backup is taken, this database has recovery history but
	// no base to recover from — the one moment where a Base that just proved it
	// could restore is least able to do it again.
	progressf(opts.Progress, "==> Taking a full backup on the new timeline\n")
	if err := takeFullBackup(ctx, pb); err != nil {
		// Not fatal: the database is up and serving, which was the point. But
		// say so clearly, because the gap it leaves is exactly the kind that
		// goes unnoticed until it matters.
		// Naming the pgBackRest command matters here: `ownbasectl backup run`
		// takes a restic snapshot, which copies a repository that has no base
		// backup on this timeline and would look like the gap was closed.
		progressf(opts.Progress, "    WARNING: the post-promote full backup failed: %v\n"+
			"    This database is running but has no base backup on its new timeline.\n"+
			"    Take one on the Base before relying on recovery:\n"+
			"      podman exec %s pgbackrest --stanza=%s --type=full backup\n",
			err, pb.Container(), pb.Stanza)
		return out, nil
	}
	out.BackupAfterPromote = true
	return out, nil
}

// scratchImage is the image a restore runs in: the Postgres service's own,
// so the restore uses the same pgBackRest and Postgres versions that wrote the
// repository.
func scratchImage(pb PGBackRest) string {
	return fmt.Sprintf("localhost/ownbase-%s:local", pb.Service)
}

// waitForRecovery blocks until pg_is_in_recovery() reports false.
//
// pg_ctl -w and systemd's readiness both return once the postmaster accepts
// connections, which happens while WAL replay is still running. Only this
// answers "is the recovery finished".
//
// The bound is on progress, not on total time. Replaying a fortnight of WAL
// into a large database is legitimately slow, and a wall-clock limit would
// abandon a recovery that was working — reporting a failure that is not one,
// while Postgres carries on replaying behind it. A replay position that has not
// moved for recoveryStallTimeout is the thing worth giving up on.
func waitForRecovery(ctx context.Context, pb PGBackRest, container string, opts RestoreOptions) error {
	progressf(opts.Progress, "==> Waiting for recovery to finish\n")

	var (
		position     string
		lastProgress = time.Now()
		lastReport   time.Time
	)

	for {
		if inRecovery, err := psqlScalar(ctx, pb, container, "select pg_is_in_recovery()"); err == nil && inRecovery == "f" {
			return nil
		}
		// A container that has exited will never leave recovery, and its log
		// holds the reason. Fail immediately rather than waiting out the stall.
		if !isContainerRunning(ctx, container) {
			return fmt.Errorf("%s", diagnoseRestoreFailure(ctx, pb, container))
		}

		if at, err := psqlScalar(ctx, pb, container, replayPositionQuery); err == nil && at != "" && at != position {
			position, lastProgress = at, time.Now()
			// A long replay with no output reads as a hang, which is what
			// makes an operator kill it.
			if time.Since(lastReport) >= recoveryReportInterval {
				lastReport = time.Now()
				progressf(opts.Progress, "    replaying — %s\n", position)
			}
		}
		if stalled := time.Since(lastProgress); stalled > recoveryStallTimeout {
			return fmt.Errorf("recovery has not advanced in %s (last position %s) — the database is not replaying any more; check 'podman logs %s' on the Base",
				recoveryStallTimeout, orUnknown(position), container)
		}

		select {
		case <-time.After(recoveryPollInterval):
		case <-ctx.Done():
			return fmt.Errorf("gave up waiting for recovery: %w — Postgres may still be replaying; check 'podman logs %s' on the Base",
				ctx.Err(), container)
		}
	}
}

// replayPositionQuery reports how far replay has got. Both values are NULL
// outside recovery, which is not a state this is asked about.
const replayPositionQuery = `select coalesce(pg_last_wal_replay_lsn()::text,'') ||
	coalesce(' (' || to_char(pg_last_xact_replay_timestamp(),'YYYY-MM-DD HH24:MI:SSOF') || ')','')`

func orUnknown(s string) string {
	return orElseString(s, "unknown")
}

func orElseString(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// bringBack puts the Base back together after a production restore, whether it
// finished or failed part-way. A restore that fails is the case worth designing
// for — the whole Base is down at that moment — so the steps are fields rather
// than direct calls, and their ordering is tested rather than hoped for.
type bringBack struct {
	pb       PGBackRest
	progress io.Writer

	// stoppedPostgres records that the database was taken down and not yet
	// started again; startedDependants that its dependants are already back.
	stoppedPostgres   bool
	startedDependants bool

	startUnit func(unit string) error
	serving   func() bool
}

func newBringBack(pb PGBackRest, progress io.Writer) *bringBack {
	return &bringBack{
		pb:       pb,
		progress: progress,
		// Cleanup runs on its own context: the reason for being here is often
		// that the caller's was cancelled.
		startUnit: func(unit string) error { return systemctlUnit(context.Background(), "start", unit) },
		serving:   func() bool { return databaseServing(pb) },
	}
}

// dependants starts everything that requires the database, once. The restore
// calls it as soon as the database is serving, so the outage ends there rather
// than after the post-promote backup.
func (b *bringBack) dependants() {
	if b.startedDependants {
		return
	}
	b.startedDependants = true
	for _, dep := range b.pb.Dependants {
		progressf(b.progress, "==> Starting %s\n", dep)
	}
	for _, unit := range dependantUnits(b.pb) {
		if err := b.startUnit(unit); err != nil {
			fmt.Fprintf(os.Stderr, "db restore: start dependant unit %s: %v\n", unit, err)
		}
	}
}

// all is what the restore defers: the database first, then the services that
// need it, in reverse of the order they were stopped.
func (b *bringBack) all() {
	if b.startedDependants && !b.stoppedPostgres {
		return // the restore finished and brought everything back itself
	}
	if b.stoppedPostgres {
		b.stoppedPostgres = false
		progressf(b.progress, "==> Starting %s\n", b.pb.Service)
		if err := b.startUnit(b.pb.Unit()); err != nil {
			fmt.Fprintf(os.Stderr, "db restore: start %s: %v\n", b.pb.Service, err)
		}
	}
	// Dependants only once the database can actually answer them. Starting an
	// app against a database that is down — or still read-only, part-way
	// through replay — is a crash loop rather than a recovery, and it buries
	// which of the two is actually broken.
	if b.serving() {
		b.dependants()
		return
	}
	if !b.startedDependants && len(b.pb.Dependants) > 0 {
		progressf(b.progress, "    %s is not serving, so its dependants are being left stopped rather\n"+
			"    than started against a database that cannot answer them. Once it is back:\n"+
			"      systemctl start %s\n", b.pb.Service, strings.Join(dependantUnits(b.pb), " "))
	}
}

// databaseServing reports whether Postgres is up and out of recovery — the only
// state in which starting its dependants brings the Base back rather than
// pointing every app at a database that cannot answer.
//
// Uses its own context: this runs while cleaning up, which is often precisely
// because the caller's context is done.
func databaseServing(pb PGBackRest) bool {
	ctx, cancel := context.WithTimeout(context.Background(), servingProbeTimeout)
	defer cancel()
	inRecovery, err := psqlScalar(ctx, pb, pb.Container(), "select pg_is_in_recovery()")
	return err == nil && inRecovery == "f"
}

// servingProbeTimeout bounds the "is it back" probe during cleanup.
const servingProbeTimeout = 15 * time.Second

// dependantUnits names every unit that must stop/start for Dependants —
// all replica instances when DependantUnits is populated, otherwise the
// unreplicated ownbase-<dep>.service names (hand-built tests).
func dependantUnits(pb PGBackRest) []string {
	if len(pb.DependantUnits) > 0 {
		return append([]string(nil), pb.DependantUnits...)
	}
	units := make([]string, 0, len(pb.Dependants))
	for _, dep := range pb.Dependants {
		units = append(units, "ownbase-"+dep+".service")
	}
	return units
}

// describeRecovered asks the recovered database what it is.
func describeRecovered(ctx context.Context, pb PGBackRest, container string, out *RestoreOutcome) error {
	out.Timeline, _ = psqlScalar(ctx, pb, container, "select timeline_id from pg_control_checkpoint()")

	relations, err := psqlScalar(ctx, pb, container, "select count(*) from pg_class")
	if err != nil {
		return fmt.Errorf("query the recovered database: %w", err)
	}
	out.Relations = atoiSafe(relations)
	if out.Relations == 0 {
		return fmt.Errorf("the recovered cluster has no relations — the restore produced an empty catalog")
	}
	databases, _ := psqlScalar(ctx, pb, container, "select count(*) from pg_database where not datistemplate")
	out.Databases = atoiSafe(databases)
	out.LastTransaction = lastReplayedTransaction(ctx, container)
	return nil
}

// takeFullBackup runs a full pgBackRest backup from inside the container.
func takeFullBackup(ctx context.Context, pb PGBackRest) error {
	out, err := exec.CommandContext(ctx, "podman", "exec", pb.Container(),
		"pgbackrest", "--stanza="+pb.Stanza, "--type=full", "backup").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, lastLines(string(out), 5))
	}
	return nil
}

// psqlScalar runs a single-value query inside a container.
func psqlScalar(ctx context.Context, pb PGBackRest, container, query string) (string, error) {
	out, err := exec.CommandContext(ctx, "podman", "exec", container,
		"psql", "-tAX", "--no-psqlrc", "-U", pb.SuperUser, "-d", pb.database(),
		"-c", query).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func isContainerRunning(ctx context.Context, container string) bool {
	out, err := exec.CommandContext(ctx, "podman", "inspect",
		"--format={{.State.Running}}", container).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// recoveryTargetMissedLog is what Postgres logs when the recovery target is
// newer than anything in the archive.
const recoveryTargetMissedLog = "recovery ended before configured recovery target was reached"

// diagnoseRestoreFailure turns a stopped container's log into an explanation.
func diagnoseRestoreFailure(ctx context.Context, pb PGBackRest, container string) string {
	log := restoreLog(ctx, pb, container)

	if strings.Contains(log, recoveryTargetMissedLog) {
		msg := "the recovery target is newer than the last change in the repository, so Postgres replayed " +
			"everything it had and refused to start. The backup is intact and no data is lost"
		if last := extractLastTransaction(log); last != "" {
			msg += fmt.Sprintf("; the newest change in the repository is from %s", last)
		}
		return msg + ".\n" +
			"  Re-run with an earlier --to, or omit --to to recover everything the repository holds."
	}
	if line := firstErrorLine(log); line != "" {
		return fmt.Sprintf("the restore stopped: %s", line)
	}
	return fmt.Sprintf("the restore stopped without completing recovery; check 'podman logs %s' on the Base", container)
}

// restoreLog reads the log of whatever was replaying WAL.
//
// A service container that fails to start is removed and recreated by systemd,
// so by the time the failure is noticed `podman logs` has nothing to say and the
// reason is only in the journal. Without this fallback a production restore that
// failed at startup reported "check podman logs" — against a container that no
// longer existed.
func restoreLog(ctx context.Context, pb PGBackRest, container string) string {
	if log := containerLog(ctx, container); strings.TrimSpace(log) != "" {
		return log
	}
	if container != pb.Container() {
		return ""
	}
	args := []string{"-u", pb.Unit(), "--no-pager", "-n", "200"}
	if os.Geteuid() != 0 {
		args = append([]string{"--user"}, args...)
	}
	out, _ := exec.CommandContext(ctx, "journalctl", args...).CombinedOutput()
	return string(out)
}

// lastReplayedTransaction reports where WAL replay actually stopped, which is
// the honest answer to what point the recovered data represents — and can be
// earlier than the requested target when the repository held nothing newer.
func lastReplayedTransaction(ctx context.Context, container string) string {
	return extractRecoveryStop(containerLog(ctx, container))
}

// recoveryStoppingLog is what Postgres logs when it reaches a recovery target:
// "recovery stopping before commit of transaction 942, time 2026-07-26 01:37:33+00".
const recoveryStoppingLog = "recovery stopping "

// extractRecoveryStop pulls the timestamp out of Postgres's "recovery stopping"
// line, falling back to the "last completed transaction" line that a *failed*
// recovery logs instead.
func extractRecoveryStop(log string) string {
	idx := strings.LastIndex(log, recoveryStoppingLog)
	if idx < 0 {
		return extractLastTransaction(log)
	}
	line := log[idx:]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	const timeMarker = ", time "
	if i := strings.Index(line, timeMarker); i >= 0 {
		return strings.TrimSpace(line[i+len(timeMarker):])
	}
	return extractLastTransaction(log)
}

const lastTransactionLog = "last completed transaction was at log time "

// extractLastTransaction pulls the timestamp out of Postgres's
// "last completed transaction was at log time ..." line.
func extractLastTransaction(log string) string {
	idx := strings.LastIndex(log, lastTransactionLog)
	if idx < 0 {
		return ""
	}
	rest := log[idx+len(lastTransactionLog):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.TrimSuffix(strings.TrimSpace(rest), ".")
}

func containerLog(ctx context.Context, container string) string {
	out, _ := exec.CommandContext(ctx, "podman", "logs", "--tail", "200", container).CombinedOutput()
	return string(out)
}

// firstErrorLine returns the first Postgres or pgBackRest error in a log, which
// is the cause; the lines after it are usually its consequences.
func firstErrorLine(log string) string {
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "ERROR:") || strings.Contains(line, "FATAL:") || strings.Contains(line, "PANIC:") {
			return line
		}
	}
	return ""
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// streamOutput forwards captured command output to a progress writer.
func streamOutput(w io.Writer, out string) {
	if w == nil || out == "" {
		return
	}
	fmt.Fprint(w, out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Fprintln(w)
	}
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// systemctlUnit runs a systemctl verb against a unit, using the user manager
// when not running as root.
func systemctlUnit(ctx context.Context, verb, unit string) error {
	args := []string{verb, unit}
	if os.Geteuid() != 0 {
		args = append([]string{"--user"}, args...)
	}
	if out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s %s: %w\n%s", verb, unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// pgBackRestRestoreScript restores a stanza and, unless OWNBASE_RESTORE_ONLY is
// set, becomes the recovered Postgres.
//
// Environment: OWNBASE_STANZA (required), OWNBASE_TARGET, OWNBASE_DELTA,
// OWNBASE_RESTORE_ONLY, OWNBASE_LISTEN.
const pgBackRestRestoreScript = `
set -uo pipefail
fail() { echo "OWNBASE_RESTORE_FAILED $*" >&2; exit 1; }

# The repository is mounted read-only and owned by the repository host's user
# (0750/0640), so postgres cannot read it. Joining that group is the only fix
# available: chowning is both wrong and impossible on a read-only mount, and
# running pgBackRest as the repository's owner would leave a data directory
# postgres cannot then start from.
REPO_GID=$(stat -c %g /repo) || fail "cannot stat the repository mount"
if [ "$REPO_GID" != 0 ]; then
    getent group "$REPO_GID" >/dev/null || groupadd -g "$REPO_GID" ownbase-repo || fail "cannot create the repository group"
    usermod -aG "$REPO_GID" postgres || fail "cannot grant postgres access to the repository"
fi

install -d -o postgres -g postgres -m 0700 /pgdata
install -d -o postgres -g postgres /tmp/ownbase-restore

# A configuration of our own: the image's points repo1-host at the production
# repository host over SSH, which is exactly what a restore should not need.
cat > /tmp/ownbase-restore/pgbackrest.conf <<CONF
[global]
repo1-path=/repo
lock-path=/tmp/ownbase-restore
log-level-console=detail
log-level-file=off
log-path=/tmp/ownbase-restore

[global:restore]
# Recorded verbatim into postgresql.auto.conf as restore_command, so it must be
# the wrapper that strips this image's PGBACKREST_* variables.
cmd=/usr/local/bin/pgbackrest

[${OWNBASE_STANZA}]
pg1-path=/pgdata
CONF
chown postgres:postgres /tmp/ownbase-restore/pgbackrest.conf

RESTORE_ARGS=()
if [ -n "${OWNBASE_TARGET:-}" ]; then
    RESTORE_ARGS+=(--type=time --target="${OWNBASE_TARGET}" --target-action=promote)
fi
if [ -n "${OWNBASE_DELTA:-}" ]; then
    RESTORE_ARGS+=(--delta)
fi
if [ -n "${OWNBASE_RESTORE_ONLY:-}" ]; then
    # pgBackRest records its own invocation as restore_command, --config and
    # all. For a production restore the replay happens later, in the service's
    # own container, where this container's /tmp does not exist — Postgres then
    # fails every archive-get with "unable to open missing file" and cannot get
    # past its first checkpoint record. Pin the recorded command to the
    # service's standard configuration, which is what should fetch the WAL.
    RESTORE_ARGS+=(--recovery-option="restore_command=/usr/local/bin/pgbackrest --stanza=${OWNBASE_STANZA} archive-get %f \"%p\"")
fi

echo "==> pgbackrest restore ${RESTORE_ARGS[*]:-(everything the repository holds)}"
gosu postgres /usr/local/bin/pgbackrest \
    --config=/tmp/ownbase-restore/pgbackrest.conf \
    --stanza="${OWNBASE_STANZA}" \
    "${RESTORE_ARGS[@]}" \
    restore || fail "pgbackrest restore failed"

if [ -n "${OWNBASE_RESTORE_ONLY:-}" ]; then
    echo "OWNBASE_RESTORE_OK data directory restored; the service will replay the archive on start"
    exit 0
fi

# Postgres becomes the container's own process, so the instance lives exactly as
# long as the container and 'podman rm -f' is a complete teardown. Starting it in
# the background instead leaves podman waiting on a process nobody watches.
#
# archive_mode=off: the restored configuration archives to pgBackRest, and a
# scratch instance has no business writing into a backup repository.
echo "==> starting Postgres"
exec gosu postgres postgres -D /pgdata \
    -c listen_addresses="${OWNBASE_LISTEN:-*}" \
    -c archive_mode=off
`
