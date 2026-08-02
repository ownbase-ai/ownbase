package backup

// These tests cover the parts of point-in-time recovery that hold no Podman:
// finding the Postgres and its repository in ownbase.yaml, folding
// `pgbackrest info` into a status, and — the one worth having — refusing a
// recovery target the repository cannot reach.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/schema"
)

// pgConfig is a Base with a Postgres archiving to a pgBackRest repository host.
func pgConfig() *schema.OwnbaseConfig {
	return &schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"pgbackrest": {
				Volumes: []schema.VolumeDecl{
					{Name: "repo", Mount: pgBackRestRepoMount},
					{Name: "log", Mount: "/var/log/pgbackrest"},
				},
			},
			"postgres": {
				Env: []string{
					"POSTGRES_USER=revolve",
					"POSTGRES_DB=revolve",
					"PGBACKREST_HOST=ownbase-pgbackrest",
					"PGBACKREST_STANZA=main",
				},
			},
			"api": {Requires: []string{"postgres"}},
		},
	}
}

func TestFindPGBackRest_FindsPostgresRepoAndDependants(t *testing.T) {
	pb, err := FindPGBackRest(pgConfig())
	if err != nil {
		t.Fatalf("FindPGBackRest: %v", err)
	}
	if pb.Service != "postgres" {
		t.Errorf("Service = %q, want postgres", pb.Service)
	}
	if pb.Stanza != "main" {
		t.Errorf("Stanza = %q, want main", pb.Stanza)
	}
	if pb.SuperUser != "revolve" || pb.Database != "revolve" {
		t.Errorf("SuperUser/Database = %q/%q, want revolve/revolve", pb.SuperUser, pb.Database)
	}
	// The repository volume is found by mount point, not by name: a restore
	// mounts it directly rather than reaching the repository host over SSH.
	if pb.RepoVolume != "ownbase-pgbackrest-repo" {
		t.Errorf("RepoVolume = %q, want ownbase-pgbackrest-repo", pb.RepoVolume)
	}
	if len(pb.Dependants) != 1 || pb.Dependants[0] != "api" {
		t.Errorf("Dependants = %v, want [api]", pb.Dependants)
	}
	if pb.Container() != "ownbase-postgres" || pb.Unit() != "ownbase-postgres.service" {
		t.Errorf("Container/Unit = %q/%q", pb.Container(), pb.Unit())
	}
	if len(pb.DependantUnits) != 1 || pb.DependantUnits[0] != "ownbase-api.service" {
		t.Errorf("DependantUnits = %v, want [ownbase-api.service]", pb.DependantUnits)
	}
}

func TestFindPGBackRest_ReplicatedPrimaryNames(t *testing.T) {
	// Replicating Postgres is not a v1 goal, but Container/DataVolume must
	// still agree on primary naming if someone sets replicas:.
	n := 2
	oc := pgConfig()
	pg := oc.Services["postgres"]
	pg.Replicas = &n
	pg.DataPath = DefaultPostgresDataDir // so DataVolume is discovered
	oc.Services["postgres"] = pg
	api := oc.Services["api"]
	api.Replicas = &n
	oc.Services["api"] = api

	pb, err := FindPGBackRest(oc)
	if err != nil {
		t.Fatalf("FindPGBackRest: %v", err)
	}
	if pb.Container() != "ownbase-postgres-0" {
		t.Errorf("Container = %q, want ownbase-postgres-0", pb.Container())
	}
	if pb.Unit() != "ownbase-postgres-0.service" {
		t.Errorf("Unit = %q, want ownbase-postgres-0.service", pb.Unit())
	}
	if pb.DataVolume != "ownbase-postgres-data-0" {
		t.Errorf("DataVolume = %q, want ownbase-postgres-data-0", pb.DataVolume)
	}
	if len(pb.DependantUnits) != 1 || pb.DependantUnits[0] != "ownbase-api-0.service" {
		t.Errorf("DependantUnits = %v, want [ownbase-api-0.service]", pb.DependantUnits)
	}
}

