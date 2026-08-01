package main

// config.go implements `ownbasectl config get/set` — the agent-first
// interface to a Base's ownbase.yaml. Every invocation is a single
// non-interactive command: no editor, no prompts, exit code 0/non-zero.
// `get` reads the checkout's current document over the SSH tunnel; `set`
// validates a whole new document locally and pushes it through the
// daemon's front-door commit path (POST /config) — the same path a user's
// own `git push` to the config bare repo takes.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/vault"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Set up, read, or replace a Base's ownbase.yaml (agent-first, non-interactive)",
		Long: `config setup points the Base at an external git config repo;
config get reads the current ownbase.yaml; config set replaces it. set edits
the external config repo client-side (with your own git credentials), pushes,
and asks the Base to reconcile. Every invocation is a single non-interactive
command, safe to script or run from an AI agent.`,
	}
	cmd.AddCommand(newConfigSetupCmd(), newConfigGetCmd(), newConfigSetCmd())
	return cmd
}

// pgBackRestRef pins the commit of github.com/ownbase-ai/pgbackrest that the
// scaffolded config builds both the repository host and the Postgres image
// from. Bumping the Postgres/pgBackRest pair is a one-line change here.
const pgBackRestRef = "3ec931b9e2afe5eec934d46442031d21019c2da3"

// defaultOwnbaseYAML is the config seeded into an empty config repo by
// `config setup --init`.
//
// It ships a working Postgres with point-in-time recovery rather than an empty
// services: map, because almost every Base needs a database and the settings
// that make one recoverable are exactly the settings nobody discovers on their
// own — the AppArmor exception Postgres cannot start without, the capabilities
// sshd needs, and the decision to back up the pgBackRest repository instead of
// the live data directory. Each is spelled out here, with a comment explaining
// what breaks if it is removed. Delete both services if this Base needs no
// database; nothing else depends on them.
//
// The %s is pgBackRestRef, interpolated in defaultOwnbaseYAMLContent.
const defaultOwnbaseYAMLTemplate = `schema_version: v1

# OwnBase configuration — the single source of truth for this Base.
# Edit via ownbasectl (config set / service add / deploy), which commits here.

core:
  caddy:
    # email: you@example.com  # for automatic TLS certificates

  backup:
    # Set a restic repository to enable off-machine backups, then store the
    # credentials with: ownbasectl secrets set <base> backup RESTIC_PASSWORD=...
    #   repo: s3:s3.us-east-2.amazonaws.com/my-bucket/my-base
    repo: ""
    interval: 1h
    verify_interval: 24h

services:
  # pgBackRest repository host. Owns the WAL archive and the base backups; the
  # postgres container pushes to it over SSH. Its repo volume is what restic
  # ships off the machine, so Postgres recovery never depends on a file-level
  # copy of a live data directory.
  pgbackrest:
    repo: https://github.com/ownbase-ai/pgbackrest
    ref: %[1]s
    context: "."
    # OwnBase always emits DropCapability=ALL. sshd needs SETUID/SETGID to drop
    # privileges to the pgbackrest user, and SYS_CHROOT because its pre-auth
    # privilege-separation child chroots into /run/sshd. Without SYS_CHROOT sshd
    # listens but every connection is reset during key exchange.
    add_capabilities:
      - SETUID
      - SETGID
      - SYS_CHROOT
    # The Base generates this keypair on first reconcile and keeps both halves
    # age-encrypted: the public half here, the private half on postgres, which
    # is the end that dials in. No ssh-keygen, and the private key never leaves
    # the machine.
    generated_secrets:
      - type: ssh-ed25519
        public_key: PGBACKREST_CLIENT_PUBKEY
        private_key: postgres:PGBACKREST_SSH_KEY_B64
        # A PEM does not survive a trip through an environment variable.
        private_encoding: base64
    volumes:
      - name: repo
        mount: /var/lib/pgbackrest
        # The one volume that makes Postgres recoverable from off-machine backup.
        backup: ["."]
      - name: log
        mount: /var/log/pgbackrest

  # Postgres 17 with the pgBackRest client. The image pre-owns every writable
  # path as UID 999 and sets USER postgres, so unlike the plain docker-library
  # image it needs no added capabilities.
  #
  # PGDATA is deliberately unset: the image default (/var/lib/postgresql/data)
  # is what pgBackRest's pg1-path points at.
  postgres:
    repo: https://github.com/ownbase-ai/pgbackrest
    ref: %[1]s
    context: "postgres"
    port: 5432
    requires:
      - pgbackrest
    generated_secrets:
      - type: password
        key: POSTGRES_PASSWORD
    volumes:
      - name: data
        mount: /var/lib/postgresql/data
        # No backup: key — pgBackRest owns Postgres recovery. A restic file copy
        # of a live data directory is crash-inconsistent and cannot do PITR.
    env:
      - POSTGRES_USER=ownbase
      - POSTGRES_DB=ownbase
      # Inter-container DNS resolves the container name, not the service key.
      - PGBACKREST_HOST=ownbase-pgbackrest
      - PGBACKREST_STANZA=main
    # Required, and not optional: Podman's containers-default AppArmor profile
    # denies signals between the container's own processes, so the startup
    # process cannot signal the checkpointer. Postgres then dies with
    # "could not signal for checkpoint: Permission denied" at the end-of-recovery
    # checkpoint, on every start. CAP_KILL does not substitute — this is
    # AppArmor mediation, not the kill(2) UID check.
    security_opt:
      - apparmor=unconfined
`

