package main

// deploy.go implements `ownbasectl deploy <base> <service> [--ref X]` — the
// single, explicit way to move a service to new code. It resolves the
// requested ref to a concrete commit SHA against the service's repo: URL
// (client-side, with the operator's git credentials), writes that SHA into
// ownbase.yaml in the external config repo, commits + pushes it, and triggers
// a reconcile on the Base. There is no server-side branch-tip pinning: the ref
// written here is deterministic.

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/update"
)

func newDeployCmd() *cobra.Command {
	var ref string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "deploy <base> <service>",
		Short: "Resolve a ref to a commit, pin it in ownbase.yaml, and reconcile",
		Long: `deploy resolves --ref (a branch, tag, or commit) to a concrete commit
SHA against the service's repo: URL, commits that SHA to the external config
repo, and triggers a reconcile on the Base. This is the only command that
moves a service to new code — branch-named refs never auto-redeploy.`,
		Example: `  ownbasectl deploy mybase api --ref main
  ownbasectl deploy mybase api --ref v2.3.0`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(args[0], args[1], ref, jsonOut)
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "branch, tag, or commit to deploy (default: the service's current ref, else HEAD)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runDeploy(base, service, ref string, jsonOut bool) error {
	var resolvedSHA string
	err := mutateConfig(base, func(current string) (string, string, error) {
		cfg, err := schema.ParseConfig(strings.NewReader(current))
		if err != nil {
			return "", "", fmt.Errorf("parse current ownbase.yaml: %w", err)
		}
		svc, ok := cfg.Services[service]
		if !ok {
			return "", "", fmt.Errorf("service %q not found on %q — add it with 'ownbasectl service add'", service, base)
		}
		want := ref
		if want == "" {
			want = svc.Ref
		}
		if want == "" {
			want = "HEAD"
		}
		sha, err := resolveRemoteRef(svc.Repo, want)
		if err != nil {
			return "", "", err
		}
		resolvedSHA = sha
		updated, err := update.BumpRef(current, service, svc.Ref, sha)
		if err != nil {
			return "", "", err
		}
		return updated, fmt.Sprintf("deploy(%s): %s", service, sha), nil
	})
	if err == errNoConfigChange {
		if jsonOut {
			return printJSON(map[string]any{"status": "unchanged", "service": service, "ref": resolvedSHA})
		}
		fmt.Printf("%s is already at %s — nothing to deploy.\n", service, shortSHA(resolvedSHA))
		return nil
	}
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(map[string]any{"status": "deployed", "service": service, "ref": resolvedSHA})
	}
	fmt.Printf("Deployed %s at %s on %q — reconcile triggered.\n", service, shortSHA(resolvedSHA), base)
	return nil
}

// resolveRemoteRef resolves ref (branch, tag, or commit) to a full commit SHA
// against repoURL using `git ls-remote`. Annotated tags resolve to the peeled
// commit. When ref is already a full commit SHA that ls-remote cannot look up
// directly, it is accepted as-is (explicit and deterministic).
func resolveRemoteRef(repoURL, ref string) (string, error) {
	if repoURL == "" {
		return "", fmt.Errorf("service has no repo: URL to resolve %q against", ref)
	}
	out, err := exec.Command("git", "ls-remote", repoURL, ref).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s %s: %w", repoURL, ref, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	type entry struct{ sha, name string }
	var entries []entry
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		fields := strings.Fields(ln)
		if len(fields) < 2 {
			continue
		}
		entries = append(entries, entry{sha: fields[0], name: fields[1]})
	}

	// Preference order: peeled tag commit, exact head, exact tag, HEAD, first.
	pick := func(pred func(string) bool) string {
		for _, e := range entries {
			if pred(e.name) {
				return e.sha
			}
		}
		return ""
	}
	if sha := pick(func(n string) bool { return n == "refs/tags/"+ref+"^{}" }); sha != "" {
		return sha, nil
	}
	if sha := pick(func(n string) bool { return n == "refs/heads/"+ref }); sha != "" {
		return sha, nil
	}
	if sha := pick(func(n string) bool { return n == "refs/tags/"+ref }); sha != "" {
		return sha, nil
	}
	if sha := pick(func(n string) bool { return n == "HEAD" }); sha != "" {
		return sha, nil
	}
	if len(entries) > 0 {
		return entries[0].sha, nil
	}
	// No match. Accept a full commit SHA as-is (ls-remote can't query it).
	if looksLikeSHA(ref) {
		return ref, nil
	}
	return "", fmt.Errorf("ref %q not found in %s", ref, repoURL)
}

// looksLikeSHA reports whether s is a full 40-char hex commit SHA.
func looksLikeSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// shortSHA truncates a 40-char SHA to 12 chars for display; other refs are
// returned unchanged.
func shortSHA(s string) string {
	if looksLikeSHA(s) {
		return s[:12]
	}
	return s
}
