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
	"github.com/ownbase/ownbase/internal/serverconfig"
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

// defaultOwnbaseYAML is the minimal config seeded into an empty config repo by
// `config setup --init`.
const defaultOwnbaseYAML = `schema_version: v1

# OwnBase configuration — the single source of truth for this Base.
# Edit via ownbasectl (config set / service add / deploy), which commits here.

core:
  caddy:
    # email: you@example.com  # for automatic TLS certificates

services: {}
`

func newConfigSetupCmd() *cobra.Command {
	var repo, ref string
	var doInit bool
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
			return runConfigSetup(args[0], repo, ref, doInit)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "git URL of the config repo (required)")
	cmd.Flags().StringVar(&ref, "ref", "", "branch/ref of the config repo to track (default: main)")
	cmd.Flags().BoolVar(&doInit, "init", false, "seed a default ownbase.yaml into an empty config repo")
	return cmd
}

func runConfigSetup(base, repo, ref string, doInit bool) error {
	if repo == "" {
		return fmt.Errorf("--repo is required, e.g. --repo git@github.com:org/ownbase-config.git")
	}
	if ref == "" {
		ref = serverconfig.DefaultConfigRef
	}

	// Persist to the local profile so subsequent mutations know where to commit.
	if err := saveProfile(base, func(p *serverconfig.ServerProfile) {
		p.ConfigRepoURL = repo
		p.ConfigRef = ref
	}); err != nil {
		return fmt.Errorf("save config repo to profile: %w", err)
	}

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
			fmt.Println("Seeded default ownbase.yaml into the config repo.")
		} else {
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