// A service called postgres that archives nowhere has no point-in-time recovery
// to report on, and saying so is more use than reporting on nothing.
// The data directory volume is found by mount path, like the repository is. A
// production restore replaces that directory, so assuming the volume is called
// "data" would break the destructive path on any Base that names it otherwise.
func TestFindPGBackRest_FindsDataVolumeByMount(t *testing.T) {
	cases := []struct {
		name   string
		env    []string
		volume schema.VolumeDecl
		want   string
	}{
		{
			name:   "image default",
			volume: schema.VolumeDecl{Name: "pgdata", Mount: DefaultPostgresDataDir},
			want:   "ownbase-postgres-pgdata",
		},
		{
			name:   "PGDATA moves it",
			env:    []string{"PGDATA=/data/pg17"},
			volume: schema.VolumeDecl{Name: "cluster", Mount: "/data/pg17"},
			want:   "ownbase-postgres-cluster",
		},
		{
			name:   "pg1-path wins, trailing slash ignored",
			env:    []string{"PGDATA=/unused", "PGBACKREST_PG1_PATH=/srv/pg"},
			volume: schema.VolumeDecl{Name: "srv", Mount: "/srv/pg/"},
			want:   "ownbase-postgres-srv",
		},
		{
			// Not an error: only a production restore needs it, and db status
			// has to keep working here.
			name:   "no volume at the data directory",
			volume: schema.VolumeDecl{Name: "data", Mount: "/var/lib/somewhere-else"},
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oc := pgConfig()
			svc := oc.Services["postgres"]
			svc.Env = append(svc.Env, tc.env...)
			svc.Volumes = []schema.VolumeDecl{tc.volume}
			oc.Services["postgres"] = svc

			pb, err := FindPGBackRest(oc)
			if err != nil {
				t.Fatalf("FindPGBackRest: %v", err)
			}
			if pb.DataVolume != tc.want {
				t.Errorf("DataVolume = %q, want %q", pb.DataVolume, tc.want)
			}
		})
	}
}

// A service that declares no volumes: still gets one, created by the compiler
// as ownbase-<name>-data at its data_path. Reading only the declaration would
// miss it and refuse a production restore on the most ordinary config there is.
func TestFindPGBackRest_FindsTheImplicitDataVolume(t *testing.T) {
	cases := []struct {
		name     string
		dataPath string
		want     string
	}{
		{name: "data_path is the data directory", dataPath: DefaultPostgresDataDir, want: "ownbase-postgres-data"},
		{name: "data_path is somewhere else", dataPath: "", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oc := pgConfig()
			svc := oc.Services["postgres"]
			svc.DataPath = tc.dataPath
			oc.Services["postgres"] = svc

			pb, err := FindPGBackRest(oc)
			if err != nil {
				t.Fatalf("FindPGBackRest: %v", err)
			}
			if pb.DataVolume != tc.want {
				t.Errorf("DataVolume = %q, want %q", pb.DataVolume, tc.want)
			}
		})
	}
}

// Without a data volume the destructive path has nothing to restore over, and
// finding that out after the Base is down would be the worst possible moment.
func TestRestoreIntoProduction_RefusesWithoutADataVolume(t *testing.T) {
	// The path it names is the one it looked for, which on a Base that sets
	// PGBACKREST_PG1_PATH is not the default.
	for _, dataDir := range []string{"", "/srv/pg"} {
		pb := PGBackRest{Service: "postgres", Stanza: "main", RepoVolume: "ownbase-pgbackrest-repo", DataDir: dataDir}
		_, err := restoreIntoProduction(context.Background(), pb, RestoreOptions{})
		if err == nil {
			t.Fatal("want an error when no volume holds the data directory")
		}
		want := dataDir
		if want == "" {
			want = DefaultPostgresDataDir
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got: %v", want, err)
		}
	}
}

func TestFindPGBackRest_RequiresArchiving(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"postgres": {Env: []string{"POSTGRES_USER=app"}},
		},
	}
	_, err := FindPGBackRest(oc)
	if err == nil {
		t.Fatal("want an error for a Postgres that archives nowhere")
	}
	if !strings.Contains(err.Error(), "PGBACKREST_HOST") {
		t.Errorf("error should name what is missing, got: %v", err)
	}
}

