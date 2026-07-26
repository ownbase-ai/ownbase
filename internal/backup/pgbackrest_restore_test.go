package backup

// The restore itself needs Podman and a real repository, so these tests cover
// what can be checked without either: the script's syntax and the behaviour it
// carries, and the diagnosis that turns a failed restore into a sentence an
// operator can act on.

import (
	"os/exec"
	"strings"
	"testing"
)

// The restore script only ever runs inside a container on a Base, so a syntax
// error in it would surface as a failed recovery at the worst possible moment
// rather than at build time. Parse it here instead.
func TestPGBackRestRestoreScript_IsValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(pgBackRestRestoreScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore script is not valid bash: %v\n%s", err, out)
	}
}

// testBringBack is a bringBack with the machine replaced: it records the units
// it was asked to start, in order.
func testBringBack(serving bool) (*bringBack, *[]string, *strings.Builder) {
	var started []string
	var out strings.Builder
	b := newBringBack(PGBackRest{
		Service:    "postgres",
		Dependants: []string{"api", "worker"},
	}, &out)
	b.startUnit = func(unit string) error {
		started = append(started, unit)
		return nil
	}
	b.serving = func() bool { return serving }
	return b, &started, &out
}

// The database comes back before the services that need it, because an app
// started against a database that is not there crash-loops rather than
// recovers — and then the logs blame the app.
func TestBringBack_StartsPostgresBeforeItsDependants(t *testing.T) {
	b, started, _ := testBringBack(true)
	b.stoppedPostgres = true

	b.all()

	want := []string{"ownbase-postgres.service", "ownbase-api.service", "ownbase-worker.service"}
	if strings.Join(*started, ",") != strings.Join(want, ",") {
		t.Errorf("started %v, want %v", *started, want)
	}
}

// A restore that fails while the database is still down must not hand every app
// a database that cannot answer. Leaving them stopped is the honest state, so
// long as it says so and says how to undo it.
func TestBringBack_LeavesDependantsStoppedWhileTheDatabaseIsNot(t *testing.T) {
	b, started, out := testBringBack(false)
	b.stoppedPostgres = true

	b.all()

	if len(*started) != 1 || (*started)[0] != "ownbase-postgres.service" {
		t.Errorf("started %v, want only the database", *started)
	}
	if !strings.Contains(out.String(), "systemctl start ownbase-api.service ownbase-worker.service") {
		t.Errorf("output does not say how to start them by hand:\n%s", out)
	}
}

// A shutdown that fails part-way — the second dependant refuses to stop — still
// has to bring back the first. Postgres was never stopped in that case, so only
// the dependants are started.
func TestBringBack_RecoversAPartialShutdown(t *testing.T) {
	b, started, _ := testBringBack(true)

	b.all()

	want := []string{"ownbase-api.service", "ownbase-worker.service"}
	if strings.Join(*started, ",") != strings.Join(want, ",") {
		t.Errorf("started %v, want %v", *started, want)
	}
}

// The restore starts dependants itself as soon as the database is serving, so
// the outage ends before the post-promote backup. The deferred call must then
// do nothing rather than restart everything a second time.
func TestBringBack_IsANoOpAfterASuccessfulRestore(t *testing.T) {
	b, started, _ := testBringBack(true)
	b.dependants()
	before := len(*started)

	b.all()

	if len(*started) != before {
		t.Errorf("started %v after the restore had already brought them back", (*started)[before:])
	}
}

// Each of these is a production failure the script exists to avoid.
func TestPGBackRestRestoreScript_KeepsLoadBearingBehaviour(t *testing.T) {
	musts := map[string]string{
		"repo1-path=/repo":              "must restore from the mounted repository volume, not the repo host over SSH",
		"cmd=/usr/local/bin/pgbackrest": "restore_command is recorded verbatim and must use the env-scrubbing wrapper",
		"usermod -aG":                   "the repository is 0750 and owned by the repo host's user; postgres cannot read it otherwise",
		"--target-action=promote":       "a recovery that stops without promoting leaves a cluster nobody can write to",
		"OWNBASE_RESTORE_ONLY":          "a production restore only restores; the service itself replays the archive on start",
		"--recovery-option=":            "pgBackRest records its own --config as restore_command, which does not exist in the service's container",
		"archive_mode=off":              "a scratch instance must never push its own WAL into the backup repository",
	}
	for fragment, why := range musts {
		if !strings.Contains(pgBackRestRestoreScript, fragment) {
			t.Errorf("restore script no longer contains %q — %s", fragment, why)
		}
	}

	// Postgres must be the container's own process, so the instance lives
	// exactly as long as the container and `podman rm -f` is a complete
	// teardown. Starting it in the background instead left podman waiting on a
	// process nobody watched, and `podman exec` then failed with "container
	// state improper".
	if !strings.Contains(pgBackRestRestoreScript, "exec gosu postgres postgres") {
		t.Error("restore script must exec Postgres as the container's main process")
	}
}

