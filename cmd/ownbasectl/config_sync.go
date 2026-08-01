package main

// config_sync.go keeps the vault profile's ConfigRepoURL in step with what
// the Base is actually tracking. The profile can lag — adopt never copied it,
// or config setup ran on another laptop — and then the app shows "not set up
// yet" while services are clearly running from a real ownbase.yaml.
//
// Every status/checkup path runs ensureConfigKnown so a single successful
// connect repairs the profile and, for older daemons that do not yet emit
// status.config, injects the source into the JSON the UI already reads.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ownbase/ownbase/internal/tunnel"
	"github.com/ownbase/ownbase/internal/vault"
)

// ensureConfigKnown makes sure body carries a config section when the Base
// has one, and that the vault profile matches. Never fails the caller: a
// missing source or a vault write error is non-fatal for status display.
func ensureConfigKnown(base string, body []byte) []byte {
	url, ref := configFromStatusJSON(body)
	if url == "" {
		url, ref = configFromBaseSSH(base)
	}
	if url == "" {
		return body
	}
	syncProfileConfig(base, url, ref)
	if existing, _ := configFromStatusJSON(body); existing == "" {
		if enriched, err := injectStatusConfig(body, url, ref); err == nil {
			return enriched
		}
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

// configFromBaseSSH reads /opt/ownbase/config-source.yaml over SSH. Used when
// the daemon is older than status.config and the vault profile has no URL.
func configFromBaseSSH(base string) (url, ref string) {
	target, _, err := baseTarget(base)
	if err != nil {
		return "", ""
	}
	out, err := tunnel.RunCommand(target,
		"sudo cat /opt/ownbase/config-source.yaml 2>/dev/null || cat /opt/ownbase/config-source.yaml 2>/dev/null")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", ""
	}
	var src struct {
		RepoURL string `yaml:"repo_url"`
		Ref     string `yaml:"ref"`
	}
	if err := yaml.Unmarshal([]byte(out), &src); err != nil {
		return "", ""
	}
	return strings.TrimSpace(src.RepoURL), strings.TrimSpace(src.Ref)
}

func syncProfileConfig(base, url, ref string) {
	p, err := loadProfile(base)
	if err != nil {
		return
	}
	if p.ConfigRepoURL == url && (ref == "" || p.ConfigRef == ref || (p.ConfigRef == "" && ref == vault.DefaultConfigRef)) {
		return
	}
	if err := saveProfile(base, func(p *vault.Profile) {
		p.ConfigRepoURL = url
		if ref != "" {
			p.ConfigRef = ref
		} else if p.ConfigRef == "" {
			p.ConfigRef = vault.DefaultConfigRef
		}
	}); err != nil {
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
