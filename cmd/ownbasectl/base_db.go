package main

// base_db.go implements `ownbasectl db status|restore <name>` — the Postgres
// point-in-time recovery surface.
//
// `backup status` answers "is the restic snapshot good". These answer the
// question underneath it: how far back can this database be recovered, is the
// window still moving, and — when it has to be — take it back to a point in
// time. Both run on the Base, because point-in-time recovery is `podman exec`
// and volume mounts, neither of which works from here.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Inspect and recover the Base's Postgres (status|restore)",
		Long: `Point-in-time recovery for the Base's Postgres.

'db status' shows what the pgBackRest repository holds and whether WAL
archiving is still working — the recovery window, and whether it is moving.

'db restore' takes the database back to a point in time, by default into a
scratch instance beside production so the result can be inspected before
anything is replaced.`,
	}
	cmd.AddCommand(
		newDBStatusCmd(),
		newDBRestoreCmd(),
	)
	return cmd
}

func newDBStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Show the Postgres recovery window, backups held, and archiver health",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBStatus(args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON instead of a formatted summary")
	return cmd
}

// dbStatus mirrors backup.DBStatus over the wire. Redeclared rather than
// imported so the CLI keeps working against a daemon that has since added
// fields, as every other status payload here does.
type dbStatus struct {
	Stanza          string `json:"stanza"`
	StanzaOK        bool   `json:"stanza_ok"`
	StanzaMessage   string `json:"stanza_message"`
	PostgresVersion string `json:"postgres_version"`
	Backups         []struct {
		Label         string    `json:"label"`
		Type          string    `json:"type"`
		SizeBytes     int64     `json:"size_bytes"`
		RepoSizeBytes int64     `json:"repo_size_bytes"`
		Started       time.Time `json:"started"`
		Stopped       time.Time `json:"stopped"`
		Error         bool      `json:"error"`
	} `json:"backups"`
	ArchiveMinWAL string `json:"archive_min_wal"`
	ArchiveMaxWAL string `json:"archive_max_wal"`
	Archiver      struct {
		ArchivedCount    int64     `json:"archived_count"`
		LastArchivedWAL  string    `json:"last_archived_wal"`
		LastArchivedTime time.Time `json:"last_archived_time"`
		FailedCount      int64     `json:"failed_count"`
		LastFailedWAL    string    `json:"last_failed_wal"`
		LastFailedTime   time.Time `json:"last_failed_time"`
	} `json:"archiver"`
	EarliestRecovery time.Time `json:"earliest_recovery"`
	LatestRecovery   time.Time `json:"latest_recovery"`
}

func runDBStatus(base string, jsonOut bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	// Reading the repository means running pgbackrest info inside the
	// container, which on a large repository is slower than a status call.
	body, err := apiCallWithTimeout(conn, http.MethodGet, "/db/status", nil, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("db status: %w", err)
	}
	if jsonOut {
		fmt.Println(string(body))
		return nil
	}

	var s dbStatus
	if err := json.Unmarshal(body, &s); err != nil {
		return fmt.Errorf("parse db status JSON: %w", err)
	}
	printDBStatus(base, s)
	return nil
}

func printDBStatus(base string, s dbStatus) {
	fmt.Println("─────────────────────── Postgres Recovery Status ───────────────────────")
	fmt.Printf("  Stanza:        %s", s.Stanza)
	if s.PostgresVersion != "" {
		fmt.Printf("  (PostgreSQL %s)", s.PostgresVersion)
	}
	fmt.Println()

	if s.StanzaOK {
		fmt.Println("  Repository:    ✓ ok")
	} else {
		fmt.Printf("  Repository:    ✗ %s\n", orElse(s.StanzaMessage, "not ok"))
	}

	fmt.Printf("  Backups held:  %d\n", len(s.Backups))
	for _, b := range s.Backups {
		marker := " "
		if b.Error {
			marker = "!"
		}
		// 33 characters is the width of an incremental label
		// ("<full>_<incr>"), so a mixed list still lines up.
		fmt.Printf("    %s %-33s %-4s %9s  %s\n", marker, b.Label, b.Type,
			humanBytes(b.RepoSizeBytes), localTime(b.Stopped))
	}

	// The recovery window is the answer to the question an operator actually
	// arrives with, so it is stated as a span rather than left to be inferred
	// from two timestamps.
	fmt.Println()
	if s.EarliestRecovery.IsZero() || s.LatestRecovery.IsZero() {
		fmt.Println("  Recovery window: unknown — no backup or no archived WAL yet")
	} else {
		fmt.Printf("  Recovery window: %s  →  %s\n",
			localTime(s.EarliestRecovery), localTime(s.LatestRecovery))
	}
	if s.ArchiveMinWAL != "" {
		fmt.Printf("  WAL archive:     %s → %s\n", s.ArchiveMinWAL, s.ArchiveMaxWAL)
	}

	fmt.Println()
	a := s.Archiver
	broken := a.FailedCount > 0 && (a.LastArchivedTime.IsZero() || a.LastFailedTime.After(a.LastArchivedTime))
	switch {
	case broken:
		// This is the failure worth interrupting for. The database keeps
		// serving queries while its recovery window silently stops moving, so
		// nothing else about the Base looks wrong.
		fmt.Printf("  Archiving:     ✗ FAILING — %d failures, last %s\n", a.FailedCount, localTime(a.LastFailedTime))
		if a.LastFailedWAL != "" {
			fmt.Printf("                 stuck on segment %s\n", a.LastFailedWAL)
		}
		fmt.Println("                 The database is fine, but the recovery window has stopped")
		fmt.Println("                 moving: changes since the last success cannot be recovered.")
		fmt.Printf("                 podman logs ownbase-pgbackrest   (on %s)\n", base)
	case a.ArchivedCount == 0:
		fmt.Println("  Archiving:     ⚠ nothing archived yet")
	default:
		fmt.Printf("  Archiving:     ✓ %d segments, last %s", a.ArchivedCount, localTime(a.LastArchivedTime))
		if a.FailedCount > 0 {
			fmt.Printf(" (%d earlier failures, since recovered)", a.FailedCount)
		}
		fmt.Println()
	}
	fmt.Println("────────────────────────────────────────────────────────────────────────")
	if !s.LatestRecovery.IsZero() {
		fmt.Println()
		fmt.Println("Recover to a point in time (into a scratch instance, production untouched):")
		fmt.Printf("  ownbasectl db restore %s --to %q\n", base, s.LatestRecovery.UTC().Format("2006-01-02 15:04:05+00"))
	}
}

