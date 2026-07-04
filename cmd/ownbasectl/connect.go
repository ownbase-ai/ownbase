package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/serverconfig"
	"github.com/ownbase/ownbase/internal/tunnel"
)

// extractStatusSection returns one top-level section of the status payload
// (e.g. "updates", "security") as raw JSON. Used by the --json flags of the
// section-specific commands so they emit only their slice of the payload,
// not the whole status document. Returns "{}" when the section is absent.
func extractStatusSection(body []byte, key string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse status JSON: %w", err)
	}
	section, ok := doc[key]
	if !ok {
		return []byte("{}"), nil
	}
	return section, nil
}

// connection holds the resources needed to make authenticated API calls to a
// remote Base. Call close() when done to tear down the SSH tunnel.
type connection struct {
	baseURL string // e.g. "http://127.0.0.1:54321"
	token   string // Bearer token
	tun     *tunnel.Tunnel
}

func (c *connection) close() {
	if c.tun != nil {
		c.tun.Close()
	}
}

// connectToServer opens an SSH tunnel to the named Base, bootstraps the API
// token if the profile does not have one yet, and returns a ready
// connection.
//
// Callers must call conn.close() when done.
func connectToServer(serverName string) (*connection, error) {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return nil, fmt.Errorf("locate config: %w", err)
	}

	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	profile, err := cfg.ProfileFor(serverName)
	if err != nil {
		return nil, err
	}

	if profile.Host == "" {
		return nil, fmt.Errorf("server profile has no host configured")
	}

	tun, err := tunnel.Open(
		profile.Host,
		profile.EffectiveSSHUser(),
		profile.EffectiveSSHKey(),
		profile.EffectiveAPIPort(),
		profile.EffectiveSSHPort(),
	)
	if err != nil {
		return nil, fmt.Errorf("open SSH tunnel to %s: %w", profile.Host, err)
	}

	tok := profile.Token

	// If the profile has no token cached, try to read it from the Base via SSH
	// and persist it to the local profile.
	if tok == "" {
		fetched, ferr := tunnel.RunCommand(
			profile.Host,
			profile.EffectiveSSHUser(),
			profile.EffectiveSSHKey(),
			"sudo cat /opt/ownbase/api-token 2>/dev/null || cat /opt/ownbase/api-token 2>/dev/null",
			profile.EffectiveSSHPort(),
		)
		if ferr == nil && fetched != "" {
			tok = strings.TrimSpace(fetched)
			// Persist the token to the local profile.
			if serverName != "" {
				p := cfg.Servers[serverName]
				p.Token = tok
				cfg.Servers[serverName] = p
				if saveErr := serverconfig.Save(cfgPath, cfg); saveErr != nil {
					fmt.Fprintf(os.Stderr, "ownbasectl: warning: could not save token to profile: %v\n", saveErr)
				}
			}
		}
	}

	if tok == "" {
		tun.Close()
		return nil, fmt.Errorf("no API token found for %q\n  The token was printed at install time and is stored at /opt/ownbase/api-token on the Base — SSH access to fetch it automatically failed",
			profile.Host)
	}

	return &connection{
		baseURL: "http://" + tun.LocalAddr(),
		token:   tok,
		tun:     tun,
	}, nil
}

// fetchStatusBody GETs the named Base's daemon /status JSON through its SSH
// tunnel. This is the shared entry point for status, updates, security, and
// every other read of the status payload.
func fetchStatusBody(base string, timeout time.Duration) ([]byte, error) {
	conn, err := connectToServer(base)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	statusURL := conn.baseURL + "/status"
	tok := conn.token

	req, err := http.NewRequest(http.MethodGet, statusURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("status API at %s: %w\n  Is the agent running?", statusURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized — the cached token may be stale; remove the profile and run 'ownbasectl adopt' again")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status API returned %d: %s", resp.StatusCode, body)
	}
	return body, nil
}
