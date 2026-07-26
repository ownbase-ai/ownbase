package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// captureStdout collects what fn prints. The report functions write to stdout
// directly, as every other formatted report in this package does.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()

	fn()
	w.Close()
	return <-done
}

// The status payload is decoded from the daemon's own struct tags, so a rename
// on either side would silently render an empty report rather than fail.
func TestDBStatus_DecodesTheDaemonPayload(t *testing.T) {
	body := []byte(`{
		"stanza":"main","stanza_ok":true,"postgres_version":"17",
		"backups":[{"label":"20260725-000000F","type":"full","size_bytes":4096,
			"repo_size_bytes":1024,"started":"2026-07-25T00:00:00Z","stopped":"2026-07-25T00:10:00Z"}],
		"archive_min_wal":"000000010000000000000002","archive_max_wal":"00000001000000000000000A",
		"archiver":{"archived_count":9,"last_archived_wal":"00000001000000000000000A",
			"last_archived_time":"2026-07-25T06:00:00Z","failed_count":0},
		"earliest_recovery":"2026-07-25T00:10:00Z","latest_recovery":"2026-07-25T06:00:00Z"
	}`)

	var s dbStatus
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Stanza != "main" || !s.StanzaOK || s.PostgresVersion != "17" {
		t.Errorf("stanza fields not decoded: %+v", s)
	}
	if len(s.Backups) != 1 || s.Backups[0].RepoSizeBytes != 1024 {
		t.Fatalf("backups not decoded: %+v", s.Backups)
	}
	if s.Archiver.ArchivedCount != 9 || s.Archiver.LastArchivedWAL == "" {
		t.Errorf("archiver not decoded: %+v", s.Archiver)
	}
	if s.EarliestRecovery.IsZero() || s.LatestRecovery.IsZero() {
		t.Errorf("recovery window not decoded: %s → %s", s.EarliestRecovery, s.LatestRecovery)
	}
}

// When Postgres cannot be read the daemon answers 500 with the repository half
// it did read. That half is the answer to "what can I restore", which is the
// question being asked precisely when the database is down.
func TestPartialDBStatus_RendersTheHalfThatWasReadable(t *testing.T) {
	body := []byte(`{"error":"db status: read pg_stat_archiver: psql in ownbase-postgres: exit status 2",
		"status":{"stanza":"main","stanza_ok":true,
			"backups":[{"label":"20260725-000000F","type":"full","repo_size_bytes":1024,
				"stopped":"2026-07-25T00:10:00Z"}],
			"earliest_recovery":"2026-07-25T00:10:00Z"}}`)
	err := &apiError{StatusCode: 500, Body: body, msg: "API returned 500: …"}

	s, raw, reason, ok := partialDBStatus(err)
	if !ok {
		t.Fatal("partialDBStatus did not recognise the daemon's error body")
	}
	if s.Stanza != "main" || len(s.Backups) != 1 {
		t.Errorf("repository half not decoded: %+v", s)
	}
	if !strings.Contains(reason, "pg_stat_archiver") {
		t.Errorf("reason does not name what failed: %q", reason)
	}
	if len(raw) == 0 {
		t.Error("raw payload is empty; --json would print nothing")
	}

	out := captureStdout(t, func() { printDBStatus("mybase", s) })
	// "nothing archived yet" would be a claim about archiving made from a
	// database nobody could reach.
	if strings.Contains(out, "nothing archived yet") {
		t.Errorf("report claims archiving state it could not read:\n%s", out)
	}
	if !strings.Contains(out, "Archiving:     ? unknown") {
		t.Errorf("report does not mark archiving unknown:\n%s", out)
	}
	if !strings.Contains(out, "→  unknown") {
		t.Errorf("recovery window should state its known end and stop there:\n%s", out)
	}
	if !strings.Contains(out, "20260725-000000F") {
		t.Errorf("report drops the backups it could read:\n%s", out)
	}
}

// Any other failure — a daemon too old to send a half, an unreachable agent —
// stays an ordinary error rather than being rendered as an empty report.
func TestPartialDBStatus_IgnoresEverythingElse(t *testing.T) {
	for name, err := range map[string]error{
		"plain error":  errors.New("connection refused"),
		"no status":    &apiError{StatusCode: 500, Body: []byte(`{"error":"db status: boom"}`), msg: "…"},
		"not json":     &apiError{StatusCode: 500, Body: []byte("db status: boom"), msg: "…"},
		"empty stanza": &apiError{StatusCode: 500, Body: []byte(`{"error":"x","status":{}}`), msg: "…"},
	} {
		if _, _, _, ok := partialDBStatus(err); ok {
			t.Errorf("%s: partialDBStatus claimed a usable half", name)
		}
	}
}

