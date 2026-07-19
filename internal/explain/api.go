package explain

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ownbase/ownbase/internal/secrets"
)

const (
	// DefaultAPITokenPath is the canonical location of the agent API Bearer
	// token on the Base (written by install.sh, mode 0600).
	DefaultAPITokenPath = "/opt/ownbase/api-token"
)

// APIConfig holds the paths and references the API endpoints need.
type APIConfig struct {
	// SecretsDir is the directory containing age-encrypted secrets files,
	// one per service: <SecretsDir>/<service>.yaml.age. Defaults to
	// /opt/ownbase/secrets/. No configuration in ownbase.yaml is needed.
	SecretsDir string
	// AgeKeyPath is the path to the age private key (default:
	// secrets.DefaultKeyPath). Required for secrets endpoints.
	AgeKeyPath string
	// APITokenPath is the path to the Bearer token file (default:
	// DefaultAPITokenPath). Required for /token/reset.
	APITokenPath string
	// StatusSrv is the running StatusServer whose token will be hot-swapped by
	// /token/reset. Required for /token/reset.
	StatusSrv *StatusServer
	// TriggerScan, when non-nil, starts an immediate vulnerability scan in the
	// background. Returns true if a scan was successfully queued, or false when
	// the daemon is still initializing and the scan cannot be started yet.
	// Called by /security/fix (after upgrade) and /security/scan (on-demand).
	TriggerScan func() bool
	// UpgradeCore, when non-nil, pulls the latest pinned image for the core
	// package (Caddy) and restarts it. Progress lines are written
	// to w so the HTTP handler can stream them to the client. A non-nil error
	// means at least one pull failed; partial progress may have been written.
	UpgradeCore func(w io.Writer) error
	// GetConfig, when non-nil, returns the current contents of ownbase.yaml
	// from the checkout. Called by GET /config — the read side of
	// `ownbasectl config get`.
	GetConfig func() (string, error)
	// Reconcile, when non-nil, pulls the external config repo into the
	// checkout and triggers a reconcile. Called by POST /reconcile — the way
	// every client-side config mutation (deploy, config set, service *,
	// backup setup) asks the Base to apply the just-pushed change.
	Reconcile func() error
	// SetConfigSource, when non-nil, records the external config repo
	// (repo_url + ref) on the Base, (re)clones it, and reconciles. Called by
	// POST /config/source — the write side of `ownbasectl config setup`.
	SetConfigSource func(repoURL, ref string) error
	// EnsureSSHKey, when non-nil, ensures the Base's managed SSH identity
	// exists, optionally records host keys for host, and returns the public
	// key to register as a read-only deploy key. Called by POST /ssh-key.
	EnsureSSHKey func(host string) (publicKey string, err error)
	// GetSSHKey, when non-nil, returns the Base's managed SSH public key (or
	// "" when none exists yet). Called by GET /ssh-key.
	GetSSHKey func() (publicKey string, err error)
	// RunBackup, when non-nil, runs one backup cycle immediately and returns
	// the resulting status. Called by POST /backup/run.
	RunBackup func() (BackupRunStatus, error)
	// CoreStatus, when non-nil, reports the current state of the OwnBase core
	// package (Caddy): pinned image + digest and whether the
	// container is running on the Base. Called by GET /core/status — the
	// endpoint behind `ownbasectl upgrade` (check-only mode).
	CoreStatus func() []CorePackageStatus
}

// CorePackageStatus is the JSON-friendly state of one core package as
// returned by GET /core/status.
type CorePackageStatus struct {
	Name      string `json:"name"`      // e.g. "Caddy"
	Container string `json:"container"` // e.g. "ownbase-core-caddy"
	Image     string `json:"image"`     // e.g. "docker.io/library/caddy:2-alpine"
	Digest    string `json:"digest,omitempty"`
	Running   bool   `json:"running"`
}

// BackupRunStatus is the JSON-friendly result of an immediate backup run,
// returned by POST /backup/run. Mirrors the fields of backup.Status that are
// meaningful to a CLI caller, without requiring api.go to expose the backup
// package's full type in its public signature.
type BackupRunStatus struct {
	LastBackup     string `json:"last_backup,omitempty"`
	LatestSnapshot string `json:"latest_snapshot,omitempty"`
	Restorable     bool   `json:"restorable"`
	LastError      string `json:"last_error,omitempty"`
}

