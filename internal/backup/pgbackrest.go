package backup

// pgbackrest.go reports the state of a Base's Postgres point-in-time recovery:
// what backups are held, how far the WAL archive reaches, and whether archiving
// is still working.
//
// The last of those is the one worth having. A Postgres whose archive_command
// has started failing looks perfectly healthy from every other angle — it
// serves queries, its container is up, its disk is fine — while the window it
// can be recovered to quietly stops moving. pg_stat_archiver.failed_count is
// the only place that shows, and nothing surfaced it before.
//
// Everything here runs pgBackRest and psql *inside* the Postgres container, not
// on the host: that is where the client, the stanza configuration, and the SSH
// identity for the repository host live. An earlier version of this file
// assumed a host-installed pgbackrest and had no callers; it also polled
// archive-push from the daemon, which was never OwnBase's job — archiving is
// Postgres's own archive_command, driven by WAL volume and archive_timeout.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/schema"
)

// PGBackRest locates a Base's Postgres and its pgBackRest repository.
type PGBackRest struct {
	// Service is the ownbase.yaml key of the Postgres service.
	Service string

	// Stanza is the pgBackRest stanza name.
	Stanza string

	// SuperUser is the cluster's bootstrap superuser (POSTGRES_USER).
	SuperUser string

	// Database is the default database to connect to (POSTGRES_DB).
	Database string

	// RepoVolume is the Podman volume holding the pgBackRest repository, e.g.
	// "ownbase-pgbackrest-repo". A restore mounts it directly rather than going
	// through the repository host over SSH: the repository is on this machine,
	// and a restore that needs neither the network nor a key is one less thing
	// to fail when it is needed most.
	RepoVolume string

	// DataVolume is the Podman volume holding the live data directory, e.g.
	// "ownbase-postgres-data". Empty when the service declares no volume at its
	// data directory, which only a production restore cares about.
	DataVolume string

	// DataDir is where that volume is expected: PGBACKREST_PG1_PATH, then
	// PGDATA, then the image default. Kept so that failing to find the volume
	// can say which path was looked for rather than which one is usual.
	DataDir string

	// Dependants are the services that declare requires: on Service. A
	// production restore stops them first, because a client holding a
	// connection through a data-directory swap sees corruption, not an outage.
	Dependants []string
}

// Container is the Podman container name for the Postgres service.
func (p PGBackRest) Container() string { return "ownbase-" + p.Service }

// Unit is the systemd unit for the Postgres service.
func (p PGBackRest) Unit() string {
	return "ownbase-" + p.Service + ".service"

}

// database returns the database to connect to, defaulting to the superuser's
// own database as libpq does.
func (p PGBackRest) database() string {
	if d := strings.TrimSpace(p.Database); d != "" {
		return d
	}
	return "postgres"
}

// pgBackRestRepoMount is where the pgBackRest image keeps its repository. It
// identifies the repository volume among a repository host's volumes.
const pgBackRestRepoMount = "/var/lib/pgbackrest"

// DefaultPostgresDataDir is the data directory of the official Postgres image,
// and of the pgBackRest image built on it.
const DefaultPostgresDataDir = "/var/lib/postgresql/data"

// postgresDataDir is where a Postgres service keeps its data directory inside
// the container: whatever pgBackRest is pointed at, then PGDATA, then the image
// default. Read from configuration rather than assumed, because a Base that
// moves its data directory would otherwise have a production restore replace
// the wrong one — or, if the volume is missing, none at all.
// volumeMount pairs a Podman volume with where a service mounts it.
type volumeMount struct{ Volume, Mount string }

