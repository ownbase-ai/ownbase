package gendb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secrets"
)

// fakeRunner stands in for a Postgres container: it records the statements it
// was asked to run and answers the existence check from a set of databases.
type fakeRunner struct {
	running   bool
	databases map[string]bool
	queries   []string
	failWith  error
	// createErr fails only CREATE DATABASE, which is how a race between two
	// reconciles looks: the existence check said no, the create says otherwise.
	createErr error
}

func (f *fakeRunner) Exec(_ context.Context, _ string, args ...string) ([]byte, error) {
	query := args[len(args)-1]
	f.queries = append(f.queries, query)
	if f.failWith != nil {
		return nil, f.failWith
	}
	if f.createErr != nil && strings.HasPrefix(query, "CREATE DATABASE") {
		return nil, f.createErr
	}
	switch {
	case strings.HasPrefix(query, "select 1 from pg_database"):
		for name := range f.databases {
			if strings.Contains(query, "'"+name+"'") {
				return []byte("1\n"), nil
			}
		}
		return []byte("\n"), nil
	case strings.HasPrefix(query, "CREATE DATABASE"):
		name := strings.Trim(strings.TrimPrefix(query, "CREATE DATABASE "), `"`)
		if f.databases == nil {
			f.databases = map[string]bool{}
		}
		f.databases[name] = true
		return []byte("CREATE DATABASE\n"), nil
	}
	return nil, fmt.Errorf("unexpected query %q", query)
}

func (f *fakeRunner) Running(context.Context, string) bool { return f.running }

// testConfig sets up an age key and a secrets dir, returning a Config that uses
// them, in the shape the daemon would.
func testConfig(t *testing.T, runner Runner) Config {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.age")
	if _, err := secrets.GenerateAndSave(keyPath); err != nil {
		t.Fatalf("generate age key: %v", err)
	}
	return Config{
		SecretsDir: filepath.Join(dir, "secrets"),
		AgeKeyPath: keyPath,
		Runner:     runner,
	}
}

// writeSecrets seeds a service's encrypted secrets file.
func writeSecrets(t *testing.T, c Config, service string, values map[string]string) {
	t.Helper()
	if err := c.write(service, values); err != nil {
		t.Fatalf("write %s secrets: %v", service, err)
	}
}

// readSecrets reads a service's decrypted secrets.
func readSecrets(t *testing.T, c Config, service string) map[string]string {
	t.Helper()
	values, err := secrets.IssueMap(c.custody(), c.fileFor(service))
	if err != nil {
		t.Fatalf("read %s secrets: %v", service, err)
	}
	return values
}

// appConfig is a consumer declaring a database on a Postgres provider.
func appConfig() *schema.OwnbaseConfig {
	return &schema.OwnbaseConfig{
		SchemaVersion: schema.CurrentSchemaVersion,
		Services: map[string]schema.ServiceDecl{
			"postgres": {
				Repo: "https://github.com/example/postgres",
				Port: 5432,
				Env:  []string{"POSTGRES_USER=ownbase", "POSTGRES_DB=ownbase"},
			},
			"api": {
				Repo:     "https://github.com/example/api",
				Requires: []string{"postgres"},
				Database: "postgres/revolve",
			},
		},
	}
}

