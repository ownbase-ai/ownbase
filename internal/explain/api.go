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
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/install"
	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secrets"
	"github.com/ownbase/ownbase/internal/secwatch"
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
	// NotifyReboot, when non-nil, is called after /security/fix with the
	// current reboot-required marker so the daemon's cached status reflects
	// the marker immediately (not on the next 5-minute secwatch tick).
	NotifyReboot func(secwatch.RebootResult)
	// SelfUpdate, when non-nil, downloads and installs a newer ownbased
	// binary. Called by POST /self-update. The handler exits the process
	// after a successful swap so systemd restarts into the new binary.
	SelfUpdate func(w io.Writer, version string) (restart bool, err error)
	// DaemonVersion is the running binary's release tag, reported on
	// GET /version and folded into /status.
	DaemonVersion string
	// AuditLog, when non-nil, receives one record per host-mutating security
	// action (patch, reboot, scanner install, self-update). Nil is safe.
	AuditLog authz.AuditLogger
	// UpgradeCore, when non-nil, pulls the latest pinned image for the core
	// package (Caddy) and restarts it. Progress lines are written
	// to w so the HTTP handler can stream them to the client. A non-nil error
	// means at least one pull failed; partial progress may have been written.
	UpgradeCore func(w io.Writer) error
	// SecurityStatePath is where durable security-loop stamps are written
	// (last_patch_at, last_core_rebuild_at, rescan_on_boot). Empty means
	// DefaultSecurityStatePath. Tests override with a temp path.
	SecurityStatePath string
	// GetConfig, when non-nil, returns the current contents of ownbase.yaml
	// from the checkout. Called by GET /config — the read side of
	// `ownbasectl config get`.
	GetConfig func() (string, error)
	// WriteConfig, when non-nil, validates content and pushes it as a
	// proposal branch on the external config repo (never the tracked ref).
	// Called by POST /config. Returns branch, sha, repo_url, message.
	WriteConfig func(content, message, branch string) (ConfigWriteResult, error)
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
	// VerifyBackup, when non-nil, runs the verified-restore drill immediately,
	// streaming progress lines to w, and returns the per-check outcomes.
	// Called by POST /backup/verify. A non-nil error means the drill could not
	// run at all, which is distinct from a drill that ran and failed a check
	// (VerifyDrillResult.Passed == false).
	VerifyBackup func(w io.Writer) (VerifyDrillResult, error)
	// PruneBackup, when non-nil, runs restic forget+prune with credentials
	// merged from the request over the Base's stored backup secret. Called by
	// POST /backup/prune (owner-only). Transient delete-capable keys in the
	// request body are never persisted on the Base.
	PruneBackup func(req BackupPruneRequest) (BackupPruneStatus, error)
	// RekeyBackup, when non-nil, runs one phase of restic password rotation.
	// Called by POST /backup/rekey (owner-only).
	RekeyBackup func(req BackupRekeyRequest) (BackupRekeyStatus, error)
	// CoreStatus, when non-nil, reports the current state of the OwnBase core
	// package (Caddy): pinned image + digest and whether the
	// container is running on the Base. Called by GET /core/status — the
	// endpoint behind `ownbasectl upgrade` (check-only mode).
	CoreStatus func() []CorePackageStatus
	// DBStatus, when non-nil, reports the Postgres point-in-time recovery
	// posture: backups held, WAL archive range, and archiver health. Called by
	// GET /db/status — the endpoint behind `ownbasectl db status`.
	DBStatus func() (any, error)
	// DBRestore, when non-nil, performs a point-in-time restore, streaming
	// progress to w and returning a JSON-serialisable outcome. Called by
	// POST /db/restore — the endpoint behind `ownbasectl db restore`.
	DBRestore func(w io.Writer, req DBRestoreRequest) (any, error)
}

// ConfigWriteResult is the JSON body of a successful POST /config.
type ConfigWriteResult struct {
	Status  string `json:"status"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha"`
	RepoURL string `json:"repo_url"`
	Message string `json:"message"`
}