// serviceVolumeMounts lists a service's volumes as the compiler creates them,
// including the implicit "ownbase-<name>-data" that a service declaring no
// volumes: gets at its data_path. Reading the declaration directly would miss
// that one, which is the shape most Bases have.
func serviceVolumeMounts(name string, svc schema.ServiceDecl) []volumeMount {
	if len(svc.Volumes) == 0 {
		mount := svc.DataPath
		if mount == "" {
			mount = "/data"
		}
		return []volumeMount{{Volume: fmt.Sprintf("ownbase-%s-data", name), Mount: mount}}
	}
	out := make([]volumeMount, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		out = append(out, volumeMount{
			Volume: fmt.Sprintf("ownbase-%s-%s", name, v.Name),
			Mount:  v.Mount,
		})
	}
	return out
}

func postgresDataDir(svc schema.ServiceDecl) string {
	for _, key := range []string{"PGBACKREST_PG1_PATH", "PGDATA"} {
		if v := envValue(svc.Env, key); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return DefaultPostgresDataDir
}

// FindPGBackRest locates the Postgres service that archives to a pgBackRest
// repository host, and the repository volume on that host.
//
// The signal is PGBACKREST_HOST in the service's env:, which names the
// repository host's *container*; the service key is that with the "ownbase-"
// prefix removed. Matching on configuration rather than on a service called
// "postgres" keeps this honest — a Base can call its database anything, and a
// service called postgres that archives nowhere has no PITR to report on.
func FindPGBackRest(oc *schema.OwnbaseConfig) (PGBackRest, error) {
	if oc == nil {
		return PGBackRest{}, fmt.Errorf("no configuration loaded")
	}

	for _, name := range sortedServiceNames(oc) {
		svc := oc.Services[name]
		host := envValue(svc.Env, "PGBACKREST_HOST")
		if host == "" {
			continue
		}

		out := PGBackRest{
			Service:   name,
			Stanza:    envValue(svc.Env, "PGBACKREST_STANZA"),
			SuperUser: envValue(svc.Env, "POSTGRES_USER"),
			Database:  envValue(svc.Env, "POSTGRES_DB"),
		}
		if out.Stanza == "" {
			out.Stanza = "main"
		}
		if out.SuperUser == "" {
			out.SuperUser = "postgres"
		}

		repoService := strings.TrimPrefix(host, "ownbase-")
		repoDecl, ok := oc.Services[repoService]
		if !ok {
			return PGBackRest{}, fmt.Errorf("service %q sets PGBACKREST_HOST=%s, but no service %q exists to hold the repository",
				name, host, repoService)
		}
		for _, v := range repoDecl.Volumes {
			if v.Mount == pgBackRestRepoMount {
				out.RepoVolume = fmt.Sprintf("ownbase-%s-%s", repoService, v.Name)
				break
			}
		}
		if out.RepoVolume == "" {
			return PGBackRest{}, fmt.Errorf("service %q has no volume mounted at %s — cannot find the pgBackRest repository",
				repoService, pgBackRestRepoMount)
		}

		// The data directory is found by mount path, like the repository, since
		// a volume can be called anything. Not finding one is not an error
		// here: only a production restore replaces the data directory, and
		// `db status` has to keep working on a Base this does not fit.
		out.DataDir = postgresDataDir(svc)
		for _, vm := range serviceVolumeMounts(name, svc) {
			if strings.TrimRight(vm.Mount, "/") == out.DataDir {
				out.DataVolume = vm.Volume
				break
			}
		}

		for _, dep := range sortedServiceNames(oc) {
			if dep == name {
				continue
			}
			for _, req := range oc.Services[dep].Requires {
				if req == name {
					out.Dependants = append(out.Dependants, dep)
					break
				}
			}
		}
		return out, nil
	}

	return PGBackRest{}, fmt.Errorf("no service declares PGBACKREST_HOST — this Base has no Postgres with point-in-time recovery")
}

// DBStatus is the point-in-time recovery posture of one Postgres.
type DBStatus struct {
	Stanza          string     `json:"stanza"`
	StanzaOK        bool       `json:"stanza_ok"`
	StanzaMessage   string     `json:"stanza_message,omitempty"`
	PostgresVersion string     `json:"postgres_version,omitempty"`
	Backups         []DBBackup `json:"backups,omitempty"`

	// ArchiveMinWAL and ArchiveMaxWAL bound the WAL archive by segment name.
	ArchiveMinWAL string `json:"archive_min_wal,omitempty"`
	ArchiveMaxWAL string `json:"archive_max_wal,omitempty"`

	// Archiver is Postgres's own view of whether archiving is working.
	Archiver DBArchiver `json:"archiver"`

	// EarliestRecovery is the oldest point that can be recovered to: the end of
	// the oldest backup still held.
	EarliestRecovery time.Time `json:"earliest_recovery,omitempty"`

	// LatestRecovery is the newest point that can be recovered to. Bounded by
	// the last WAL segment successfully archived, not by now(): a change
	// committed after that segment is on disk but not yet in the repository.
	LatestRecovery time.Time `json:"latest_recovery,omitempty"`
}

// DBBackup is one pgBackRest backup.
type DBBackup struct {
	Label string `json:"label"`
	// Type is "full", "diff", or "incr".
	Type          string    `json:"type"`
	SizeBytes     int64     `json:"size_bytes"`
	RepoSizeBytes int64     `json:"repo_size_bytes"`
	Started       time.Time `json:"started"`
	Stopped       time.Time `json:"stopped"`
	Error         bool      `json:"error"`
}

// DBArchiver mirrors pg_stat_archiver.
type DBArchiver struct {
	ArchivedCount    int64     `json:"archived_count"`
	LastArchivedWAL  string    `json:"last_archived_wal,omitempty"`
	LastArchivedTime time.Time `json:"last_archived_time,omitempty"`

	// FailedCount is the one number worth watching. While it climbs, the
	// database is healthy and the recovery window is frozen.
	FailedCount    int64     `json:"failed_count"`
	LastFailedWAL  string    `json:"last_failed_wal,omitempty"`
	LastFailedTime time.Time `json:"last_failed_time,omitempty"`
}

// ArchivingBroken reports whether archiving has failed more recently than it
// has succeeded — the state in which the recovery window has stopped moving.
func (a DBArchiver) ArchivingBroken() bool {
	if a.FailedCount == 0 {
		return false
	}
	if a.LastFailedTime.IsZero() {
		return false
	}
	return a.LastArchivedTime.IsZero() || a.LastFailedTime.After(a.LastArchivedTime)
}

// QueryStatus reads the repository and Postgres's archiver statistics.
func QueryStatus(ctx context.Context, pb PGBackRest) (DBStatus, error) {
	status := DBStatus{Stanza: pb.Stanza}

	raw, err := pgBackRestInfo(ctx, pb)
	if err != nil {
		return status, err
	}
	applyRepoInfo(&status, raw)

	archiver, err := queryArchiver(ctx, pb)
	if err != nil {
		// The repository half is still worth reporting: a Postgres that is
		// down is exactly when an operator wants to know what can be restored.
		return status, fmt.Errorf("read pg_stat_archiver: %w", err)
	}
	status.Archiver = archiver
	if !archiver.LastArchivedTime.IsZero() {
		status.LatestRecovery = archiver.LastArchivedTime
	}
	return status, nil
}

// stanzaInfo mirrors the subset of `pgbackrest info --output=json` this uses.
type stanzaInfo struct {
	Name   string `json:"name"`
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	DB []struct {
		Version string `json:"version"`
	} `json:"db"`
	Archive []struct {
		Min string `json:"min"`
		Max string `json:"max"`
	} `json:"archive"`
	Backup []struct {
		Label string `json:"label"`
		Type  string `json:"type"`
		Error bool   `json:"error"`
		Info  struct {
			Size       int64 `json:"size"`
			Repository struct {
				Size int64 `json:"size"`
			} `json:"repository"`
		} `json:"info"`
		Timestamp struct {
			Start int64 `json:"start"`
			Stop  int64 `json:"stop"`
		} `json:"timestamp"`
	} `json:"backup"`
}

// pgBackRestInfo runs `pgbackrest info --output=json` inside the container.
func pgBackRestInfo(ctx context.Context, pb PGBackRest) (stanzaInfo, error) {
	out, err := exec.CommandContext(ctx, "podman", "exec", pb.Container(),
		"pgbackrest", "--stanza="+pb.Stanza, "--output=json", "info").Output()
	if err != nil {
		return stanzaInfo{}, fmt.Errorf("pgbackrest info in %s: %w", pb.Container(), err)
	}

	var stanzas []stanzaInfo
	if err := json.Unmarshal(out, &stanzas); err != nil {
		return stanzaInfo{}, fmt.Errorf("parse pgbackrest info: %w", err)
	}
	for _, s := range stanzas {
		if s.Name == pb.Stanza {
			return s, nil
		}
	}
	return stanzaInfo{}, fmt.Errorf("stanza %q not found in the repository", pb.Stanza)
}

// applyRepoInfo folds pgBackRest's own view into a DBStatus.
func applyRepoInfo(status *DBStatus, raw stanzaInfo) {
	status.StanzaOK = raw.Status.Code == 0
	status.StanzaMessage = raw.Status.Message
	if len(raw.DB) > 0 {
		status.PostgresVersion = raw.DB[0].Version
	}
	if len(raw.Archive) > 0 {
		status.ArchiveMinWAL = raw.Archive[0].Min
		status.ArchiveMaxWAL = raw.Archive[0].Max
	}
	for _, b := range raw.Backup {
		status.Backups = append(status.Backups, DBBackup{
			Label:         b.Label,
			Type:          b.Type,
			SizeBytes:     b.Info.Size,
			RepoSizeBytes: b.Info.Repository.Size,
			Started:       time.Unix(b.Timestamp.Start, 0).UTC(),
			Stopped:       time.Unix(b.Timestamp.Stop, 0).UTC(),
			Error:         b.Error,
		})
	}
	// Recovery cannot reach further back than the end of the oldest backup
	// still held: WAL before it has nothing to be applied to.
	if len(status.Backups) > 0 {
		status.EarliestRecovery = status.Backups[0].Stopped
	}
}

// archiverQuery reads pg_stat_archiver as a single tab-separated row, which is
// simpler to parse reliably than JSON from psql.
const archiverQuery = `select archived_count, coalesce(last_archived_wal,''),
	coalesce(to_char(last_archived_time,'YYYY-MM-DD"T"HH24:MI:SSOF'),''),
	failed_count, coalesce(last_failed_wal,''),
	coalesce(to_char(last_failed_time,'YYYY-MM-DD"T"HH24:MI:SSOF'),'')
	from pg_stat_archiver`

// queryArchiver reads Postgres's archiver statistics from inside the container.
func queryArchiver(ctx context.Context, pb PGBackRest) (DBArchiver, error) {
	out, err := exec.CommandContext(ctx, "podman", "exec", pb.Container(),
		"psql", "-tAX", "--no-psqlrc", "-F", "\t",
		"-U", pb.SuperUser, "-d", pb.database(),
		"-c", archiverQuery).Output()
	if err != nil {
		return DBArchiver{}, fmt.Errorf("psql in %s: %w", pb.Container(), err)
	}

	return parseArchiverRow(string(out))
}

// parseArchiverRow parses the single tab-separated row archiverQuery returns.
func parseArchiverRow(out string) (DBArchiver, error) {
	// Only the line ending is trimmed. A cluster that has never failed to
	// archive returns empty trailing fields, and trimming whitespace generally
	// would take the tabs that delimit them with it — leaving four fields where
	// six were expected, in exactly the healthy case.
	row := strings.Trim(out, "\r\n")
	fields := strings.Split(row, "\t")
	if len(fields) < 6 {
		return DBArchiver{}, fmt.Errorf("unexpected pg_stat_archiver output %q", row)
	}
	archived, _ := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64)
	failed, _ := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
	return DBArchiver{
		ArchivedCount:    archived,
		LastArchivedWAL:  strings.TrimSpace(fields[1]),
		LastArchivedTime: parsePGTime(fields[2]),
		FailedCount:      failed,
		LastFailedWAL:    strings.TrimSpace(fields[4]),
		LastFailedTime:   parsePGTime(fields[5]),
	}, nil
}