func TestFindPGBackRest_ReportsMissingRepositoryHost(t *testing.T) {
	oc := pgConfig()
	delete(oc.Services, "pgbackrest")
	_, err := FindPGBackRest(oc)
	if err == nil || !strings.Contains(err.Error(), "pgbackrest") {
		t.Fatalf("want an error naming the missing repository host, got: %v", err)
	}
}

func TestFindPGBackRest_ReportsRepositoryHostWithoutVolume(t *testing.T) {
	oc := pgConfig()
	host := oc.Services["pgbackrest"]
	host.Volumes = []schema.VolumeDecl{{Name: "log", Mount: "/var/log/pgbackrest"}}
	oc.Services["pgbackrest"] = host

	_, err := FindPGBackRest(oc)
	if err == nil || !strings.Contains(err.Error(), pgBackRestRepoMount) {
		t.Fatalf("want an error naming the expected mount, got: %v", err)
	}
}

func TestFindPGBackRest_DefaultsStanzaAndSuperUser(t *testing.T) {
	oc := pgConfig()
	svc := oc.Services["postgres"]
	svc.Env = []string{"PGBACKREST_HOST=ownbase-pgbackrest"}
	oc.Services["postgres"] = svc

	pb, err := FindPGBackRest(oc)
	if err != nil {
		t.Fatalf("FindPGBackRest: %v", err)
	}
	if pb.Stanza != "main" {
		t.Errorf("Stanza = %q, want the main default", pb.Stanza)
	}
	if pb.SuperUser != "postgres" {
		t.Errorf("SuperUser = %q, want the postgres default", pb.SuperUser)
	}
	// libpq connects to the user's own database when none is named.
	if pb.database() != "postgres" {
		t.Errorf("database() = %q, want postgres", pb.database())
	}
}

func TestApplyRepoInfo_FoldsPGBackRestInfo(t *testing.T) {
	raw := stanzaInfo{Name: "main"}
	raw.Status.Code = 0
	raw.DB = []struct {
		Version string `json:"version"`
	}{{Version: "17"}}
	raw.Archive = []struct {
		Min string `json:"min"`
		Max string `json:"max"`
	}{{Min: "000000010000000000000002", Max: "00000001000000000000000A"}}
	raw.Backup = append(raw.Backup, struct {
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
	}{Label: "20260725-000000F", Type: "full"})
	raw.Backup[0].Info.Size = 4096
	raw.Backup[0].Info.Repository.Size = 1024
	raw.Backup[0].Timestamp.Start = 1750000000
	raw.Backup[0].Timestamp.Stop = 1750000600

	var status DBStatus
	applyRepoInfo(&status, raw)

	if !status.StanzaOK {
		t.Error("StanzaOK = false for status code 0")
	}
	if status.PostgresVersion != "17" {
		t.Errorf("PostgresVersion = %q, want 17", status.PostgresVersion)
	}
	if status.ArchiveMinWAL == "" || status.ArchiveMaxWAL == "" {
		t.Errorf("WAL range not carried over: %q → %q", status.ArchiveMinWAL, status.ArchiveMaxWAL)
	}
	if len(status.Backups) != 1 || status.Backups[0].Label != "20260725-000000F" {
		t.Fatalf("Backups = %+v", status.Backups)
	}
	// Recovery cannot reach before the end of the oldest backup held: WAL
	// older than it has nothing to be applied to.
	if want := time.Unix(1750000600, 0).UTC(); !status.EarliestRecovery.Equal(want) {
		t.Errorf("EarliestRecovery = %s, want the oldest backup's stop time %s", status.EarliestRecovery, want)
	}
}