// DefaultSecretsDir is the conventional directory for age-encrypted secrets files.
const DefaultSecretsDir = "/opt/ownbase/secrets"

func (c *APIConfig) effectiveSecretsDir() string {
	if c.SecretsDir != "" {
		return c.SecretsDir
	}
	return DefaultSecretsDir
}

func (c *APIConfig) effectiveAgeKeyPath() string {
	if c.AgeKeyPath != "" {
		return c.AgeKeyPath
	}
	return secrets.DefaultKeyPath
}

func (c *APIConfig) effectiveAPITokenPath() string {
	if c.APITokenPath != "" {
		return c.APITokenPath
	}
	return DefaultAPITokenPath
}

// MountAPI registers the management API routes on mux. All routes require the
// Bearer token from the StatusServer (same token as /status). The caller must
// mount the status API first so the token is set.
func MountAPI(mux *http.ServeMux, cfg APIConfig) {
	// /secrets[/{service}[/{key}]] — list all services, list keys, or operate on a key.
	// Note: ServeMux redirects GET /secrets → GET /secrets/ so the empty-service
	// case (list all) is handled here rather than in a separate /secrets handler.
	mux.HandleFunc("/secrets/", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		// Path: /secrets/, /secrets/{service}, or /secrets/{service}/{key}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/secrets/"), "/", 2)
		service := parts[0]
		key := ""
		if len(parts) == 2 {
			key = parts[1]
		}
		if service == "" {
			// No service specified: list all services with secrets (GET only).
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handleSecretsListAll(w, r, cfg)
			return
		}

		switch {
		case r.Method == http.MethodGet && key == "":
			handleSecretsList(w, r, cfg, service)
		case r.Method == http.MethodGet && key != "":
			handleSecretsGet(w, r, cfg, service, key)
		case r.Method == http.MethodPost && key == "":
			handleSecretsSet(w, r, cfg, service)
		case r.Method == http.MethodDelete && key != "":
			handleSecretsDelete(w, r, cfg, service, key)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// /config — read the current ownbase.yaml from the checkout (read-only).
	// The config repo is external; all mutations are committed client-side by
	// ownbasectl and applied via POST /reconcile.
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.GetConfig == nil {
			http.Error(w, "config read not available", http.StatusNotImplemented)
			return
		}
		content, err := cfg.GetConfig()
		if err != nil {
			http.Error(w, "read config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml; charset=utf-8")
		_, _ = io.WriteString(w, content)
	})

	// /reconcile — pull the external config repo into the checkout and
	// reconcile. Called after a client-side commit+push.
	mux.HandleFunc("/reconcile", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.Reconcile == nil {
			http.Error(w, "reconcile not available", http.StatusNotImplemented)
			return
		}
		if err := cfg.Reconcile(); err != nil {
			http.Error(w, "reconcile: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "reconciling"})
	})

	// /config/source — record the external config repo (repo_url + ref),
	// (re)clone it, and reconcile. The write side of `ownbasectl config setup`.
	mux.HandleFunc("/config/source", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.SetConfigSource == nil {
			http.Error(w, "config source not available", http.StatusNotImplemented)
			return
		}
		var body struct {
			RepoURL string `json:"repo_url"`
			Ref     string `json:"ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.RepoURL) == "" {
			http.Error(w, "repo_url is required", http.StatusBadRequest)
			return
		}
		if err := cfg.SetConfigSource(body.RepoURL, body.Ref); err != nil {
			http.Error(w, "set config source: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "configured", "repo_url": body.RepoURL})
	})

	// /ssh-key — manage the Base's read-only git deploy identity. POST ensures
	// the key (and optionally records a host's keys) and returns the public
	// key; GET returns the current public key.
	mux.HandleFunc("/ssh-key", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if cfg.GetSSHKey == nil {
				http.Error(w, "ssh-key not available", http.StatusNotImplemented)
				return
			}
			pub, err := cfg.GetSSHKey()
			if err != nil {
				http.Error(w, "read ssh key: "+err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]string{"public_key": pub})
		case http.MethodPost:
			if cfg.EnsureSSHKey == nil {
				http.Error(w, "ssh-key not available", http.StatusNotImplemented)
				return
			}
			var body struct {
				Host string `json:"host"`
			}
			// Body is optional; ignore decode errors on an empty body.
			_ = json.NewDecoder(r.Body).Decode(&body)
			pub, err := cfg.EnsureSSHKey(body.Host)
			if err != nil {
				http.Error(w, "ensure ssh key: "+err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]string{"public_key": pub})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// /security/fix — run apt-get update + upgrade and stream the combined output.
	// Only applies to host OS packages; container image upgrades are handled by
	// ownbasectl upgrade. Requires POST. Output is streamed as plain text so the
	// client can print progress in real time.
	mux.HandleFunc("/security/fix", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// apt-get is Linux-only. Return a clear error on other platforms rather
		// than a cryptic "exec: not found" message.
		if _, err := exec.LookPath("apt-get"); err != nil {
			http.Error(w, "apt-get not found — this endpoint is only available on Ubuntu/Debian", http.StatusNotImplemented)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		flusher, canFlush := w.(http.Flusher)
		flush := func() {
			if canFlush {
				flusher.Flush()
			}
		}
		fw := &flushWriter{w: w, flush: flush}

		ctx := r.Context()

		// runStep executes apt-get with its stdout/stderr written directly to
		// the flushing response writer, avoiding any intermediate pipe or
		// scanner that could deadlock on long output lines.
		runStep := func(label string, args ...string) bool {
			fmt.Fprintf(fw, "==> %s\n", label)
			cmd := exec.CommandContext(ctx, "apt-get", args...)
			cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
			cmd.Stdout = fw
			cmd.Stderr = fw
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(fw, "ERROR: apt-get %s: %v\n", args[0], err)
				return false
			}
			return true
		}

		if !runStep("Refreshing package index (apt-get update)", "update", "-q") {
			return
		}
		if !runStep("Upgrading packages (apt-get upgrade)", "upgrade", "-y", "-q") {
			return
		}

		fmt.Fprintf(fw, "\n==> Done. Triggering vulnerability rescan...\n")
		if cfg.TriggerScan != nil {
			if cfg.TriggerScan() {
				fmt.Fprintf(fw, "    Scan started — results available in a few minutes.\n")
				fmt.Fprintf(fw, "    Run 'ownbasectl security' to see updated counts.\n")
			} else {
				fmt.Fprintf(fw, "    Vulnerability scan will refresh on its normal schedule.\n")
				fmt.Fprintf(fw, "    Run 'ownbasectl security' to see updated counts.\n")
			}
		}
		fmt.Fprintf(fw, "---OK---\n")
	})

	// /upgrade — pull updated core package images and restart containers.
	// Streams progress as plain text. After completion, triggers a vuln scan
	// so the operator sees updated CVE counts without waiting for the scheduler.
	mux.HandleFunc("/upgrade", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.UpgradeCore == nil {
			http.Error(w, "upgrade not configured", http.StatusNotImplemented)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		flusher, canFlush := w.(http.Flusher)
		flush := func() {
			if canFlush {
				flusher.Flush()
			}
		}

		// fw is a flushing writer that flushes after every write so the client
		// sees progress lines as they are produced rather than in one burst.
		fw := &flushWriter{w: w, flush: flush}

		if err := cfg.UpgradeCore(fw); err != nil {
			fmt.Fprintf(fw, "\nERROR: %v\n", err)
			return
		}

		fmt.Fprintf(fw, "\n==> Done. Triggering vulnerability rescan...\n")
		if cfg.TriggerScan != nil {
			if cfg.TriggerScan() {
				fmt.Fprintf(fw, "    Scan started — run 'ownbasectl security' in a few minutes to see updated CVE counts.\n")
			} else {
				fmt.Fprintf(fw, "    Vulnerability scan will refresh on its normal schedule.\n")
			}
		}
		fmt.Fprintf(fw, "---OK---\n")
	})

	// /core/status — report the pinned image/digest and running state of the
	// core package (Caddy). Read-only companion to POST /upgrade,
	// used by `ownbasectl upgrade` without --apply.
	mux.HandleFunc("/core/status", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.CoreStatus == nil {
			http.Error(w, "core status not configured", http.StatusNotImplemented)
			return
		}
		writeJSON(w, map[string]any{"packages": cfg.CoreStatus()})
	})

	// /backup/run — trigger an immediate backup snapshot and return once it
	// completes (or fails). Used by `ownbasectl backup run`.
	mux.HandleFunc("/backup/run", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.RunBackup == nil {
			http.Error(w, "backup not configured", http.StatusNotImplemented)
			return
		}
		status, err := cfg.RunBackup()
		if err != nil {
			http.Error(w, "run backup: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, status)
	})

	// /security/scan — trigger an immediate vulnerability scan. Returns quickly;
	// the scan runs asynchronously and updates the cached status when complete.
	mux.HandleFunc("/security/scan", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.TriggerScan == nil {
			http.Error(w, "scan trigger not configured", http.StatusNotImplemented)
			return
		}
		if !cfg.TriggerScan() {
			http.Error(w, "daemon is still initialising — retry in a moment", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]string{
			"status":  "started",
			"message": "Scan started — results available in a few minutes. Check 'ownbasectl security'.",
		})
	})

	// /token/reset — generate a new Bearer token, hot-swap it, persist to file.
	mux.HandleFunc("/token/reset", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		newToken, err := generateToken(32)
		if err != nil {
			http.Error(w, "generate token: "+err.Error(), http.StatusInternalServerError)
			return
		}
		tokenPath := cfg.effectiveAPITokenPath()
		if err := os.WriteFile(tokenPath, []byte(newToken), 0o600); err != nil {
			http.Error(w, "write token file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cfg.StatusSrv.SetToken(newToken)
		writeJSON(w, map[string]string{"token": newToken})
	})
}

// handleSecretsList returns the sorted list of key names for a service's
// secrets file. Never returns plaintext values. Returns an empty list when
// no secrets file exists for the service yet.
func handleSecretsList(w http.ResponseWriter, _ *http.Request, cfg APIConfig, service string) {
	secretsFile := conventionalSecretsFile(cfg, service)
	if _, err := os.Stat(secretsFile); os.IsNotExist(err) {
		writeJSON(w, map[string]any{"service": service, "keys": []string{}})
		return
	}
	set, err := secrets.Issue(secrets.FileKeyCustody{Path: cfg.effectiveAgeKeyPath()}, secretsFile)
	if err != nil {
		http.Error(w, "decrypt secrets: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"service": service,
		"keys":    set.Names(),
	})
}

// handleSecretsGet decrypts and returns the value for a single key.
func handleSecretsGet(w http.ResponseWriter, _ *http.Request, cfg APIConfig, service, key string) {
	secretsFile := conventionalSecretsFile(cfg, service)
	set, err := secrets.Issue(secrets.FileKeyCustody{Path: cfg.effectiveAgeKeyPath()}, secretsFile)
	if err != nil {
		http.Error(w, "decrypt secrets: "+err.Error(), http.StatusInternalServerError)
		return
	}
	val, ok := set.Get(key)
	if !ok {
		http.Error(w, fmt.Sprintf("key %q not found in secrets for service %q", key, service), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{
		"key":   key,
		"value": string(val),
	})
}

// handleSecretsSet merges new key-value pairs into the service's secrets file
// and re-encrypts it. The file is stored at the conventional path; no git
// involvement.
func handleSecretsSet(w http.ResponseWriter, r *http.Request, cfg APIConfig, service string) {
	var updates map[string]string
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(updates) == 0 {
		http.Error(w, "request body must contain at least one key-value pair", http.StatusBadRequest)
		return
	}

	secretsFile := conventionalSecretsFile(cfg, service)
	custody := secrets.FileKeyCustody{Path: cfg.effectiveAgeKeyPath()}
	merged, err := mergeSecrets(custody, secretsFile, updates)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id, err := custody.LoadIdentity()
	if err != nil {
		http.Error(w, "load age key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ciphertext, err := secrets.EncryptSecrets(id.Recipient(), merged)
	if err != nil {
		http.Error(w, "encrypt secrets: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.MkdirAll(filepath.Dir(secretsFile), 0o700); err != nil {
		http.Error(w, "create secrets dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(secretsFile, ciphertext, 0o600); err != nil {
		http.Error(w, "write secrets file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"service": service, "updated": len(updates)})
}

// handleSecretsDelete removes one key from a service's secrets file and
// re-encrypts it. No git involvement.
func handleSecretsDelete(w http.ResponseWriter, _ *http.Request, cfg APIConfig, service, key string) {
	secretsFile := conventionalSecretsFile(cfg, service)
	custody := secrets.FileKeyCustody{Path: cfg.effectiveAgeKeyPath()}
	set, err := secrets.Issue(custody, secretsFile)
	if err != nil {
		http.Error(w, "decrypt secrets: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, ok := set.Get(key); !ok {
		http.Error(w, fmt.Sprintf("key %q not found in secrets for service %q", key, service), http.StatusNotFound)
		return
	}

	remaining := make(map[string]string, set.Len()-1)
	for _, k := range set.Names() {
		if k == key {
			continue
		}
		v, _ := set.Get(k)
		remaining[k] = string(v)
	}

	id, err := custody.LoadIdentity()
	if err != nil {
		http.Error(w, "load age key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ciphertext, err := secrets.EncryptSecrets(id.Recipient(), remaining)
	if err != nil {
		http.Error(w, "encrypt secrets: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(secretsFile, ciphertext, 0o600); err != nil {
		http.Error(w, "write secrets file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"service": service, "deleted": key})
}

// handleSecretsListAll returns the sorted list of service names that have at
// least one secrets file in SecretsDir. Never returns key names or values.
func handleSecretsListAll(w http.ResponseWriter, _ *http.Request, cfg APIConfig) {
	dir := cfg.effectiveSecretsDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		writeJSON(w, map[string]any{"services": []string{}})
		return
	}
	if err != nil {
		http.Error(w, "list secrets dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var services []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yaml.age") {
			services = append(services, strings.TrimSuffix(name, ".yaml.age"))
		}
	}
	writeJSON(w, map[string]any{"services": services})
}

// conventionalSecretsFile returns the conventional path for a service's
// age-encrypted secrets file: <SecretsDir>/<service>.yaml.age. The file
// may or may not exist; callers must check.
func conventionalSecretsFile(cfg APIConfig, service string) string {
	return filepath.Join(cfg.effectiveSecretsDir(), service+".yaml.age")
}

// mergeSecrets decrypts the existing secrets file (if it exists) and merges
// updates into the existing key-value pairs. Returns the merged map.
func mergeSecrets(custody secrets.FileKeyCustody, secretsFile string, updates map[string]string) (map[string]string, error) {
	merged := make(map[string]string)

	// If the file exists, decrypt the current contents first.
	if _, err := os.Stat(secretsFile); err == nil {
		set, err := secrets.Issue(custody, secretsFile)
		if err != nil {
			return nil, fmt.Errorf("decrypt existing secrets: %w", err)
		}
		for _, k := range set.Names() {
			v, _ := set.Get(k)
			merged[k] = string(v)
		}
	}

	for k, v := range updates {
		merged[k] = v
	}
	return merged, nil
}

// authRequired checks the Bearer token from the StatusServer. Returns false
// and writes 401 when the token is invalid or missing.
func authRequired(w http.ResponseWriter, r *http.Request, srv *StatusServer) bool {
	tok := srv.currentToken()
	if tok == "" {
		return true // no auth configured
	}
	if !bearerTokenValid(r.Header.Get("Authorization"), tok) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(w, `{"error":"encode failed"}`)
	}
}

// flushWriter wraps an http.ResponseWriter and flushes after every Write so
// streaming endpoints deliver lines to the client in real time.
type flushWriter struct {
	w     io.Writer
	flush func()
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.flush()
	return n, err
}

// generateToken returns a cryptographically random alphanumeric token of n
// characters.
func generateToken(n int) (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		b[i] = chars[idx.Int64()]
	}
	return string(b), nil
}