func TestDefaultsAreNotProduction(t *testing.T) {
	// The point of a scratch restore is that production keeps serving on its
	// own port throughout.
	if DefaultScratchPort == 5432 {
		t.Error("the scratch port must not be Postgres's own")
	}
}

// A successful point-in-time recovery logs where it stopped, and that is the
// point the data represents. Postgres only logs "last completed transaction"
// when recovery *failed* to reach its target, so reading only that line left
// every successful restore unable to say what it had recovered to.
func TestExtractRecoveryStop_ReadsTheStoppingLine(t *testing.T) {
	log := strings.Join([]string{
		"LOG:  consistent recovery state reached at 0/12000158",
		"LOG:  recovery stopping before commit of transaction 942, time 2026-07-26 01:37:33.597124+00",
		"LOG:  redo done at 0/1402B170",
		"LOG:  archive recovery complete",
	}, "\n")
	if got := extractRecoveryStop(log); got != "2026-07-26 01:37:33.597124+00" {
		t.Errorf("extractRecoveryStop = %q", got)
	}
}

// A failed recovery logs the other line instead, and it is equally the answer.
func TestExtractRecoveryStop_FallsBackToTheLastTransaction(t *testing.T) {
	log := "FATAL:  recovery ended before configured recovery target was reached\n" +
		"LOG:  last completed transaction was at log time 2026-07-26 01:20:11+00.\n"
	if got := extractRecoveryStop(log); got != "2026-07-26 01:20:11+00" {
		t.Errorf("extractRecoveryStop = %q", got)
	}
	if got := extractRecoveryStop("LOG:  redo done\n"); got != "" {
		t.Errorf("extractRecoveryStop with neither line = %q, want empty", got)
	}
}

func TestExtractLastTransaction(t *testing.T) {
	log := strings.Join([]string{
		"LOG:  starting point-in-time recovery to 2026-07-26 01:37:38+00",
		"LOG:  last completed transaction was at log time 2026-07-26 01:20:11.83422+00.",
		"LOG:  redo done",
	}, "\n")
	if got := extractLastTransaction(log); got != "2026-07-26 01:20:11.83422+00" {
		t.Errorf("extractLastTransaction = %q", got)
	}
	if got := extractLastTransaction("LOG:  redo done\n"); got != "" {
		t.Errorf("extractLastTransaction on a log without the line = %q, want empty", got)
	}
}

// The last transaction is the honest answer to "what point am I looking at", so
// the *last* occurrence is the one that matters when recovery logs several.
func TestExtractLastTransaction_TakesTheLastOccurrence(t *testing.T) {
	log := "last completed transaction was at log time 2026-07-01 00:00:00+00.\n" +
		"last completed transaction was at log time 2026-07-26 01:20:11+00.\n"
	if got := extractLastTransaction(log); got != "2026-07-26 01:20:11+00" {
		t.Errorf("extractLastTransaction = %q, want the last one", got)
	}
}

func TestFirstErrorLine(t *testing.T) {
	// The first error is the cause; the ones after it are its consequences.
	log := strings.Join([]string{
		"LOG:  database system was interrupted",
		"P00   ERROR: [055]: unable to load info file",
		"FATAL:  could not locate a valid checkpoint record",
	}, "\n")
	if got := firstErrorLine(log); got != "P00   ERROR: [055]: unable to load info file" {
		t.Errorf("firstErrorLine = %q", got)
	}
	if got := firstErrorLine("LOG:  all is well\n"); got != "" {
		t.Errorf("firstErrorLine on a clean log = %q, want empty", got)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\nd\n", 2); got != "c\nd" {
		t.Errorf("lastLines = %q, want \"c\\nd\"", got)
	}
	if got := lastLines("a\nb", 5); got != "a\nb" {
		t.Errorf("lastLines with n past the end = %q", got)
	}
}

func TestAtoiSafe(t *testing.T) {
	if got := atoiSafe(" 412 \n"); got != 412 {
		t.Errorf("atoiSafe = %d, want 412", got)
	}
	// psql can return an empty string or an error message; neither should be
	// read as a count.
	for _, in := range []string{"", "ERROR", "4.1"} {
		if got := atoiSafe(in); got != 0 {
			t.Errorf("atoiSafe(%q) = %d, want 0", in, got)
		}
	}
}