func newDBRestoreCmd() *cobra.Command {
	var (
		target  string
		into    string
		port    int
		yes     bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "restore <name> [--to <timestamp>] [--into scratch|production]",
		Short: "Recover Postgres to a point in time (scratch by default)",
		Long: `Recover the Base's Postgres to a point in time from its pgBackRest repository.

--into scratch (the default) brings up a second Postgres on 127.0.0.1:5433 on
the Base and leaves it running to be inspected. Production keeps serving
throughout. This is how a recovery should normally start: look at what came
back before deciding to keep it.

--into production stops the database and its dependants, restores over the live
data directory with --delta, replays the archive, and takes a full backup on the
new timeline. It replaces the current database.

--to accepts "2006-01-02 15:04:05+00" and shorter forms; a timestamp without a
zone is read as UTC. It is checked against the repository first, because a
target past the end of the WAL archive fails with "recovery ended before
configured recovery target was reached", which reads like data loss and is not.
Omit --to to recover everything the repository holds.`,
		Example: `  # Look at yesterday's data without touching production
  ownbasectl db restore mybase --to "2026-07-25 14:00:00+00"

  # Take production back to just before a bad migration
  ownbasectl db restore mybase --to "2026-07-25 14:00:00+00" --into production`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBRestore(args[0], target, into, port, yes, jsonOut)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&target, "to", "", `recovery target, e.g. "2026-07-25 14:00:00+00" (default: everything the repository holds)`)
	fl.StringVar(&into, "into", "scratch", "where the restore lands: scratch or production")
	fl.IntVar(&port, "scratch-port", 0, "loopback port for the scratch instance (default 5433)")
	fl.BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt for --into production")
	fl.BoolVar(&jsonOut, "json", false, "print the raw JSON outcome instead of a formatted summary")
	return cmd
}

// dbRestoreOutcome mirrors backup.RestoreOutcome over the wire.
type dbRestoreOutcome struct {
	Into               string    `json:"into"`
	Target             time.Time `json:"target"`
	Timeline           string    `json:"timeline"`
	Databases          int       `json:"databases"`
	Relations          int       `json:"relations"`
	LastTransaction    string    `json:"last_transaction"`
	ScratchEndpoint    string    `json:"scratch_endpoint"`
	BackupAfterPromote bool      `json:"backup_after_promote"`
}

func runDBRestore(base, target, into string, scratchPort int, yes, jsonOut bool) error {
	switch into {
	case "scratch", "production":
	default:
		return fmt.Errorf("--into must be \"scratch\" or \"production\", not %q", into)
	}

	if into == "production" {
		what := "everything the repository holds"
		if target != "" {
			what = target
		}
		fmt.Printf("This replaces the live database on %s with its state at %s.\n", base, what)
		fmt.Println("The database and everything that depends on it go down for the restore.")
		fmt.Println("To look at the data first without replacing anything, use --into scratch.")
		if !confirm(fmt.Sprintf("Restore %s's database over production?", base), yes) {
			return errAborted
		}
	}

	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	payload, err := json.Marshal(map[string]any{"target": target, "into": into, "scratch_port": scratchPort})
	if err != nil {
		return err
	}

	outcome, err := streamDBRestore(conn, payload, jsonOut)
	if err != nil {
		return err
	}
	if !jsonOut {
		printDBRestoreOutcome(base, conn.sshTarget, outcome)
	}
	return nil
}

