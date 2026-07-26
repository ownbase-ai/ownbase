// Package gendb provisions the databases a config declares via database: and
// hands each service the URL to reach its own.
//
// The declaration names the provider explicitly:
//
//	api:
//	  requires: [postgres]
//	  database: postgres/revolve
//
// On reconcile, Ensure creates that database if it does not exist and writes a
// DATABASE_URL into the *consumer's* age-encrypted secrets file, composed from
// the provider's user and password. From there the normal secrets path
// (internal/podman's injectSecrets) turns it into an environment variable, so
// the credential appears in neither ownbase.yaml nor the unit file — the same
// treatment every other secret gets.
//
// Two properties make this safe on every reconcile tick:
//
//   - It converges rather than acting. A database that exists is left alone, and
//     a URL that already matches is not rewritten, so a Base that has been up
//     for a month does no work here and its containers are not restarted.
//   - It is allowed to be too early. Provisioning needs the provider's Postgres
//     to be running, which on a Base's first reconcile it is not yet. Ensure
//     reports that as a skip rather than an error, and the next tick — a minute
//     later — completes it.
package gendb

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secrets"
)

// Config locates the secret store and the container runtime Ensure uses.
type Config struct {
	// SecretsDir holds the age-encrypted per-service files
	// (<SecretsDir>/<service>.yaml.age). Empty means /opt/ownbase/secrets.
	SecretsDir string

	// AgeKeyPath is the Base's age private key. Empty means
	// secrets.DefaultKeyPath.
	AgeKeyPath string

	// Runner runs commands inside the provider's container. Empty means
	// Podman.
	Runner Runner
}

// DefaultSecretsDir is the conventional location of the age-encrypted
// per-service secrets files.
const DefaultSecretsDir = "/opt/ownbase/secrets"

// Runner runs commands inside a container. It exists so provisioning can be
// tested without a container runtime.
type Runner interface {
	// Exec runs a command inside the named container and returns its stdout.
	Exec(ctx context.Context, container string, args ...string) ([]byte, error)

	// Running reports whether the named container is up.
	Running(ctx context.Context, container string) bool
}

// Result reports what Ensure did, for logging. No credential ever appears here.
type Result struct {
	// Created lists databases that did not exist and now do, as
	// "provider/dbname".
	Created []string

	// Wired lists the services whose DATABASE_URL was written or updated.
	Wired []string

	// Skipped explains each declaration that could not be provisioned yet,
	// one sentence per entry. These are expected during a first bring-up and
	// are not errors.
	Skipped []string
}

// Ensure creates every declared database that is missing and writes each
// consumer's DATABASE_URL.
//
// A declaration that cannot be provisioned yet — its provider is not running,
// or has no password to compose a URL from — is recorded in Skipped and retried
// on the next reconcile. Only a genuine failure (a psql error, an unwritable
// secrets file) is returned as an error.
func Ensure(ctx context.Context, cfg *schema.OwnbaseConfig, opts Config) (Result, error) {
	var result Result
	if cfg == nil {
		return result, nil
	}

	runner := opts.Runner
	if runner == nil {
		runner = PodmanRunner{}
	}

	// Sorted order keeps logs and tests deterministic.
	for _, name := range sortedKeys(cfg.Services) {
		svc := cfg.Services[name]
		ref, ok := svc.DatabaseRef()
		if !ok {
			continue
		}
		provider, ok := cfg.Services[ref.Service]
		if !ok {
			// Validation rejects this, so reaching it means a config was
			// loaded another way. Say so rather than panicking on a nil map.
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s: provider %q is not a service in this config", name, ref.Service))
			continue
		}

		conn := connectionFor(ref, provider)
		container := containerName(ref.Service)

		password, err := opts.readSecret(ref.Service, postgresPasswordKey)
		if err != nil {
			return result, fmt.Errorf("read %s secrets: %w", ref.Service, err)
		}
		if password == "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf(
				"%s: %s has no %s yet, so there is no URL to compose — declare it under generated_secrets: on %s or set it with 'ownbasectl secrets set'",
				name, ref.Service, postgresPasswordKey, ref.Service))
			continue
		}

		if !runner.Running(ctx, container) {
			result.Skipped = append(result.Skipped, fmt.Sprintf(
				"%s: %s is not running yet, so %q cannot be created — this resolves itself once it starts",
				name, ref.Service, ref.Name))
			continue
		}

		created, err := ensureDatabase(ctx, runner, container, conn, ref.Name)
		if err != nil {
			return result, fmt.Errorf("create database %q on %s: %w", ref.Name, ref.Service, err)
		}
		if created {
			result.Created = append(result.Created, ref.Service+"/"+ref.Name)
		}

		wired, err := opts.wireURL(name, composeURL(conn, password, ref.Name))
		if err != nil {
			return result, fmt.Errorf("store %s for %q: %w", schema.DatabaseURLKey, name, err)
		}
		if wired {
			result.Wired = append(result.Wired, name)
		}
	}

	return result, nil
}

// postgresPasswordKey is the provider secret the URL's password comes from. It
// is the same key the postgres image reads to set the superuser's password, so
// a provider that works at all has it.
const postgresPasswordKey = "POSTGRES_PASSWORD"