// DBRestoreRequest is the body of POST /db/restore.
type DBRestoreRequest struct {
	// Target is the recovery timestamp. Empty means recover everything the
	// repository holds.
	Target string `json:"target,omitempty"`

	// Into is "scratch" (default) or "production".
	Into string `json:"into,omitempty"`

	// ScratchPort overrides the loopback port a scratch instance publishes on.
	ScratchPort int `json:"scratch_port,omitempty"`
}

// DBStatusError is the 500 body of GET /db/status. It carries whatever was
// readable with the reason the rest was not: the repository is read first and
// from the filesystem, so a Postgres that is down still has a recovery window
// worth reporting.
type DBStatusError struct {
	Error  string `json:"error"`
	Status any    `json:"status,omitempty"`
}

// CorePackageStatus is the JSON-friendly state of one core package as
// returned by GET /core/status.
type CorePackageStatus struct {
	Name      string `json:"name"`      // e.g. "Caddy"
	Container string `json:"container"` // e.g. "ownbase-core-caddy"
	Image     string `json:"image"`     // e.g. "docker.io/library/caddy:2-alpine"
	Digest    string `json:"digest,omitempty"`
	// RunningDigest is the image digest the container is actually executing,
	// when podman can report it. Compared to Digest to decide whether
	// `upgrade --apply` would change anything.
	RunningDigest string `json:"running_digest,omitempty"`
	Running       bool   `json:"running"`
	// Recipe is the short hash of the Dockerfile embedded in this daemon.
	Recipe string `json:"recipe,omitempty"`
	// ImageRecipe is the ownbase.core.recipe label on the local image, when set.
	ImageRecipe string `json:"image_recipe,omitempty"`
	// GoImage is the GO_IMAGE default from the embedded Dockerfile.
	GoImage string `json:"go_image,omitempty"`
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

// BackupPruneRequest is the body of POST /backup/prune. Every field is
// optional: non-empty values override the matching key from the Base's
// age-encrypted backup secret for this one invocation only.
type BackupPruneRequest struct {
	Password           string `json:"password,omitempty"`
	AWSAccessKeyID     string `json:"aws_access_key_id,omitempty"`
	AWSSecretAccessKey string `json:"aws_secret_access_key,omitempty"`
	B2AccountID        string `json:"b2_account_id,omitempty"`
	B2AccountKey       string `json:"b2_account_key,omitempty"`
}

// BackupPruneStatus is the JSON body of a successful POST /backup/prune.
type BackupPruneStatus struct {
	LastPrune string `json:"last_prune,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// BackupRekeyRequest is the body of POST /backup/rekey.
type BackupRekeyRequest struct {
	// Phase is "add" or "finalize" (see backup.RekeyPhase).
	Phase string `json:"phase"`
	// NewPassword is the replacement restic repository password.
	NewPassword string `json:"new_password"`
}

// BackupRekeyStatus is the JSON body of a successful POST /backup/rekey.
type BackupRekeyStatus struct {
	Phase       string `json:"phase"`
	AlreadyDone bool   `json:"already_done,omitempty"`
	KeysRemoved int    `json:"keys_removed,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// VerifyDrillResult is the JSON-friendly outcome of one verified-restore
// drill, emitted as the ---RESULT--- trailer of POST /backup/verify.
//
// The per-check breakdown is the point of it. A drill that collapses to a
// single boolean tells an operator that their backups are not provably
// restorable without telling them which part is not — and the answer ("restic
// is fine, Postgres would not start") decides what they do next.
type VerifyDrillResult struct {
	Passed     bool               `json:"passed"`
	SnapshotID string             `json:"snapshot_id,omitempty"`
	VerifiedAt string             `json:"verified_at,omitempty"`
	Checks     []VerifyDrillCheck `json:"checks,omitempty"`
}

// VerifyDrillCheck is one integrity check's outcome.
type VerifyDrillCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
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

	// /config — GET reads the checkout; POST pushes a validated proposal
	// branch on the external config repo (never the tracked ref). Merge on
	// the forge, then POST /reconcile to apply.
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authorize(w, r, cfg.StatusSrv)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
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
		case http.MethodPost:
			if cfg.WriteConfig == nil {
				http.Error(w, "config write not available", http.StatusNotImplemented)
				return
			}
			var body struct {
				Content string `json:"content"`
				Message string `json:"message"`
				Branch  string `json:"branch"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(body.Content) == "" {
				http.Error(w, "content is required", http.StatusBadRequest)
				return
			}
			// Schema-validate before any git work.
			if _, err := schema.ParseConfig(strings.NewReader(body.Content)); err != nil {
				http.Error(w, "invalid ownbase.yaml: "+err.Error(), http.StatusBadRequest)
				return
			}
			result, err := cfg.WriteConfig(body.Content, body.Message, body.Branch)
			if cfg.AuditLog != nil {
				target := body.Branch
				if result.Branch != "" {
					target = result.Branch
				}
				if a, aerr := auditAction(schema.ActionConfigWrite, target, principal); aerr == nil {
					outcome := authz.OutcomeApplied
					errMsg := ""
					if err != nil {
						outcome = authz.OutcomeError
						errMsg = err.Error()
					}
					_ = cfg.AuditLog.Record(a, outcome, errMsg)
				}
			}
			if err != nil {
				// No-change and validation-shaped errors stay 400; git/network 500.
				msg := err.Error()
				if strings.HasPrefix(msg, "no change:") || strings.HasPrefix(msg, "invalid ") ||
					strings.HasPrefix(msg, "branch ") || strings.HasPrefix(msg, "refusing ") {
					http.Error(w, "write config: "+msg, http.StatusBadRequest)
					return
				}
				http.Error(w, "write config: "+msg, http.StatusInternalServerError)
				return
			}
			if result.Status == "" {
				result.Status = "pushed"
			}
			writeJSON(w, result)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
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
	// Owner-only (socket gate refuses services). Audited: repointing the Base
	// is the most consequential state change on the machine.
	mux.HandleFunc("/config/source", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authorize(w, r, cfg.StatusSrv)
		if !ok {
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
		err := cfg.SetConfigSource(body.RepoURL, body.Ref)
		if cfg.AuditLog != nil {
			if a, aerr := auditAction(schema.ActionConfigSource, body.RepoURL, principal); aerr == nil {
				outcome := authz.OutcomeApplied
				errMsg := ""
				if err != nil {
					outcome = authz.OutcomeError
					errMsg = err.Error()
				}
				_ = cfg.AuditLog.Record(a, outcome, errMsg)
			}
		}
		if err != nil {
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
		recordPatch := func(outcome, errMsg string) {
			if cfg.AuditLog == nil {
				return
			}
			action, err := auditAction(schema.ActionHostPatch, "host OS packages", schema.Owner())
			if err != nil {
				return
			}
			_ = cfg.AuditLog.Record(action, outcome, errMsg)
		}

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
			recordPatch(authz.OutcomeError, "apt-get update failed")
			return
		}
		// --with-new-pkgs is required for kernel upgrades: linux-image-generic
		// is a metapackage whose newer version depends on a *new* package
		// (linux-image-N.N.N-X-generic). Plain `apt-get upgrade` refuses to
		// install new packages and silently keeps the kernel back — leaving
		// every kernel CVE still "fixable" after Apply Patches. full-upgrade
		// is avoided: it may remove packages; --with-new-pkgs only adds.
		if !runStep("Upgrading packages (apt-get upgrade --with-new-pkgs)", "upgrade", "-y", "-q", "--with-new-pkgs") {
			recordPatch(authz.OutcomeError, "apt-get upgrade failed")
			return
		}
		// Old kernel ABIs stay installed after a metapackage bump (they are
		// not auto-removable). trivy then keeps counting their CVEs as
		// fixable. Purge every versioned kernel package that is not the
		// newest installed ABI; a reboot is still required to run it.
		if obsolete, err := obsoleteKernelPackages(ctx); err != nil {
			fmt.Fprintf(fw, "WARNING: list obsolete kernels: %v\n", err)
		} else if len(obsolete) > 0 {
			args := append([]string{"purge", "-y", "-q"}, obsolete...)
			if !runStep("Removing obsolete kernel packages", args...) {
				fmt.Fprintf(fw, "WARNING: could not purge obsolete kernels — they may still appear in CVE scans\n")
			}
		}
		if !runStep("Removing unused packages (apt-get autoremove)", "autoremove", "-y", "-q") {
			// Non-fatal: the upgrades already applied.
			fmt.Fprintf(fw, "WARNING: apt-get autoremove failed\n")
		}
		recordPatch(authz.OutcomeApplied, "")

		// Stamp last_patch_at so checkup suppresses "Apply patches" until a
		// post-patch scan lands (scanned_at would otherwise keep driving the
		// pre-patch count).
		patchedAt := time.Now().UTC()
		if err := MarkPatched(cfg.securityStatePath()); err != nil {
			fmt.Fprintf(fw, "WARNING: persist last_patch_at: %v\n", err)
		}
		if cfg.StatusSrv != nil {
			cfg.StatusSrv.SetLastPatchAt(patchedAt)
		}

		// A successful upgrade can leave the machine needing a reboot (new
		// kernel). Surface that here and push it into the cached status so
		// the next /status read (and therefore the app's refresh) sees it
		// without waiting for the 5-minute secwatch tick.
		reboot := secwatch.GatherRebootRequired()
		if cfg.NotifyReboot != nil {
			cfg.NotifyReboot(reboot)
		}
		if reboot.Required {
			fmt.Fprintf(fw, "\n==> A reboot is required for some upgrades to take effect")
			if len(reboot.Packages) > 0 {
				fmt.Fprintf(fw, " (%s)", strings.Join(reboot.Packages, ", "))
			}
			fmt.Fprintf(fw, ".\n    Run 'ownbasectl security reboot' (or 'security fix --reboot') when ready — every service will drop for ~30–60s.\n")
			fmt.Fprintf(fw, "    Until then CVE counts still describe the pre-patch packages.\n")
		}

		// When a reboot is required, do not start a multi-minute trivy run
		// against packages that are about to be left behind — the boot-time
		// rescan (rescan_on_boot) is the honest measurement.
		if !reboot.Required {
			fmt.Fprintf(fw, "\n==> Done. Triggering vulnerability rescan...\n")
			if cfg.TriggerScan != nil {
				if cfg.TriggerScan() {
					fmt.Fprintf(fw, "    Scan started — results available in a few minutes.\n")
				} else {
					fmt.Fprintf(fw, "    Vulnerability scan will refresh on its normal schedule.\n")
				}
			}
		} else {
			fmt.Fprintf(fw, "\n==> Done. Reboot to finish; a CVE rescan will run automatically on boot.\n")
		}
		fmt.Fprintf(fw, "---OK---\n")
	})

	// /security/reboot — schedule a host reboot one minute out and stream a
	// short confirmation. The delay lets ---OK--- reach the client before the
	// SSH tunnel dies with the machine. Requires POST.
	mux.HandleFunc("/security/reboot", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := exec.LookPath("shutdown"); err != nil {
			http.Error(w, "shutdown not found — this endpoint is only available on Linux", http.StatusNotImplemented)
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

		recordReboot := func(outcome, errMsg string) {
			if cfg.AuditLog == nil {
				return
			}
			action, err := auditAction(schema.ActionHostReboot, "host", schema.Owner())
			if err != nil {
				return
			}
			_ = cfg.AuditLog.Record(action, outcome, errMsg)
		}

		// Ask the next daemon start to scan immediately instead of waiting
		// the normal 5-minute startup delay — otherwise the Base is blind to
		// CVEs for minutes after a security reboot.
		if err := MarkRescanOnBoot(cfg.securityStatePath()); err != nil {
			fmt.Fprintf(fw, "WARNING: could not set rescan-on-boot: %v\n", err)
		}

		fmt.Fprintf(fw, "==> Scheduling reboot in 1 minute\n")
		fmt.Fprintf(fw, "    Every service on this Base will stop and restart with the machine.\n")
		fmt.Fprintf(fw, "    A CVE rescan will run automatically when the Base comes back.\n")
		// `shutdown -r +1` returns immediately; the actual reboot is a minute
		// later. That window is what lets ---OK--- leave the box before the
		// network drops. `+0` would race the response.
		cmd := exec.Command("shutdown", "-r", "+1", "OwnBase security reboot")
		cmd.Stdout = fw
		cmd.Stderr = fw
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(fw, "ERROR: shutdown: %v\n", err)
			recordReboot(authz.OutcomeError, err.Error())
			return
		}
		recordReboot(authz.OutcomeApplied, "")
		fmt.Fprintf(fw, "==> Reboot scheduled at %s UTC\n", time.Now().UTC().Add(time.Minute).Format(time.RFC3339))
		fmt.Fprintf(fw, "---OK---\n")
		flush()
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

		// Stamp last_core_rebuild_at so checkup suppresses "Rebuild Caddy"
		// while the post-rebuild scan is still in flight, and can detect a
		// proven-ineffective rebuild (newer scan still shows fixable CVEs).
		rebuiltAt := time.Now().UTC()
		if err := MarkCoreRebuilt(cfg.securityStatePath()); err != nil {
			fmt.Fprintf(fw, "WARNING: persist last_core_rebuild_at: %v\n", err)
		}
		if cfg.StatusSrv != nil {
			cfg.StatusSrv.SetLastCoreRebuildAt(rebuiltAt)
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

	// /backup/prune — owner-driven forget+prune. Used when
	// core.backup.append_only keeps scheduled snapshots from pruning (the
	// Base holds non-deleting cloud keys). Optional body credentials override
	// the stored backup secret for this call only and are never written down.
	mux.HandleFunc("/backup/prune", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.PruneBackup == nil {
			http.Error(w, "backup not configured", http.StatusNotImplemented)
			return
		}
		var req BackupPruneRequest
		if r.Body != nil {
			defer r.Body.Close()
			dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil && err != io.EOF {
				http.Error(w, "invalid prune body: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		status, err := cfg.PruneBackup(req)
		if err != nil {
			http.Error(w, "prune backup: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, status)
	})

	// /backup/rekey — rotate the restic repository password in two crash-safe
	// phases (add, then finalize). Owner-only. Behind `ownbasectl backup rekey`.
	mux.HandleFunc("/backup/rekey", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.RekeyBackup == nil {
			http.Error(w, "backup not configured", http.StatusNotImplemented)
			return
		}
		var req BackupRekeyRequest
		if r.Body == nil {
			http.Error(w, "rekey body required", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid rekey body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Phase == "" || req.NewPassword == "" {
			http.Error(w, "phase and new_password are required", http.StatusBadRequest)
			return
		}
		status, err := cfg.RekeyBackup(req)
		if err != nil {
			http.Error(w, "rekey backup: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, status)
	})

	// /backup/verify — run the verified-restore drill on demand and stream
	// progress, rather than making the operator wait for the daemon's
	// verify_interval to come round. Used by `ownbasectl checkup --verify`.
	//
	// Streamed plain text, like /upgrade and /security/fix: the drill restores
	// a real snapshot and (when Postgres is in it) starts a real database, so
	// it takes minutes and an operator watching it needs to see where it is.
	// The response ends with a ---RESULT--- line carrying the per-check
	// outcomes as JSON, then ---OK--- only when every check passed.
	mux.HandleFunc("/backup/verify", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.VerifyBackup == nil {
			http.Error(w, "backup not configured", http.StatusNotImplemented)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		flusher, canFlush := w.(http.Flusher)
		fw := &flushWriter{w: w, flush: func() {
			if canFlush {
				flusher.Flush()
			}
		}}

		result, err := cfg.VerifyBackup(fw)
		if err != nil {
			fmt.Fprintf(fw, "\nERROR: %v\n", err)
			return
		}

		// The trailer is emitted whether or not the drill passed — a failed
		// drill's per-check detail is exactly what the caller needs.
		if encoded, err := json.Marshal(result); err == nil {
			fmt.Fprintf(fw, "---RESULT---%s\n", encoded)
		}
		if !result.Passed {
			return
		}
		fmt.Fprintf(fw, "---OK---\n")
	})

	// /db/status — Postgres point-in-time recovery posture: what backups are
	// held, how far the WAL archive reaches, and whether archiving still works.
	// Behind `ownbasectl db status`.
	mux.HandleFunc("/db/status", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.DBStatus == nil {
			http.Error(w, "no managed Postgres on this Base", http.StatusNotImplemented)
			return
		}
		status, err := cfg.DBStatus()
		if err != nil {
			// The repository half is read before Postgres is, and the usual
			// reason this fails is a database that is down — which is exactly
			// when what can be restored is the thing worth knowing. Return that
			// half alongside the error rather than only the error text.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(DBStatusError{
				Error:  "db status: " + err.Error(),
				Status: status,
			})
			return
		}
		writeJSON(w, status)
	})

	// /db/restore — point-in-time recovery, either into a scratch instance
	// beside production or over production itself. Behind
	// `ownbasectl db restore`.
	//
	// Streamed plain text with the same two trailers as /backup/verify: a
	// restore takes minutes, and every one of its failure modes happens
	// mid-flight, so an operator needs to see where it got to.
	mux.HandleFunc("/db/restore", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.DBRestore == nil {
			http.Error(w, "no managed Postgres on this Base", http.StatusNotImplemented)
			return
		}

		var req DBRestoreRequest
		if r.Body != nil {
			// An empty body is valid: it means recover everything the
			// repository holds, into a scratch instance.
			_ = json.NewDecoder(r.Body).Decode(&req)
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		flusher, canFlush := w.(http.Flusher)
		fw := &flushWriter{w: w, flush: func() {
			if canFlush {
				flusher.Flush()
			}
		}}

		outcome, err := cfg.DBRestore(fw, req)
		if err != nil {
			fmt.Fprintf(fw, "\nERROR: %v\n", err)
			return
		}
		if encoded, err := json.Marshal(outcome); err == nil {
			fmt.Fprintf(fw, "---RESULT---%s\n", encoded)
		}
		fmt.Fprintf(fw, "---OK---\n")
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

	// /security/scanner/install — install trivy + enable podman.socket, streaming
	// progress. Same path PassZero uses at bootstrap. Requires POST.
	mux.HandleFunc("/security/scanner/install", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		fmt.Fprintf(fw, "==> Installing trivy vulnerability scanner\n")
		st := install.EnsureTrivy(r.Context(), install.PassZeroConfig{})
		if st.Err != nil {
			fmt.Fprintf(fw, "ERROR: %v\n", st.Err)
			if cfg.AuditLog != nil {
				if a, err := auditAction(schema.ActionHostInstallScanner, "trivy", schema.Owner()); err == nil {
					_ = cfg.AuditLog.Record(a, authz.OutcomeError, st.Err.Error())
				}
			}
			return
		}
		if st.AlreadyOK {
			fmt.Fprintf(fw, "==> Already installed: %s\n", st.Detail)
		} else {
			fmt.Fprintf(fw, "==> Installed: %s\n", st.Detail)
		}
		if cfg.AuditLog != nil {
			if a, err := auditAction(schema.ActionHostInstallScanner, "trivy", schema.Owner()); err == nil {
				_ = cfg.AuditLog.Record(a, authz.OutcomeApplied, "")
			}
		}
		if cfg.TriggerScan != nil {
			fmt.Fprintf(fw, "==> Triggering first vulnerability scan...\n")
			_ = cfg.TriggerScan()
		}
		fmt.Fprintf(fw, "---OK---\n")
	})

	// /version — the running daemon's release tag. Public-ish but still auth'd
	// so a random port scanner cannot fingerprint OwnBase versions.
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		v := cfg.DaemonVersion
		if v == "" {
			v = "dev"
		}
		writeJSON(w, map[string]string{"version": v})
	})

	// /self-update — download a newer ownbased, verify, install, then exit so
	// systemd Restart=always boots the new binary. Streams progress.
	mux.HandleFunc("/self-update", func(w http.ResponseWriter, r *http.Request) {
		if !authRequired(w, r, cfg.StatusSrv) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.SelfUpdate == nil {
			http.Error(w, "self-update not configured", http.StatusNotImplemented)
			return
		}
		var body struct {
			Version string `json:"version"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		if body.Version == "" {
			body.Version = "latest"
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

		restart, err := cfg.SelfUpdate(fw, body.Version)
		if err != nil {
			fmt.Fprintf(fw, "ERROR: %v\n", err)
			if cfg.AuditLog != nil {
				if a, e := auditAction(schema.ActionHostSelfUpdate, body.Version, schema.Owner()); e == nil {
					_ = cfg.AuditLog.Record(a, authz.OutcomeError, err.Error())
				}
			}
			return
		}
		if cfg.AuditLog != nil {
			if a, e := auditAction(schema.ActionHostSelfUpdate, body.Version, schema.Owner()); e == nil {
				_ = cfg.AuditLog.Record(a, authz.OutcomeApplied, "")
			}
		}
		// Distinct trailers so the CLI can tell a no-op from a real restart
		// (both end with ---OK--- for back-compat with stream parsers).
		if restart {
			fmt.Fprintf(fw, "==> Restart pending — process will exit for systemd\n")
		} else {
			fmt.Fprintf(fw, "==> Already current — no restart\n")
		}
		fmt.Fprintf(fw, "---OK---\n")
		flush()
		if restart {
			// Give the response a moment to leave the box, then exit so
			// systemd Restart=always boots the new binary.
			go func() {
				time.Sleep(500 * time.Millisecond)
				os.Exit(0)
			}()
		}
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
// and writes 401 when the token is empty, invalid, or missing. An empty
// configured token is fail-closed — the management API never runs open.
// Requests that already carry a service principal (unix socket) skip Bearer.
func authRequired(w http.ResponseWriter, r *http.Request, srv *StatusServer) bool {
	_, ok := authorize(w, r, srv)
	return ok
}

// authorize authenticates the request and returns the acting principal.
// A service principal injected on the request context (unix socket accept)
// is trusted without Bearer. Otherwise Bearer auth yields the owner.
func authorize(w http.ResponseWriter, r *http.Request, srv *StatusServer) (schema.Principal, bool) {
	if p, ok := PrincipalFromContext(r.Context()); ok {
		return p, true
	}
	if !authorizeBearer(w, r, srv.currentToken()) {
		return schema.Principal{}, false
	}
	return schema.Owner(), true
}

// auditAction builds a taxonomy action tagged with the given principal.
func auditAction(t schema.ActionType, target string, p schema.Principal) (schema.Action, error) {
	a, err := schema.NewAction(t, target)
	if err != nil {
		return schema.Action{}, err
	}
	if p.Kind == "" {
		p = schema.Owner()
	}
	return a.WithPrincipal(p), nil
}

// securityStatePath returns the durable security.json path for this config.
func (cfg APIConfig) securityStatePath() string {
	if cfg.SecurityStatePath != "" {
		return cfg.SecurityStatePath
	}
	return DefaultSecurityStatePath
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
