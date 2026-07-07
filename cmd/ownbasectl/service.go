package main

// service.go implements `ownbasectl service add/remove/update` — structured,
// non-interactive commands that read-modify-write the current ownbase.yaml
// on a Base through the same /config front door as `ownbasectl config set`.
// There is no per-field API on the daemon: the whole document is fetched,
// edited locally with the same schema types the daemon itself validates
// against, and pushed back atomically. Every command is a single
// invocation — no editor, no prompts — safe to run from a script or an AI
// agent unattended.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/schema"
)

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Add, remove, or update a service in a Base's ownbase.yaml",
		Long: `service add/remove/update edit the services: map in ownbase.yaml and
push the result through the same front door as 'ownbasectl config set'.
Every command is a single non-interactive invocation — no editor, no
prompts — safe to run from a script or an AI agent.`,
	}
	cmd.AddCommand(newServiceAddCmd(), newServiceRemoveCmd(), newServiceUpdateCmd())
	return cmd
}

// serviceFieldFlags are the ownbase.yaml ServiceDecl fields settable from
// the command line, shared by `service add` and `service update`.
type serviceFieldFlags struct {
	source     string
	mirror     string
	ref        string
	dockerfile string
	context    string
	port       int
	domain     string
	domains    []string
	dataPath   string
	database   string
	requires   []string
	env        []string
}

func (f *serviceFieldFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.source, "source", "", `local bare repo path the user pushes into directly, e.g. "services/auth" (mutually exclusive with --mirror)`)
	fl.StringVar(&f.mirror, "mirror", "", "external git URL to mirror, e.g. https://github.com/org/repo (mutually exclusive with --source)")
	fl.StringVar(&f.ref, "ref", "", "branch, tag, or commit SHA to build from (empty = auto-pin to default branch HEAD)")
	fl.StringVar(&f.dockerfile, "dockerfile", "", `Dockerfile path within the repo (default "Dockerfile")`)
	fl.StringVar(&f.context, "context", "", "build context subdirectory within the repo")
	fl.IntVar(&f.port, "port", 0, "primary port the container listens on")
	fl.StringVar(&f.domain, "domain", "", "public hostname for the Caddy route (empty = internal-only); deprecated alias for a single --domains entry")
	fl.StringSliceVar(&f.domains, "domains", nil, "public hostnames for the Caddy route, one route per domain; repeatable, replaces the full list when passed; combined with --domain and deduplicated")
	fl.StringVar(&f.dataPath, "data-path", "", `mount path for the persistent data volume inside the container (default "/data")`)
	fl.StringVar(&f.database, "database", "", "name of the Postgres database to provision")
	fl.StringSliceVar(&f.requires, "requires", nil, "capability (service key) this service depends on; repeatable; replaces the full list")
	fl.StringArrayVar(&f.env, "env", nil, "KEY=VALUE static environment variable to set; repeatable")
}