func TestDBRestoreOutcome_DecodesTheDaemonPayload(t *testing.T) {
	body := []byte(`{"into":"scratch","target":"2026-07-25T14:00:00Z","timeline":"3",
		"databases":2,"relations":412,"last_transaction":"2026-07-25 13:58:02+00",
		"scratch_endpoint":"127.0.0.1:5433"}`)

	var o dbRestoreOutcome
	if err := json.Unmarshal(body, &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.Into != "scratch" || o.Timeline != "3" || o.Relations != 412 {
		t.Errorf("outcome not decoded: %+v", o)
	}
	if o.ScratchEndpoint != "127.0.0.1:5433" || o.LastTransaction == "" {
		t.Errorf("scratch fields not decoded: %+v", o)
	}
}

// --into is checked before a connection is opened, so a typo costs nothing.
func TestRunDBRestore_RejectsAnUnknownDestination(t *testing.T) {
	err := runDBRestore("mybase", "", "prod", 0, true, false)
	if err == nil {
		t.Fatal("want an error for --into prod")
	}
	if !strings.Contains(err.Error(), "scratch") || !strings.Contains(err.Error(), "production") {
		t.Errorf("error should name the two valid destinations, got: %v", err)
	}
}

func TestPrintDBStatus_SaysWhenArchivingIsFailing(t *testing.T) {
	archived := time.Date(2026, 7, 25, 6, 0, 0, 0, time.UTC)

	var s dbStatus
	s.Stanza = "main"
	s.StanzaOK = true
	s.EarliestRecovery = archived.Add(-6 * time.Hour)
	s.LatestRecovery = archived
	s.Archiver.ArchivedCount = 9
	s.Archiver.LastArchivedTime = archived
	s.Archiver.FailedCount = 4
	s.Archiver.LastArchivedWAL = "00000001000000000000000A"
	s.Archiver.LastFailedWAL = "00000001000000000000000B"
	s.Archiver.LastFailedTime = archived.Add(time.Hour)

	out := captureStdout(t, func() { printDBStatus("mybase", s) })

	// This is the failure worth interrupting for: the database is serving
	// while its recovery window has stopped moving, so the report has to say
	// both halves or it reads like nothing is wrong.
	for _, want := range []string{"FAILING", "00000001000000000000000B", "stopped"} {
		if !strings.Contains(out, want) {
			t.Errorf("report should mention %q, got:\n%s", want, out)
		}
	}
}

func TestPrintDBStatus_ShowsTheRecoveryWindow(t *testing.T) {
	var s dbStatus
	s.Stanza = "main"
	s.StanzaOK = true
	s.EarliestRecovery = time.Date(2026, 7, 25, 0, 10, 0, 0, time.UTC)
	s.LatestRecovery = time.Date(2026, 7, 25, 6, 0, 0, 0, time.UTC)
	s.Archiver.ArchivedCount = 9
	s.Archiver.LastArchivedTime = s.LatestRecovery

	out := captureStdout(t, func() { printDBStatus("mybase", s) })

	if !strings.Contains(out, "Recovery window") {
		t.Errorf("report should state the recovery window, got:\n%s", out)
	}
	// The suggested command has to be one that works. The end of the window is
	// when the last WAL segment finished archiving, which is after the last
	// change inside it, so restoring to that instant replays everything, never
	// reaches the target, and refuses to start. Omitting --to is the form that
	// means "as recent as possible" and cannot miss.
	if strings.Contains(out, `--to "2026-07-25 06:00:00+00"`) {
		t.Errorf("report suggests restoring to the end of the window, which Postgres would refuse:\n%s", out)
	}
	if !strings.Contains(out, "db restore mybase  ") {
		t.Errorf("report should offer the no-target restore, got:\n%s", out)
	}
}

func TestPrintDBStatus_SaysWhenNothingIsArchivedYet(t *testing.T) {
	out := captureStdout(t, func() { printDBStatus("mybase", dbStatus{Stanza: "main", StanzaOK: true}) })
	if !strings.Contains(out, "nothing archived yet") {
		t.Errorf("report should say archiving has not started, got:\n%s", out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("report should not invent a recovery window, got:\n%s", out)
	}
}

func TestPrintDBRestoreOutcome_ScratchExplainsTheTeardown(t *testing.T) {
	o := dbRestoreOutcome{
		Into: "scratch", Timeline: "3", Databases: 2, Relations: 412,
		LastTransaction: "2026-07-25 13:58:02+00", ScratchEndpoint: "127.0.0.1:5433",
	}
	out := captureStdout(t, func() { printDBRestoreOutcome("mybase", "root@203.0.113.10", o) })

	for _, want := range []string{
		"2026-07-25 13:58:02+00", // what point the data actually represents
		"127.0.0.1:5433",         // where to reach it
		"untouched",              // that production was not replaced
		// The forwarding command must be pasteable, not a placeholder.
		"ssh -L 5433:127.0.0.1:5433 root@203.0.113.10",
		"podman rm -f " + scratchContainerName,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary should mention %q, got:\n%s", want, out)
		}
	}
}

func TestPrintDBRestoreOutcome_ProductionMentionsTheNewBackup(t *testing.T) {
	o := dbRestoreOutcome{
		Into: "production", Timeline: "4", Databases: 2, Relations: 412,
		BackupAfterPromote: true,
	}
	out := captureStdout(t, func() { printDBRestoreOutcome("mybase", "root@203.0.113.10", o) })
	if !strings.Contains(out, "new timeline") {
		t.Errorf("summary should report the post-promote backup, got:\n%s", out)
	}
	if strings.Contains(out, scratchContainerName) {
		t.Errorf("a production restore has no scratch instance to tear down, got:\n%s", out)
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{5 * 1024 * 1024, "5.0 MiB"},
		{3 * 1024 * 1024 * 1024, "3.0 GiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLocalTime_RendersUnsetAsADash(t *testing.T) {
	if got := localTime(time.Time{}); got != "—" {
		t.Errorf("localTime(zero) = %q, want a dash", got)
	}
}
