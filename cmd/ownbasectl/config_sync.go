package main

// config_sync.go keeps the vault profile's ConfigRepoURL in step with what
// the Base is actually tracking. The profile can lag — adopt used to skip
// it, or config setup ran on another laptop — and then the app shows "not
// set up yet" while services are clearly running from a real ownbase.yaml.
//
// adopt reads config-source.yaml over SSH before saving the profile.
// status/checkup run ensureConfigKnown so a later connect still repairs an
// older profile and, for daemons that do not yet emit status.config, injects
// the source into the JSON the UI already reads.

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

// ensureConfigKnown makes sure body carries a config section when the Base
// has one, and that the vault profile matches. Never fails the caller: a
// missing source or a vault write error is non-fatal for status display.
//
// Order: status JSON (new daemons) → vault profile (already backfilled) →
// SSH read of config-source.yaml (older daemons / first repair only). The
// profile check avoids an extra SSH round-trip on every status after adopt
// or a previous backfill.
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

// applyConfigSource sets profile config fields from a Base-reported source.
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

func syncProfileConfig(base, url, ref string) {
	p, ok := tryLoadProfile(base)
	if !ok {
		return
	}
	if p.ConfigRepoURL == url && (ref == "" || p.ConfigRef == ref || (p.ConfigRef == "" && ref == vault.DefaultConfigRef)) {
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