func TestEnsure_CreatesTheDatabaseAndWiresTheURL(t *testing.T) {
	runner := &fakeRunner{running: true}
	cfg := testConfig(t, runner)
	writeSecrets(t, cfg, "postgres", map[string]string{"POSTGRES_PASSWORD": "s3cret"})

	got, err := Ensure(context.Background(), appConfig(), cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(got.Created) != 1 || got.Created[0] != "postgres/revolve" {
		t.Errorf("Created = %v, want [postgres/revolve]", got.Created)
	}
	if len(got.Wired) != 1 || got.Wired[0] != "api" {
		t.Errorf("Wired = %v, want [api]", got.Wired)
	}
	if len(got.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", got.Skipped)
	}

	// The URL lands in the consumer's secrets file, which is what turns it into
	// DATABASE_URL in the container — and keeps it out of git and the unit file.
	url := readSecrets(t, cfg, "api")[schema.DatabaseURLKey]
	if want := "postgresql://ownbase:s3cret@ownbase-postgres:5432/revolve"; url != want {
		t.Errorf("DATABASE_URL = %q, want %q", url, want)
	}
	// The consumer's URL must never be written into the provider's file.
	if _, ok := readSecrets(t, cfg, "postgres")[schema.DatabaseURLKey]; ok {
		t.Error("provider's secrets file gained a DATABASE_URL")
	}
}

// A Base that has been up for a month must do no work here, because any write
// changes the secrets fingerprint and restarts the container.
func TestEnsure_IsIdempotent(t *testing.T) {
	runner := &fakeRunner{running: true}
	cfg := testConfig(t, runner)
	writeSecrets(t, cfg, "postgres", map[string]string{"POSTGRES_PASSWORD": "s3cret"})

	if _, err := Ensure(context.Background(), appConfig(), cfg); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	before, err := os.Stat(cfg.fileFor("api"))
	if err != nil {
		t.Fatalf("stat api secrets: %v", err)
	}

	got, err := Ensure(context.Background(), appConfig(), cfg)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if len(got.Created) != 0 || len(got.Wired) != 0 {
		t.Errorf("second run reported work: created=%v wired=%v", got.Created, got.Wired)
	}
	after, err := os.Stat(cfg.fileFor("api"))
	if err != nil {
		t.Fatalf("stat api secrets: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("second run rewrote the secrets file, which would restart the container for nothing")
	}
	// It also must not issue a CREATE for a database that already exists.
	creates := 0
	for _, q := range runner.queries {
		if strings.HasPrefix(q, "CREATE DATABASE") {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("issued %d CREATE DATABASE statements across two runs, want 1", creates)
	}
}

// A rotated provider password has to reach the consumer somehow, and declaring
// database: is what says OwnBase owns this value.
func TestEnsure_RewritesTheURLWhenTheProviderPasswordChanges(t *testing.T) {
	runner := &fakeRunner{running: true}
	cfg := testConfig(t, runner)
	writeSecrets(t, cfg, "postgres", map[string]string{"POSTGRES_PASSWORD": "old"})
	if _, err := Ensure(context.Background(), appConfig(), cfg); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}

	writeSecrets(t, cfg, "postgres", map[string]string{"POSTGRES_PASSWORD": "new"})
	got, err := Ensure(context.Background(), appConfig(), cfg)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if len(got.Wired) != 1 {
		t.Errorf("Wired = %v, want the consumer rewired", got.Wired)
	}
	if url := readSecrets(t, cfg, "api")[schema.DatabaseURLKey]; !strings.Contains(url, ":new@") {
		t.Errorf("DATABASE_URL = %q, want the new password", url)
	}
}

// The consumer's own secrets must survive: the URL is one key in a file that may
// hold anything else the operator set.
func TestEnsure_PreservesOtherSecrets(t *testing.T) {
	runner := &fakeRunner{running: true}
	cfg := testConfig(t, runner)
	writeSecrets(t, cfg, "postgres", map[string]string{"POSTGRES_PASSWORD": "s3cret"})
	writeSecrets(t, cfg, "api", map[string]string{"STRIPE_KEY": "sk_live_x"})

	if _, err := Ensure(context.Background(), appConfig(), cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	values := readSecrets(t, cfg, "api")
	if values["STRIPE_KEY"] != "sk_live_x" {
		t.Errorf("unrelated secret lost: %v", values)
	}
	if values[schema.DatabaseURLKey] == "" {
		t.Error("DATABASE_URL not written")
	}
}

// On a Base's first reconcile the provider is not up yet. That is a skip with an
// explanation, not an error: the next tick finishes the job.
func TestEnsure_SkipsWhileTheProviderIsDown(t *testing.T) {
	runner := &fakeRunner{running: false}
	cfg := testConfig(t, runner)
	writeSecrets(t, cfg, "postgres", map[string]string{"POSTGRES_PASSWORD": "s3cret"})

	got, err := Ensure(context.Background(), appConfig(), cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(got.Created) != 0 || len(got.Wired) != 0 {
		t.Errorf("provisioned against a stopped provider: %+v", got)
	}
	if len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "not running") {
		t.Errorf("Skipped = %v, want one entry saying the provider is down", got.Skipped)
	}
	if len(runner.queries) != 0 {
		t.Errorf("ran %v against a stopped container", runner.queries)
	}
}

func TestEnsure_SkipsWhenTheProviderHasNoPassword(t *testing.T) {
	runner := &fakeRunner{running: true}
	cfg := testConfig(t, runner)

	got, err := Ensure(context.Background(), appConfig(), cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(got.Wired) != 0 {
		t.Errorf("wired a URL with no password: %+v", got)
	}
	if len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "POSTGRES_PASSWORD") {
		t.Errorf("Skipped = %v, want one entry naming the missing password", got.Skipped)
	}
}

// A psql failure is a real error, not a skip: something is wrong that retrying
// on a timer will not fix quietly.
func TestEnsure_ReportsAPostgresFailure(t *testing.T) {
	runner := &fakeRunner{running: true, failWith: fmt.Errorf("FATAL: role \"ownbase\" does not exist")}
	cfg := testConfig(t, runner)
	writeSecrets(t, cfg, "postgres", map[string]string{"POSTGRES_PASSWORD": "s3cret"})

	_, err := Ensure(context.Background(), appConfig(), cfg)
	if err == nil {
		t.Fatal("want an error when psql fails")
	}
	if !strings.Contains(err.Error(), "revolve") {
		t.Errorf("error should name the database, got: %v", err)
	}
}

// A race between two reconciles, or a database created in between, produces the
// outcome that was asked for.
func TestEnsure_TreatsAlreadyExistsAsSuccess(t *testing.T) {
	runner := &fakeRunner{running: true, createErr: fmt.Errorf(`ERROR: database "revolve" already exists`)}
	cfg := testConfig(t, runner)
	writeSecrets(t, cfg, "postgres", map[string]string{"POSTGRES_PASSWORD": "s3cret"})

	got, err := Ensure(context.Background(), appConfig(), cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(got.Created) != 0 {
		t.Errorf("Created = %v, want none — the database was already there", got.Created)
	}
	// The URL is still wired: the point is that the database exists, not who
	// created it.
	if len(got.Wired) != 1 {
		t.Errorf("Wired = %v, want the consumer wired anyway", got.Wired)
	}
}

func TestEnsure_DoesNothingWithoutADeclaration(t *testing.T) {
	runner := &fakeRunner{running: true}
	cfg := testConfig(t, runner)

	oc := appConfig()
	svc := oc.Services["api"]
	svc.Database = ""
	oc.Services["api"] = svc

	got, err := Ensure(context.Background(), oc, cfg)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(got.Created) != 0 || len(got.Wired) != 0 || len(got.Skipped) != 0 {
		t.Errorf("did work for a config with no database: %+v", got)
	}
	if len(runner.queries) != 0 {
		t.Errorf("ran %v with nothing declared", runner.queries)
	}
}

func TestConnectionFor_DefaultsAndOverrides(t *testing.T) {
	ref := schema.DatabaseRef{Service: "db", Name: "app"}

	// Defaults: the postgres image's own, and the container name as the host —
	// the database is reached container to container and need not be published.
	got := connectionFor(ref, schema.ServiceDecl{})
	if got.Host != "ownbase-db" || got.Port != 5432 || got.User != "postgres" || got.Maintenance != "postgres" {
		t.Errorf("defaults = %+v", got)
	}

	got = connectionFor(ref, schema.ServiceDecl{
		Port: 6543,
		Env:  []string{"POSTGRES_USER=app", "POSTGRES_DB=appdb"},
	})
	if got.Port != 6543 || got.User != "app" || got.Maintenance != "appdb" {
		t.Errorf("overrides = %+v", got)
	}
}

// A password set by hand can contain anything, and an unescaped "@" or "/" would
// produce a URL that parses as a different host entirely.
func TestComposeURL_EscapesCredentials(t *testing.T) {
	conn := connection{Host: "ownbase-postgres", Port: 5432, User: "own base", Maintenance: "postgres"}
	got := composeURL(conn, "p@ss/word", "revolve")
	if want := "postgresql://own%20base:p%40ss%2Fword@ownbase-postgres:5432/revolve"; got != want {
		t.Errorf("composeURL = %q, want %q", got, want)
	}
}