// defaultPostgresPort is the port used when the provider declares none.
const defaultPostgresPort = 5432

// connection is how to reach a provider's Postgres.
type connection struct {
	// Host is the provider's container name, which resolves on the shared
	// network. Not a published port: the database is reached container to
	// container, and does not have to be exposed to the host at all.
	Host string
	Port int

	// User is the provider's bootstrap superuser (POSTGRES_USER).
	User string

	// Maintenance is the database to connect to in order to create another
	// one, since CREATE DATABASE has to be issued from somewhere else.
	Maintenance string
}

func connectionFor(ref schema.DatabaseRef, provider schema.ServiceDecl) connection {
	conn := connection{
		Host: containerName(ref.Service),
		Port: provider.Port,
		User: envValue(provider.Env, "POSTGRES_USER"),
	}
	if conn.Port == 0 {
		conn.Port = defaultPostgresPort
	}
	if conn.User == "" {
		conn.User = "postgres"
	}
	// libpq connects to the user's own database when none is named, and the
	// postgres image creates POSTGRES_DB for the superuser at initdb time.
	conn.Maintenance = envValue(provider.Env, "POSTGRES_DB")
	if conn.Maintenance == "" {
		conn.Maintenance = conn.User
	}
	return conn
}

func containerName(service string) string { return "ownbase-" + service }

// ensureDatabase creates the database if it is absent, and reports whether it
// had to. The existence check is separate from the create so that the common
// case — the database is already there — is a read and produces no log noise.
func ensureDatabase(ctx context.Context, runner Runner, container string, conn connection, dbname string) (bool, error) {
	exists, err := databaseExists(ctx, runner, container, conn, dbname)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	// The name is a validated identifier (schema.isDatabaseName), and is
	// double-quoted here so that a name matching a keyword still works.
	_, err = psql(ctx, runner, container, conn, fmt.Sprintf(`CREATE DATABASE "%s"`, dbname))
	if err != nil {
		// Two reconciles racing, or a database created between the check and
		// the create, is the outcome asked for either way.
		if strings.Contains(err.Error(), "already exists") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func databaseExists(ctx context.Context, runner Runner, container string, conn connection, dbname string) (bool, error) {
	out, err := psql(ctx, runner, container, conn,
		fmt.Sprintf("select 1 from pg_database where datname = '%s'", dbname))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

func psql(ctx context.Context, runner Runner, container string, conn connection, query string) ([]byte, error) {
	return runner.Exec(ctx, container,
		"psql", "-tAX", "--no-psqlrc", "-v", "ON_ERROR_STOP=1",
		"-U", conn.User, "-d", conn.Maintenance, "-c", query)
}

// composeURL builds the connection URL handed to the consumer.
//
// url.UserPassword escapes the credentials, which matters because the password
// may have been set by hand rather than generated: an unescaped "@" or "/" in it
// would produce a URL that parses as a different host entirely.
func composeURL(conn connection, password, dbname string) string {
	u := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(conn.User, password),
		Host:   net.JoinHostPort(conn.Host, strconv.Itoa(conn.Port)),
		Path:   "/" + dbname,
	}
	return u.String()
}

// wireURL writes the composed URL into the consumer's secrets file, and reports
// whether it changed anything.
//
// It overwrites an existing DATABASE_URL that differs, rather than leaving the
// first one written forever: declaring database: is a statement that OwnBase
// owns this value, and a rotated provider password has to reach the consumer
// somehow. A service whose URL should not be managed simply does not declare
// database:.
func (c Config) wireURL(service, dbURL string) (bool, error) {
	existing, err := secrets.IssueMap(c.custody(), c.fileFor(service))
	if err != nil {
		return false, err
	}
	if existing == nil {
		existing = map[string]string{}
	}
	if existing[schema.DatabaseURLKey] == dbURL {
		return false, nil
	}
	existing[schema.DatabaseURLKey] = dbURL
	return true, c.write(service, existing)
}

func (c Config) readSecret(service, key string) (string, error) {
	values, err := secrets.IssueMap(c.custody(), c.fileFor(service))
	if err != nil {
		return "", err
	}
	return values[key], nil
}

func (c Config) secretsDir() string {
	if c.SecretsDir != "" {
		return c.SecretsDir
	}
	return DefaultSecretsDir
}

func (c Config) custody() secrets.FileKeyCustody {
	return secrets.FileKeyCustody{Path: c.AgeKeyPath}
}

func (c Config) fileFor(service string) string {
	return filepath.Join(c.secretsDir(), service+".yaml.age")
}

// write encrypts values to the Base's age recipient and replaces the service's
// secrets file. Values are the full merged set, not a delta.
func (c Config) write(service string, values map[string]string) error {
	id, err := c.custody().LoadIdentity()
	if err != nil {
		return err
	}
	ciphertext, err := secrets.EncryptSecrets(id.Recipient(), values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.secretsDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.fileFor(service), ciphertext, 0o600)
}

// envValue reads KEY=VALUE from a service's env: list.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
