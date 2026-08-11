package main

// config_sync.go keeps the vault profile's ConfigRepoURL aligned with a
// legitimate Base, without letting a compromised Base rewrite the vault.
//
// The vault is the operator's pin of which config repo this Base should
// track. status/checkup may *backfill* an empty profile from the Base (older
// installs, adopt on another laptop), but never overwrite a non-empty vault
// URL when the Base reports something different — that mismatch is a signal,
// not a sync opportunity (a rooted Base can rewrite config-source.yaml).
//
// adopt still reads config-source.yaml over SSH before first save.
// status/checkup also inject status.config for older daemons that omit it.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ownbase/ownbase/internal/agentd"
	"github.com/ownbase/ownbase/internal/tunnel"
	"github.com/ownbase/ownbase/internal/vault"
)

// ensureConfigKnown makes sure body carries a config section when known, and
// may backfill an *empty* vault profile from the Base. Never fails the caller:
// a missing source or a vault write error is non-fatal for status display.
//
// Order: status JSON (new daemons) → vault profile → SSH read of
// config-source.yaml (older daemons / first backfill only).
//
// Vault access here is best-effort against an already-running agent — never
// EnsureRunning. status/checkup only reach this after connectToServer, so the
// agent is up; calling EnsureRunning from a `go test` binary would spawn the
// test binary as "agent run" and re-enter the whole suite.
func ensureConfigKnown(base string, body []byte) []byte {
	if url, ref := configFromStatusJSON(body); url != "" {
		syncProfileConfig(base, url, ref)
		return body
	}

	if p, ok := tryLoadProfile(base); ok {
		if url := strings.TrimSpace(p.ConfigRepoURL); url != "" {
			ref := strings.TrimSpace(p.ConfigRef)
			if enriched, err := injectStatusConfig(body, url, ref); err == nil {
				return enriched
			}
			return body
		}
	}

	// No status.config and no profile URL. Only open SSH when the Base looks
	// configured (services present) — a fresh install with no config yet must
	// not pay an SSH round-trip on every status/checkup. adopt already copies
	// config-source.yaml when registering; this path is for older profiles.
	if !statusHasServices(body) {
		return body
	}

	url, ref := configFromBaseSSH(base)
	if url == "" {
		return body
	}
	syncProfileConfig(base, url, ref)
	if enriched, err := injectStatusConfig(body, url, ref); err == nil {
		return enriched
	}
	return body
}

func statusHasServices(body []byte) bool {
	var doc struct {
		Services []json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return false
	}
	return len(doc.Services) > 0
}

func configFromStatusJSON(body []byte) (url, ref string) {
	var doc struct {
		Config *struct {
			RepoURL string `json:"repo_url"`
			Ref     string `json:"ref"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Config == nil {
		return "", ""
	}
	return strings.TrimSpace(doc.Config.RepoURL), strings.TrimSpace(doc.Config.Ref)
}

// configSourceReadCmd fetches the on-Base config-source state file. Prefer
// sudo so a non-root SSH user still works; fall back to a plain read for
// installs where the file is world-readable. A var so tests can point at a
// temp file through the in-process SSH server.
var configSourceReadCmd = "sudo cat /opt/ownbase/config-source.yaml 2>/dev/null || cat /opt/ownbase/config-source.yaml 2>/dev/null"

// configFromBaseSSH reads /opt/ownbase/config-source.yaml over SSH via the
// Base's vault profile. Used when the daemon is older than status.config.
// Uses tryLoadProfile so a missing agent cannot start the test binary.
func configFromBaseSSH(base string) (url, ref string) {
	p, ok := tryLoadProfile(base)
	if !ok || strings.TrimSpace(p.Host) == "" {
		return "", ""
	}
	target, err := sshTarget(p)
	if err != nil {
		return "", ""
	}
	return configFromTarget(target)
}

// configFromTarget reads the config source over an already-resolved SSH
// target — adopt uses this before the profile is saved.
func configFromTarget(target tunnel.Target) (url, ref string) {
	out, err := tunnel.RunCommand(target, configSourceReadCmd)
	if err != nil || strings.TrimSpace(out) == "" {
		return "", ""
	}
	return parseConfigSourceYAML([]byte(out))
}

func parseConfigSourceYAML(data []byte) (url, ref string) {
	var src struct {
		RepoURL string `yaml:"repo_url"`
		Ref     string `yaml:"ref"`
	}
	if err := yaml.Unmarshal(data, &src); err != nil {
		return "", ""
	}
	return strings.TrimSpace(src.RepoURL), strings.TrimSpace(src.Ref)
}

// applyConfigSource sets profile config fields from a trusted source
// (config setup, adopt, or empty-profile backfill). Callers that must not
// overwrite a pin use syncProfileConfig instead.
func applyConfigSource(p *vault.Profile, url, ref string) {
	if url == "" {
		return
	}
	p.ConfigRepoURL = url
	if ref != "" {
		p.ConfigRef = ref
	} else if p.ConfigRef == "" {
		p.ConfigRef = vault.DefaultConfigRef
	}
}

// tryLoadProfile reads a profile only if the credential agent is already up.
// It never starts the agent (see ensureConfigKnown).
func tryLoadProfile(base string) (vault.Profile, bool) {
	c, err := agentd.NewClient()
	if err != nil {
		return vault.Profile{}, false
	}
	p, err := c.Get(base)
	if err != nil {
		return vault.Profile{}, false
	}
	return p, true
}

// syncProfileConfig backfills the vault when the profile has no config URL
// yet. If the vault already pins a different URL than the Base reports, it
// warns and leaves the vault unchanged — a compromised Base must not rewrite
// the operator's pin.
func syncProfileConfig(base, url, ref string) {
	if url == "" {
		return
	}
	p, ok := tryLoadProfile(base)
	if !ok {
		return
	}
	pinned := strings.TrimSpace(p.ConfigRepoURL)
	if pinned != "" {
		if pinned == url {
			// Same repo; optionally fill a missing ref.
			if ref != "" && p.ConfigRef == "" {
				p.ConfigRef = ref
				c, err := agentd.NewClient()
				if err != nil {
					return
				}
				if err := c.Put(base, p); err != nil {
					fmt.Fprintf(os.Stderr, "ownbasectl: warning: could not save config ref to the vault: %v\n", err)
				}
			}
			return
		}
		fmt.Fprintf(os.Stderr,
			"ownbasectl: warning: Base %q reports config repo %s but the vault pins %s — leaving the vault unchanged\n"+
				"  If you intended to repoint this Base, run: ownbasectl config setup %s --repo <url>\n"+
				"  If you did not, investigate the Base (config-source.yaml may have been rewritten)\n",
			base, url, pinned, base)
		return
	}
	applyConfigSource(&p, url, ref)
	c, err := agentd.NewClient()
	if err != nil {
		return
	}
	if err := c.Put(base, p); err != nil {
		fmt.Fprintf(os.Stderr, "ownbasectl: warning: could not save config repo to the vault: %v\n", err)
	}
}

func injectStatusConfig(body []byte, url, ref string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	cfg := struct {
		RepoURL string `json:"repo_url"`
		Ref     string `json:"ref,omitempty"`
	}{RepoURL: url, Ref: ref}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	doc["config"] = raw
	return json.Marshal(doc)
}