// streamDBRestore posts to /db/restore and streams its progress, mirroring how
// `checkup --verify` and `upgrade` consume their long-running endpoints.
//
// The daemon ends with a ---RESULT--- JSON trailer describing what came back,
// and ---OK--- only when the restore completed. Absence of ---OK--- is the
// failure signal: the stream itself carries the reason.
func streamDBRestore(conn *connection, payload []byte, jsonOut bool) (dbRestoreOutcome, error) {
	var outcome dbRestoreOutcome

	req, err := http.NewRequest(http.MethodPost, conn.baseURL+"/db/restore", strings.NewReader(string(payload)))
	if err != nil {
		return outcome, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if conn.token != "" {
		req.Header.Set("Authorization", "Bearer "+conn.token)
	}

	// A restore replays the whole WAL archive since the base backup, and a
	// production restore then takes a full backup, so this is bounded by
	// patience rather than by a typical request timeout.
	client := &http.Client{Timeout: 3 * time.Hour}
	resp, err := client.Do(req)
	if err != nil {
		return outcome, fmt.Errorf("db/restore API: %w\n  Is the agent running?", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return outcome, fmt.Errorf("unauthorized — the cached token may be stale; remove the profile and run 'ownbasectl adopt' again")
	case http.StatusNotImplemented:
		body, _ := io.ReadAll(resp.Body)
		return outcome, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	default:
		body, _ := io.ReadAll(resp.Body)
		return outcome, fmt.Errorf("db/restore returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var (
		gotOK      bool
		resultJSON string
		errLines   []string
	)
	scanner := bufio.NewScanner(resp.Body)
	// pgBackRest and Postgres both emit long lines, and a validation failure
	// is a whole paragraph; the default 64 KiB token limit would truncate it.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "---OK---":
			gotOK = true
		case strings.HasPrefix(line, "---RESULT---"):
			resultJSON = strings.TrimPrefix(line, "---RESULT---")
			_ = json.Unmarshal([]byte(resultJSON), &outcome)
		case strings.HasPrefix(line, "ERROR: ") || len(errLines) > 0:
			// The daemon's failures are multi-line: refusing a recovery target
			// takes a paragraph to explain what to do instead. Everything from
			// the ERROR: line on is collected and returned as the error rather
			// than printed here, so it appears once.
			errLines = append(errLines, strings.TrimPrefix(line, "ERROR: "))
		default:
			if !jsonOut {
				fmt.Println(line)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return outcome, err
	}

	if jsonOut && resultJSON != "" {
		fmt.Println(resultJSON)
	}
	if !gotOK {
		if msg := strings.TrimSpace(strings.Join(errLines, "\n")); msg != "" {
			return outcome, fmt.Errorf("%s", msg)
		}
		return outcome, fmt.Errorf("the restore did not complete — see the output above")
	}
	return outcome, nil
}

func printDBRestoreOutcome(base, sshTarget string, o dbRestoreOutcome) {
	fmt.Println()
	fmt.Println("  ✓ Recovery complete")
	if o.LastTransaction != "" {
		// The honest answer to "what am I looking at", which can be earlier
		// than the requested target when the repository held nothing newer.
		fmt.Printf("    Data as of:   %s (where replay stopped)\n", o.LastTransaction)
	} else if !o.Target.IsZero() {
		fmt.Printf("    Data as of:   %s\n", localTime(o.Target))
	}
	if o.Timeline != "" {
		fmt.Printf("    Timeline:     %s\n", o.Timeline)
	}
	fmt.Printf("    Recovered:    %d databases, %d relations\n", o.Databases, o.Relations)

	if o.Into == "production" {
		if o.BackupAfterPromote {
			fmt.Println("    Backup:       full backup taken on the new timeline")
		}
		fmt.Println()
		fmt.Printf("  The database is serving again. Check it with:\n    ownbasectl db status %s\n", base)
		return
	}

	endpoint := orElse(o.ScratchEndpoint, "127.0.0.1:5433")
	fmt.Println()
	fmt.Printf("  The scratch instance is listening on %s on the Base, and production\n", endpoint)
	fmt.Println("  was untouched. Forward the port to reach it from here:")
	fmt.Printf("    ssh -L %s:%s %s\n", portOf(endpoint), endpoint, orElse(sshTarget, "root@<base-host>"))
	fmt.Println()
	fmt.Println("  It is a container, so removing it is the complete teardown:")
	fmt.Println("    podman rm -f " + scratchContainerName)
}

// scratchContainerName duplicates backup.ScratchContainer for the teardown hint.
// The CLI does not import internal/backup for one string.
const scratchContainerName = "ownbase-db-scratch"

// localTime renders a timestamp in the operator's own zone, or "—" when unset.
func localTime(t time.Time) string {
	if t.IsZero() || t.Year() <= 1 {
		return "—"
	}
	local := t.Local()
	if local.Year() != time.Now().Year() {
		return local.Format("Jan 02 2006 15:04:05")
	}
	return local.Format("Jan 02 15:04:05")
}

// portOf returns the port from a host:port endpoint.
func portOf(endpoint string) string {
	if i := strings.LastIndex(endpoint, ":"); i >= 0 {
		return endpoint[i+1:]
	}
	return endpoint
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// humanBytes renders a byte count at the precision an operator reads sizes at.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value)
}