// defaultOwnbaseYAML is the seeded config with pgBackRestRef interpolated.
var defaultOwnbaseYAML = fmt.Sprintf(defaultOwnbaseYAMLTemplate, pgBackRestRef)

func newConfigSetupCmd() *cobra.Command {
	var repo, ref string
	var doInit, jsonOut bool
	cmd := &cobra.Command{
		Use:   "setup <name> --repo <git-url>",
		Short: "Point a Base at its external config repo (optionally seeding it)",
		Long: `setup records the external git repo that holds this Base's
ownbase.yaml, both in the local profile and on the Base (which then clones it
read-only and reconciles). With --init, an empty config repo is seeded with a
default ownbase.yaml (committed with your git credentials).

The Base needs READ access to the repo — add its deploy key first with
'ownbasectl ssh-key add <name>'.`,
		Example: `  ownbasectl config setup mybase --repo git@github.com:org/ownbase-config.git --init`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSetup(args[0], repo, ref, doInit, jsonOut)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "git URL of the config repo (required)")
	cmd.Flags().StringVar(&ref, "ref", "", "branch/ref of the config repo to track (default: main)")
	cmd.Flags().BoolVar(&doInit, "init", false, "seed a default ownbase.yaml into an empty config repo")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runConfigSetup(base, repo, ref string, doInit, jsonOut bool) error {
	if repo == "" {
		return fmt.Errorf("--repo is required, e.g. --repo git@github.com:org/ownbase-config.git")
	}
	if ref == "" {
		ref = vault.DefaultConfigRef
	}

	// Persist to the profile so subsequent mutations know where to commit.
	if err := saveProfile(base, func(p *vault.Profile) {
		p.ConfigRepoURL = repo
		p.ConfigRef = ref
	}); err != nil {
		return fmt.Errorf("save config repo to profile: %w", err)
	}

	seeded := false
	if doInit {
		profile, err := loadProfile(base)
		if err != nil {
			return err
		}
		cr, err := cloneConfigRepo(profile)
		if err != nil {
			return err
		}
		defer cr.close()
		current, err := cr.readOwnbaseYAML()
		if err != nil {
			return err
		}
		if current == "" {
			if err := cr.writeCommitPush(defaultOwnbaseYAML, "init: seed ownbase.yaml"); err != nil && err != errNoConfigChange {
				return fmt.Errorf("seed config repo: %w", err)
			}
			seeded = true
			if !jsonOut {
				fmt.Println("Seeded default ownbase.yaml into the config repo.")
			}
		} else if !jsonOut {
			fmt.Println("Config repo already has an ownbase.yaml — leaving it untouched.")
		}
	}

	// Tell the Base to adopt the config source (clone + reconcile).
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()
	payload, _ := json.Marshal(map[string]string{"repo_url": repo, "ref": ref})
	if _, err := apiCall(conn, http.MethodPost, "/config/source", payload); err != nil {
		return fmt.Errorf("set config source on Base: %w", err)
	}
	if jsonOut {
		// stdout must be one JSON document — no progress lines above.
		return printJSON(map[string]any{
			"status":   "configured",
			"base":     base,
			"repo_url": repo,
			"ref":      ref,
			"seeded":   seeded,
		})
	}
	fmt.Printf("Config source set to %s (%s) for %q — the Base will pull and reconcile.\n", repo, ref, base)
	return nil
}

func newConfigGetCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Print the Base's current ownbase.yaml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigGet(args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the config as JSON instead of raw YAML")
	return cmd
}

func runConfigGet(base string, jsonOut bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	body, err := apiGet(conn, "/config")
	if err != nil {
		return err
	}

	if !jsonOut {
		fmt.Print(string(body))
		return nil
	}

	var doc any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("parse ownbase.yaml from Base: %w", err)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

func newConfigSetCmd() *cobra.Command {
	var file, message string
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Atomically replace the Base's ownbase.yaml",
		Long: `Reads a complete new ownbase.yaml from --file (or stdin when --file is
omitted or "-"), validates it locally, commits it to the external config repo
(with your own git credentials), pushes, and asks the Base to reconcile.

Exit code is non-zero on validation failure or transport error, so this is
safe to call unattended from a script or an AI agent.`,
		Example: `  ownbasectl config set mybase --file ./ownbase.yaml
  cat ownbase.yaml | ownbasectl config set mybase`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSet(args[0], file, message)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to the new ownbase.yaml (default: read from stdin)")
	cmd.Flags().StringVar(&message, "message", "", "commit message (default: a generic ownbasectl message)")
	return cmd
}

func runConfigSet(base, file, message string) error {
	content, err := readConfigInput(file)
	if err != nil {
		return err
	}
	if _, err := schema.ParseConfig(bytes.NewReader(content)); err != nil {
		return fmt.Errorf("new ownbase.yaml is invalid: %w", err)
	}
	if message == "" {
		message = "chore(config): update ownbase.yaml via ownbasectl"
	}

	err = mutateConfig(base, func(_ string) (string, string, error) {
		return string(content), message, nil
	})
	if err == errNoConfigChange {
		fmt.Printf("Config on %q is already up to date — nothing to do.\n", base)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("Config updated on %q — reconcile triggered.\n", base)
	return nil
}

// readConfigInput reads the new ownbase.yaml content from file, or from
// stdin when file is empty or "-".
func readConfigInput(file string) ([]byte, error) {
	if file == "" || file == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read new config from stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return data, nil
}