// parsePGTime parses the to_char format used by archiverQuery. An unparseable
// or empty value yields the zero time, which every caller treats as "unknown".
func parsePGTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02T15:04:05-07", "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// pgBackRestTargetLayout is the timestamp format pgBackRest expects for
// --target, e.g. "2026-07-26 01:37:38+00".
const pgBackRestTargetLayout = "2006-01-02 15:04:05-07"

// FormatTarget renders t the way pgBackRest --target expects it.
func FormatTarget(t time.Time) string {
	return t.UTC().Format(pgBackRestTargetLayout)
}

// ParseTarget accepts the recovery-target timestamps an operator is likely to
// type. A bare timestamp with no zone is read as UTC rather than as the Base's
// local time, because a recovery target that silently means a different instant
// than the one written is the last place to be clever.
func ParseTarget(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty recovery target")
	}
	layouts := []string{
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05-0700",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q — use \"2006-01-02 15:04:05+00\"", s)
}

// ValidateTarget checks a recovery target against what the repository actually
// holds, and returns a message explaining the problem rather than letting
// Postgres fail hours into a restore.
//
// This exists because of one specific failure. A target past the end of the WAL
// archive does not produce "no data for that time" — it produces
//
//	FATAL: recovery ended before configured recovery target was reached
//
// which reads like the backup is broken and data is gone. Neither is true: the
// data is intact, the target is simply in the future as far as the repository is
// concerned. Archiving is driven by WAL volume and archive_timeout, so on a
// quiet database the newest recoverable point routinely trails now() by
// minutes, and "restore to right now" is the most natural thing to ask for.
func ValidateTarget(status DBStatus, target time.Time) error {
	if target.IsZero() {
		return nil
	}
	if len(status.Backups) == 0 {
		return fmt.Errorf("the repository holds no backups — nothing can be recovered yet")
	}

	if !status.EarliestRecovery.IsZero() && target.Before(status.EarliestRecovery) {
		return fmt.Errorf("%s is before the oldest backup still held (%s) — recovery cannot reach it.\n"+
			"  The oldest recoverable point is %s. Older backups have been pruned by the retention policy.",
			FormatTarget(target), FormatTarget(status.EarliestRecovery), FormatTarget(status.EarliestRecovery))
	}

	if !status.LatestRecovery.IsZero() && target.After(status.LatestRecovery) {
		msg := fmt.Sprintf("%s is newer than the last WAL segment in the repository (%s) — Postgres would replay "+
			"everything it has and then abort with \"recovery ended before configured recovery target was reached\", "+
			"which looks like data loss but is not.\n"+
			"  The newest recoverable point is %s",
			FormatTarget(target), FormatTarget(status.LatestRecovery), FormatTarget(status.LatestRecovery))
		if status.Archiver.LastArchivedWAL != "" {
			msg += fmt.Sprintf(" (segment %s)", status.Archiver.LastArchivedWAL)
		}
		msg += ".\n" +
			"  Postgres archives WAL when a segment fills or archive_timeout elapses, so the newest\n" +
			"  recoverable point normally trails now(). To bring it forward, force a segment switch:\n" +
			"    select pg_switch_wal();\n" +
			"  Or omit --to to recover everything the repository holds."
		return fmt.Errorf("%s", msg)
	}

	return nil
}
