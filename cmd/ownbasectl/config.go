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
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read or replace a Base's ownbase.yaml (agent-first, non-interactive)",
		Long: `config get/set read and atomically replace the config repo's
ownbase.yaml over the Base's SSH tunnel. There is no interactive editor:
every invocation is a single command that succeeds or fails with a
non-zero exit code, safe to script or run from an AI agent unattended.`,
	}
	cmd.AddCommand(newConfigGetCmd(), newConfigSetCmd())
	return cmd
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
omitted or "-"), validates it locally, then pushes it through the daemon's
front-door commit path — exactly like a git push to the config repo. The
post-receive hook fires and the daemon reconciles immediately.

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

	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	if err := pushConfig(conn, string(content), message); err != nil {
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

// pushConfig POSTs a complete new ownbase.yaml document to the daemon's
// front-door /config endpoint. Shared by `config set` and every
// `service add/remove/update` command (which read-modify-write the whole
// document rather than calling a per-field API).
func pushConfig(conn *connection, content, message string) error {
	payload, err := json.Marshal(map[string]string{
		"content": content,
		"message": message,
	})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	_, err = apiCall(conn, http.MethodPost, "/config", payload)
	return err
}