func TestArchivingBroken(t *testing.T) {
	archived := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		a    DBArchiver
		want bool
	}{
		{"never failed", DBArchiver{ArchivedCount: 9, LastArchivedTime: archived}, false},
		{
			// The state that matters: the database serves queries while its
			// recovery window has stopped moving.
			name: "failing since the last success",
			a:    DBArchiver{ArchivedCount: 9, LastArchivedTime: archived, FailedCount: 3, LastFailedTime: archived.Add(time.Hour)},
			want: true,
		},
		{
			name: "recovered after earlier failures",
			a:    DBArchiver{ArchivedCount: 9, LastArchivedTime: archived, FailedCount: 3, LastFailedTime: archived.Add(-time.Hour)},
			want: false,
		},
		{
			name: "failed and never succeeded",
			a:    DBArchiver{FailedCount: 1, LastFailedTime: archived},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.ArchivingBroken(); got != tc.want {
				t.Errorf("ArchivingBroken() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseTarget_AcceptsWhatOperatorsType(t *testing.T) {
	want := time.Date(2026, 7, 25, 14, 30, 0, 0, time.UTC)
	for _, in := range []string{
		"2026-07-25 14:30:00+00",
		"2026-07-25 14:30:00+0000",
		"2026-07-25 14:30:00Z",
		"2026-07-25T14:30:00Z",
		"2026-07-25 14:30:00",
		"2026-07-25 14:30",
	} {
		got, err := ParseTarget(in)
		if err != nil {
			t.Errorf("ParseTarget(%q): %v", in, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("ParseTarget(%q) = %s, want %s", in, got, want)
		}
	}
}

// A zone offset must be honoured rather than dropped: a target that silently
// means a different instant than the one written is the last place to guess.
func TestParseTarget_HonoursOffset(t *testing.T) {
	got, err := ParseTarget("2026-07-25 14:30:00-07")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if want := time.Date(2026, 7, 25, 21, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("ParseTarget = %s, want %s", got, want)
	}
}

func TestParseTarget_RejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "  ", "yesterday", "25/07/2026"} {
		if _, err := ParseTarget(in); err == nil {
			t.Errorf("ParseTarget(%q) accepted an unparseable target", in)
		}
	}
}

func TestFormatTarget_RoundTrips(t *testing.T) {
	want := time.Date(2026, 7, 25, 14, 30, 0, 0, time.UTC)
	got, err := ParseTarget(FormatTarget(want))
	if err != nil {
		t.Fatalf("ParseTarget(FormatTarget(...)): %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("round trip gave %s, want %s", got, want)
	}
}

// statusWithWindow is a repository holding one backup and WAL up to latest.
func statusWithWindow(earliest, latest time.Time) DBStatus {
	return DBStatus{
		Stanza:           "main",
		StanzaOK:         true,
		Backups:          []DBBackup{{Label: "20260725-000000F", Type: "full", Stopped: earliest}},
		EarliestRecovery: earliest,
		LatestRecovery:   latest,
		Archiver:         DBArchiver{LastArchivedWAL: "00000001000000000000000A", LastArchivedTime: latest},
	}
}

func TestValidateTarget_AcceptsATargetInTheWindow(t *testing.T) {
	earliest := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	latest := earliest.Add(6 * time.Hour)
	status := statusWithWindow(earliest, latest)

	for _, target := range []time.Time{earliest, earliest.Add(time.Hour), latest} {
		if err := ValidateTarget(status, target); err != nil {
			t.Errorf("ValidateTarget(%s): %v", target, err)
		}
	}
	// A zero target means "everything the repository holds", which is always
	// valid when there is a backup.
	if err := ValidateTarget(status, time.Time{}); err != nil {
		t.Errorf("ValidateTarget(zero): %v", err)
	}
}

// This is the check the whole file exists for. Postgres does not say "no data
// for that time" — it replays everything and aborts with "recovery ended before
// configured recovery target was reached", which reads like data loss.
func TestValidateTarget_RefusesATargetPastTheArchive(t *testing.T) {
	earliest := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	latest := earliest.Add(6 * time.Hour)

	err := ValidateTarget(statusWithWindow(earliest, latest), latest.Add(time.Minute))
	if err == nil {
		t.Fatal("want an error for a target past the end of the archive")
	}
	msg := err.Error()
	for _, want := range []string{
		FormatTarget(latest),       // where recovery can actually reach
		"pg_switch_wal",            // how to bring that forward
		"looks like data loss",     // that the backup is not broken
		"omit --to",                // the other way out
		"00000001000000000000000A", // the segment it stopped at
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got:\n%s", want, msg)
		}
	}
}

func TestValidateTarget_RefusesATargetBeforeTheOldestBackup(t *testing.T) {
	earliest := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	latest := earliest.Add(6 * time.Hour)

	err := ValidateTarget(statusWithWindow(earliest, latest), earliest.Add(-time.Hour))
	if err == nil {
		t.Fatal("want an error for a target older than the oldest backup held")
	}
	if !strings.Contains(err.Error(), "retention") || !strings.Contains(err.Error(), FormatTarget(earliest)) {
		t.Errorf("error should explain retention and name the oldest point, got:\n%v", err)
	}
}

func TestValidateTarget_RefusesAnEmptyRepository(t *testing.T) {
	err := ValidateTarget(DBStatus{Stanza: "main"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "no backups") {
		t.Fatalf("want an error for an empty repository, got: %v", err)
	}
}

// A cluster that has never failed to archive returns two empty trailing
// fields. Trimming whitespace from the row rather than just its line ending
// took the tabs delimiting them with it, and the healthy case — the common one
// — failed to parse.
func TestParseArchiverRow_KeepsEmptyTrailingFields(t *testing.T) {
	got, err := parseArchiverRow("27\t000000010000000000000019\t2026-07-26T01:42:46+00\t0\t\t\n")
	if err != nil {
		t.Fatalf("parseArchiverRow: %v", err)
	}
	if got.ArchivedCount != 27 || got.LastArchivedWAL != "000000010000000000000019" {
		t.Errorf("archived fields = %d/%q", got.ArchivedCount, got.LastArchivedWAL)
	}
	if got.LastArchivedTime.IsZero() {
		t.Error("last_archived_time not parsed")
	}
	if got.FailedCount != 0 || got.LastFailedWAL != "" || !got.LastFailedTime.IsZero() {
		t.Errorf("failure fields should be empty, got %+v", got)
	}
	if got.ArchivingBroken() {
		t.Error("ArchivingBroken() = true for a cluster that has never failed")
	}
}

func TestParseArchiverRow_ParsesFailures(t *testing.T) {
	got, err := parseArchiverRow("27\t000000010000000000000019\t2026-07-26T01:42:46+00\t4\t00000001000000000000001A\t2026-07-26T02:10:00+00\n")
	if err != nil {
		t.Fatalf("parseArchiverRow: %v", err)
	}
	if got.FailedCount != 4 || got.LastFailedWAL != "00000001000000000000001A" {
		t.Errorf("failure fields = %d/%q", got.FailedCount, got.LastFailedWAL)
	}
	if !got.ArchivingBroken() {
		t.Error("ArchivingBroken() = false while archiving has failed since the last success")
	}
}

func TestParseArchiverRow_RejectsAShortRow(t *testing.T) {
	if _, err := parseArchiverRow("27\tsomething\n"); err == nil {
		t.Fatal("want an error for a row with too few fields")
	}
}

func TestParsePGTime(t *testing.T) {
	want := time.Date(2026, 7, 25, 14, 30, 0, 0, time.UTC)
	for _, in := range []string{"2026-07-25T14:30:00+00", "2026-07-25T14:30:00+0000", "2026-07-25T14:30:00Z"} {
		if got := parsePGTime(in); !got.Equal(want) {
			t.Errorf("parsePGTime(%q) = %s, want %s", in, got, want)
		}
	}
	// An empty or unparseable value means "unknown", which every caller
	// already renders as such.
	for _, in := range []string{"", "  ", "not a time"} {
		if got := parsePGTime(in); !got.IsZero() {
			t.Errorf("parsePGTime(%q) = %s, want the zero time", in, got)
		}
	}
}