func newServiceAddCmd() *cobra.Command {
	var f serviceFieldFlags
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add <name> <service>",
		Short: "Add a new service to a Base's ownbase.yaml",
		Long: `Adds a new service entry. Exactly one of --source or --mirror is
required: --source names a local bare repo that the user (or an agent)
pushes into directly over SSH; --mirror is an external git URL that
OwnBase clones and keeps a local copy of automatically.`,
		Example: `  ownbasectl service add mybase crm --mirror https://github.com/org/crm --ref main --port 3000 --domain crm.example.com
  ownbasectl service add mybase worker --source services/worker`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceAdd(args[0], args[1], f, jsonOut)
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runServiceAdd(base, name string, f serviceFieldFlags, jsonOut bool) error {
	if f.source == "" && f.mirror == "" {
		return fmt.Errorf("either --source or --mirror is required")
	}

	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	cfg, err := fetchRemoteConfig(conn)
	if err != nil {
		return err
	}
	if _, exists := cfg.Services[name]; exists {
		return fmt.Errorf("service %q already exists on %q — use 'ownbasectl service update' to change it", name, base)
	}

	env, err := parseEnvPairs(f.env)
	if err != nil {
		return err
	}

	if cfg.Services == nil {
		cfg.Services = map[string]schema.ServiceDecl{}
	}
	cfg.Services[name] = schema.ServiceDecl{
		Source:     f.source,
		Mirror:     f.mirror,
		Ref:        f.ref,
		Dockerfile: f.dockerfile,
		Context:    f.context,
		Port:       f.port,
		Domain:     f.domain,
		Domains:    f.domains,
		DataPath:   f.dataPath,
		Database:   f.database,
		Requires:   f.requires,
		Env:        env,
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("resulting ownbase.yaml would be invalid: %w", err)
	}
	newContent, err := schema.MarshalConfig(cfg)
	if err != nil {
		return fmt.Errorf("encode ownbase.yaml: %w", err)
	}
	if err := pushConfig(conn, string(newContent), fmt.Sprintf("feat(service): add %s", name)); err != nil {
		return err
	}

	if jsonOut {
		return printJSON(map[string]any{"status": "added", "service": name})
	}
	fmt.Printf("Added service %q to %q — reconcile triggered.\n", name, base)
	return nil
}

func newServiceRemoveCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "remove <name> <service>",
		Short: "Remove a service from a Base's ownbase.yaml",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceRemove(args[0], args[1], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runServiceRemove(base, name string, jsonOut bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	cfg, err := fetchRemoteConfig(conn)
	if err != nil {
		return err
	}
	if _, exists := cfg.Services[name]; !exists {
		return fmt.Errorf("service %q not found on %q", name, base)
	}
	delete(cfg.Services, name)

	newContent, err := schema.MarshalConfig(cfg)
	if err != nil {
		return fmt.Errorf("encode ownbase.yaml: %w", err)
	}
	if err := pushConfig(conn, string(newContent), fmt.Sprintf("feat(service): remove %s", name)); err != nil {
		return err
	}

	if jsonOut {
		return printJSON(map[string]any{"status": "removed", "service": name})
	}
	fmt.Printf("Removed service %q from %q — reconcile triggered.\n", name, base)
	return nil
}

func newServiceUpdateCmd() *cobra.Command {
	var f serviceFieldFlags
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "update <name> <service>",
		Short: "Update fields of an existing service in a Base's ownbase.yaml",
		Long: `Updates only the fields whose flags were explicitly passed; every
other field of the service keeps its current value. --env merges into the
existing env list (new values win on duplicate keys); --requires replaces
the existing capability list entirely when passed.

This is how a ref bump is done: the new ref is fetched into the service's
local bare repo automatically on the next reconcile if it isn't already
present locally (see internal/repos).`,
		Example: `  ownbasectl service update mybase crm --ref v2.3.0
  ownbasectl service update mybase crm --port 4000 --domain crm.example.com`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceUpdate(cmd, args[0], args[1], f, jsonOut)
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runServiceUpdate(cmd *cobra.Command, base, name string, f serviceFieldFlags, jsonOut bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	cfg, err := fetchRemoteConfig(conn)
	if err != nil {
		return err
	}
	decl, exists := cfg.Services[name]
	if !exists {
		return fmt.Errorf("service %q not found on %q — use 'ownbasectl service add' to create it", name, base)
	}

	changed := cmd.Flags().Changed
	if changed("source") {
		decl.Source = f.source
		decl.Mirror = ""
	}
	if changed("mirror") {
		decl.Mirror = f.mirror
		decl.Source = ""
	}
	if changed("ref") {
		decl.Ref = f.ref
	}
	if changed("dockerfile") {
		decl.Dockerfile = f.dockerfile
	}
	if changed("context") {
		decl.Context = f.context
	}
	if changed("port") {
		decl.Port = f.port
	}
	if changed("domain") {
		decl.Domain = f.domain
	}
	if changed("domains") {
		decl.Domains = f.domains
	}
	if changed("data-path") {
		decl.DataPath = f.dataPath
	}
	if changed("database") {
		decl.Database = f.database
	}
	if changed("requires") {
		decl.Requires = f.requires
	}
	if changed("env") {
		newEnv, err := parseEnvPairs(f.env)
		if err != nil {
			return err
		}
		decl.Env = mergeEnvPairs(decl.Env, newEnv)
	}

	cfg.Services[name] = decl

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("resulting ownbase.yaml would be invalid: %w", err)
	}
	newContent, err := schema.MarshalConfig(cfg)
	if err != nil {
		return fmt.Errorf("encode ownbase.yaml: %w", err)
	}
	if err := pushConfig(conn, string(newContent), fmt.Sprintf("chore(service): update %s", name)); err != nil {
		return err
	}

	if jsonOut {
		return printJSON(map[string]any{"status": "updated", "service": name})
	}
	fmt.Printf("Updated service %q on %q — reconcile triggered.\n", name, base)
	return nil
}

// fetchRemoteConfig GETs the Base's current ownbase.yaml over the SSH
// tunnel and parses it with the same validation the daemon itself applies.
func fetchRemoteConfig(conn *connection) (*schema.OwnbaseConfig, error) {
	body, err := apiGet(conn, "/config")
	if err != nil {
		return nil, err
	}
	cfg, err := schema.ParseConfig(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse current ownbase.yaml from Base: %w", err)
	}
	return cfg, nil
}

// parseEnvPairs validates that every entry is a well-formed KEY=VALUE pair.
func parseEnvPairs(pairs []string) ([]string, error) {
	for _, p := range pairs {
		if !strings.Contains(p, "=") {
			return nil, fmt.Errorf("invalid --env value %q: must be KEY=VALUE", p)
		}
	}
	return pairs, nil
}

// mergeEnvPairs merges newPairs into existing (both "KEY=VALUE" strings),
// with newPairs overwriting existing entries for the same key. The result
// is sorted by key for a deterministic diff.
func mergeEnvPairs(existing, newPairs []string) []string {
	merged := make(map[string]string)
	for _, kv := range existing {
		k, v, _ := strings.Cut(kv, "=")
		merged[k] = v
	}
	for _, kv := range newPairs {
		k, v, _ := strings.Cut(kv, "=")
		merged[k] = v
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+merged[k])
	}
	return out
}

// printJSON encodes v as indented JSON to stdout.
func printJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	fmt.Println(string(out))
	return nil
}
