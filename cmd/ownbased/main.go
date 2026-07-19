// Command ownbased is the on-Base daemon: reconcile, watch, explain,
// recover. It implements the "thermostat loop" from the Reconstruction Model:
//
//	desired = compile(checkout, secrets)
//	current = query(podman, systemd)
//	reconcile(desired, current)
//
// Two triggers fire this loop:
//  1. A SIGUSR1 signal from the post-receive hook after a git push.
//  2. A periodic timer backstop that catches drift between commits.
//
// The hook sends SIGUSR1 to the PID written to /opt/ownbase/daemon.pid.
// Both paths call the identical reconcileLoop so there is never a divergence
// between event-driven and timer-driven convergence.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/backup"
	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/core"
	"github.com/ownbase/ownbase/internal/explain"
	"github.com/ownbase/ownbase/internal/githost"
	"github.com/ownbase/ownbase/internal/install"
	"github.com/ownbase/ownbase/internal/podman"
	"github.com/ownbase/ownbase/internal/reconcile"
	"github.com/ownbase/ownbase/internal/repos"
	"github.com/ownbase/ownbase/internal/runtime"
	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secrets"
	"github.com/ownbase/ownbase/internal/secwatch"
	"github.com/ownbase/ownbase/internal/update"
	"github.com/ownbase/ownbase/internal/vulnscan"
)

const (
	// DefaultCheckoutPath is where the agent reads ownbase.yaml.
	DefaultCheckoutPath = githost.DefaultCheckoutPath

	// DefaultRepoPath is the bare repo the agent bootstraps and the hook targets.
	DefaultRepoPath = githost.DefaultRepoPath

	// DefaultPIDPath is where the agent writes its PID for hook signaling.
	DefaultPIDPath = githost.DefaultPIDPath

	// DefaultTickInterval is the drift-backstop reconcile interval.
	DefaultTickInterval = 5 * time.Minute

	// DefaultUpdateInterval is how often the agent polls for service version updates.
	DefaultUpdateInterval = update.DefaultCheckInterval

	// DefaultVulnScanInterval is how often the vulnerability scanner runs.
	// Daily is appropriate: the trivy DB updates are also daily.
	DefaultVulnScanInterval = 24 * time.Hour

	// DefaultStatusAddr is the address the status API server listens on.
	// Bind to loopback only — the status API contains sensitive data
	// (audit records, security posture, service topology) and must not be
	// reachable from the network without auth. Relying solely on UFW is
	// insufficient (docs/decisions.md); the same rule applies here. The
	// post-receive hook reaches the agent via SIGUSR1 directly (not via the
	// status port), so loopback is correct.
	DefaultStatusAddr = "127.0.0.1:7070"
)

func main() {
	fs := flag.NewFlagSet("ownbased", flag.ExitOnError)
	checkoutPath := fs.String("checkout", DefaultCheckoutPath,
		"path to the ownbase checkout (contains ownbase.yaml)")
	repoPath := fs.String("repo", DefaultRepoPath,
		"path to the bare git repo")
	auditLogPath := fs.String("audit-log", authz.DefaultAuditLogPath,
		"path to the audit log file")
	pidPath := fs.String("pid", DefaultPIDPath,
		"path to write the agent PID for hook signaling")
	tickInterval := fs.Duration("tick", DefaultTickInterval,
		"drift-backstop reconcile interval")
	dryRun := fs.Bool("dry-run", false,
		"preview what the agent would do without making changes")
	once := fs.Bool("once", false,
		"run the reconcile loop exactly once and exit")
	skipBootstrap := fs.Bool("skip-bootstrap", false,
		"skip bare-repo bootstrap (use when repo already initialized)")
	skipPassZero := fs.Bool("skip-pass-zero", false,
		"skip host hardening pass zero (for dev/macOS where Linux steps don't apply)")
	sshPort := fs.Int("ssh-port", 22,
		"SSH port to allow through the firewall (pass zero)")
	// --backup-repo is only used during --rebuild on a bare machine that has no
	// ownbase.yaml yet. Steady-state backup config comes from core.backup: in
	// ownbase.yaml; credentials come from /opt/ownbase/secrets/backup.yaml.age.
	backupRepo := fs.String("backup-repo", "",
		"restic repository URL — only used with --rebuild on a bare machine")
	// Rebuild flag (M6). --force-rebuild added in M12.
	rebuild := fs.Bool("rebuild", false,
		"restore latest backup then exit (agent runs reconcile on the next start)")
	forceRebuild := fs.Bool("force-rebuild", false,
		"restore from an unverified backup snapshot (skips the Restorable guard — use deliberately)")
	updateInterval := fs.Duration("update-interval", DefaultUpdateInterval,
		"how often to poll for service version updates (source ref and bundled-image digest)")
	vulnScanInterval := fs.Duration("vuln-scan-interval", DefaultVulnScanInterval,
		"how often to run the trivy vulnerability scan (host OS packages + service images)")
	// Status API flags. Bind to loopback by default — code and
	// docs/decisions.md agree the status API must never be reachable from
	// the network without going through the SSH tunnel.
	statusAddr := fs.String("status-addr", DefaultStatusAddr,
		"address the status API server listens on (empty = disabled)")
	apiToken := fs.String("api-token", "",
		"Bearer token required to access the agent API (empty = no authentication)")
	_ = fs.Parse(os.Args[1:])

	if err := run(agentConfig{
		checkoutPath:     *checkoutPath,
		repoPath:         *repoPath,
		auditLogPath:     *auditLogPath,
		pidPath:          *pidPath,
		tickInterval:     *tickInterval,
		dryRun:           *dryRun,
		once:             *once,
		skipBootstrap:    *skipBootstrap,
		skipPassZero:     *skipPassZero,
		sshPort:          *sshPort,
		backupRepo:       *backupRepo,
		rebuild:          *rebuild,
		forceRebuild:     *forceRebuild,
		updateInterval:   *updateInterval,
		vulnScanInterval: *vulnScanInterval,
		statusAddr:       *statusAddr,
		apiToken:         *apiToken,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: %v\n", err)
		os.Exit(1)
	}
}

type agentConfig struct {
	checkoutPath     string
	repoPath         string
	auditLogPath     string
	pidPath          string
	tickInterval     time.Duration
	dryRun           bool
	once             bool
	skipBootstrap    bool
	skipPassZero     bool
	sshPort          int
	backupRepo       string
	rebuild          bool
	forceRebuild     bool
	updateInterval   time.Duration
	vulnScanInterval time.Duration
	statusAddr       string
	apiToken         string
}

// reconcileState holds the state produced during a single reconcile cycle and
// passed to the explain gather step after the cycle completes.
type reconcileState struct {
	Config      *schema.OwnbaseConfig // nil when ownbase.yaml failed to parse
	DriftEvents []reconcile.DriftEvent
	Current     runtime.CurrentState
}

// MinVulnScanInterval is the lowest accepted value for --vuln-scan-interval.
// time.NewTicker panics on zero or negative durations; 1 minute is a sensible
// lower bound even in test environments.
const MinVulnScanInterval = time.Minute

func run(cfg agentConfig) error {
	if cfg.vulnScanInterval <= 0 {
		cfg.vulnScanInterval = DefaultVulnScanInterval
		fmt.Fprintf(os.Stderr, "ownbased: vuln-scan-interval <= 0; using default %s\n", DefaultVulnScanInterval)
	} else if cfg.vulnScanInterval < MinVulnScanInterval {
		cfg.vulnScanInterval = MinVulnScanInterval
		fmt.Fprintf(os.Stderr, "ownbased: vuln-scan-interval too small; clamped to %s\n", MinVulnScanInterval)
	}

	// Rebuild mode: restore latest backup then exit. The next agent start
	// runs pass zero + reconcile as normal, completing the rebuild path.
	if cfg.rebuild {
		fmt.Fprintln(os.Stderr, "ownbased: rebuild mode — restoring latest backup ...")
		// Credentials (RESTIC_PASSWORD, AWS_*) come from the operator's
		// environment — standard restic behaviour. No PasswordFile needed.
		return backup.Rebuild(context.Background(), backup.RebuildConfig{
			BackupConfig: backup.Config{
				Repository: cfg.backupRepo,
			},
			DryRun:       cfg.dryRun,
			ForceRebuild: cfg.forceRebuild,
		})
	}

	// Pass zero: harden the host and install the container runtime.
	// Runs on every start (each step is idempotent). Skip on macOS / dev.
	if !cfg.skipPassZero {
		fmt.Fprintln(os.Stderr, "ownbased: running pass zero (host hardening) ...")
		report, err := install.PassZero(context.Background(), install.PassZeroConfig{
			DryRun:         cfg.dryRun,
			SSHPort:        cfg.sshPort,
			ExposeWebPorts: hasPublicDomainOnDisk(cfg.checkoutPath),
		})
		if err != nil {
			return fmt.Errorf("pass zero: %w", err)
		}
		if report.OK() {
			fmt.Fprintln(os.Stderr, "ownbased: pass zero complete — host is hardened")
		} else {
			fmt.Fprintln(os.Stderr, "ownbased: pass zero: some steps incomplete (will retry on next start)")
		}
	}

	// Bootstrap: ensure the bare repo and checkout exist.
	if !cfg.skipBootstrap {
		// Must run before any git operation touches a repo that may already
		// be chowned to the admin user (from a prior start) — otherwise the
		// daemon's own git commands (seed, pull, fetch) fail with git's
		// "dubious ownership" refusal. See githost.TrustAllRepos.
		if err := githost.TrustAllRepos(); err != nil {
			fmt.Fprintf(os.Stderr, "ownbased: trust local repos (non-fatal): %v\n", err)
		}
		if err := githost.Bootstrap(cfg.repoPath, cfg.checkoutPath); err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
		if err := githost.InstallHook(cfg.repoPath); err != nil {
			return fmt.Errorf("install hook: %w", err)
		}
		// Grant the admin SSH user write access to the bare repo — the
		// daemon creates it as root, so without this a direct `git push`
		// over SSH (the documented way to edit ownbase.yaml) fails with a
		// permission error. Non-fatal: ownbasectl config/service (the
		// API-based commit path) works regardless.
		if adminUser := install.ReadAdminUser(install.AdminUserPath); adminUser != "" {
			if err := githost.SetRepoOwner(cfg.repoPath, adminUser); err != nil {
				fmt.Fprintf(os.Stderr, "ownbased: set repo owner (non-fatal): %v\n", err)
			}
			// Let the post-receive hook (running as adminUser) wake this
			// (root) process after a direct push — see githost.HookScript.
			if err := install.EnsureNotifySudoers(adminUser); err != nil {
				fmt.Fprintf(os.Stderr, "ownbased: ensure notify sudoers (non-fatal): %v\n", err)
			}
		}
		// Seed a template ownbase.yaml (and README) on a brand-new config
		// repo. A no-op once the user (or a prior boot) has committed a real
		// config — see internal/install/seed.go.
		firstRun := install.ReadFirstRunEnv(install.FirstRunEnvPath)
		if err := install.SeedConfigRepo(cfg.checkoutPath, firstRun.CaddyEmail); err != nil {
			fmt.Fprintf(os.Stderr, "ownbased: seed config repo (non-fatal): %v\n", err)
		}
		// Remove any stale OWNBASE.md left by a previous daemon version that
		// wrote a generated status file to the checkout. The file is no longer
		// produced; leaving it would mislead operators with outdated status.
		staleFile := filepath.Join(cfg.checkoutPath, "OWNBASE.md")
		if err := os.Remove(staleFile); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "ownbased: remove stale OWNBASE.md (non-fatal): %v\n", err)
		}
	}

	// Ensure the age secrets key exists before anything (secrets, backups)
	// might need it. Generated once on first boot; idempotent on every
	// subsequent start. The private key never leaves this file.
	if !cfg.skipBootstrap {
		if _, err := os.Stat(secrets.DefaultKeyPath); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(secrets.DefaultKeyPath), 0o700); err != nil {
				return fmt.Errorf("create age key dir: %w", err)
			}
			if _, err := secrets.GenerateAndSave(secrets.DefaultKeyPath); err != nil {
				return fmt.Errorf("generate age key: %w", err)
			}
			fmt.Fprintln(os.Stderr, "ownbased: generated age secrets key at "+secrets.DefaultKeyPath)
		} else if err != nil {
			return fmt.Errorf("stat age key: %w", err)
		}
	}

	// Bootstrap the core package (Caddy) as a Quadlet unit.
	// Reads the core: block from ownbase.yaml if present, otherwise uses defaults.
	// Idempotent: safe to call on every startup.
	{
		coreCfg := schema.CoreConfig{} // defaults apply
		hasPublicDomain := false
		if cfgOnDisk, err := schema.ParseConfigFile(
			filepath.Join(cfg.checkoutPath, "ownbase.yaml"),
		); err == nil {
			coreCfg = cfgOnDisk.Core
			hasPublicDomain = cfgOnDisk.HasPublicDomain()
		}
		if err := bootstrapCore(context.Background(), cfg, coreCfg, hasPublicDomain); err != nil {
			return fmt.Errorf("bootstrap core: %w", err)
		}
		// Delete the one-time first-run file exactly once, here, rather than
		// inside bootstrapCore itself (which now also runs on every
		// reconcile tick via syncCoreForConfig — see readFirstRunEnv for
		// why deleting it there would silently drop the ACME email on the
		// second call).
		install.DeleteFirstRunEnv(install.FirstRunEnvPath)
	}

	// Write PID file so the post-receive hook can signal us via SIGUSR1.
	if err := githost.WritePIDFile(cfg.pidPath); err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: write pid file: %v (hook signaling disabled)\n", err)
	}

	// Open the audit log (nop in dry-run).
	var auditLog authz.AuditLogger
	if cfg.dryRun {
		auditLog = authz.NopAuditLog()
	} else {
		al, err := authz.NewAuditLog(cfg.auditLogPath)
		if err != nil {
			return fmt.Errorf("open audit log: %w", err)
		}
		defer al.Close()
		auditLog = al
	}

	checkpoint := authz.NewTrivialCheckpoint()

	// Populate cfg.apiToken from the installer-written file when no flag
	// was provided. The file is written by install.sh with mode 0600.
	if cfg.apiToken == "" {
		if data, err := os.ReadFile(explain.DefaultAPITokenPath); err == nil {
			cfg.apiToken = strings.TrimSpace(string(data))
			fmt.Fprintln(os.Stderr, "ownbased: using API token from "+explain.DefaultAPITokenPath)
		}
	}

	// newApplier is provided by applier_default.go (noop) or
	// applier_integration.go (real Podman) depending on build tags.
	// For source-built services, the Applier clones the local bare repo
	// under /opt/ownbase/repos/ (see internal/repos) — no credentials needed.
	applier := newApplier(cfg)

	// reconcileSig carries "please reconcile now" wakeups from sources other
	// than SIGUSR1: the backup API (ConfigureBackup/RunBackup) and the backup
	// scheduler. Created before the HTTP server starts so the handler
	// closures capture it.
	reconcileSig := make(chan struct{}, 4)

	// Build the update-loop config here (moved up from below) so the
	// ConfigureBackup API closure and reconcileOnce can both capture it.
	updateCfg := update.Config{
		CheckoutPath: cfg.checkoutPath,
		DryRun:       cfg.dryRun,
	}

	// Status server (M8): serves the JSON status API consumed by the briefing.
	// The webhook handler shares the same mux so both routes are on one port.
	statusSrv := explain.NewStatusServer()

	// triggerScan is set below (after ctx and vulnResultCh are declared) so
	// the closure can reference them. The API handler captures the variable by
	// pointer — any call after the assignment will invoke the real function.
	var triggerScan func()

	if cfg.statusAddr != "" {
		mux := http.NewServeMux()
		// Mount status routes (exact paths so the webhook route below takes priority).
		statusHandler := statusSrv.Handler(cfg.apiToken)
		mux.Handle("/status", statusHandler)
		mux.Handle("/health", statusHandler)
		// Mount management API (secrets, credentials, token reset) — M15.
		explain.MountAPI(mux, explain.APIConfig{
			SecretsDir: explain.DefaultSecretsDir,
			StatusSrv:  statusSrv,
			// TriggerScan delegates to triggerScan, which is assigned below
			// once ctx and vulnResultCh are available. Returns false if the daemon
			// is still initialising and the scan goroutine is not yet wired.
			TriggerScan: func() bool {
				if triggerScan != nil {
					triggerScan()
					return true
				}
				return false
			},
			// UpgradeCore pulls the latest pinned image for Caddy (the sole
			// core package) and restarts it. Progress is written to w for
			// streaming to the client.
			UpgradeCore: func(w io.Writer) error {
				upgradeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()

				m := core.Current
				for _, pkg := range []struct {
					name      string
					container string
					image     string
					digest    string
				}{
					{"Caddy", core.CaddyContainerName, m.CaddyImage, m.CaddyDigest},
				} {
					imageRef := pkg.image
					if pkg.digest != "" {
						imageRef = pkg.image + "@" + pkg.digest
					}
					fmt.Fprintf(w, "==> Pulling %s (%s)...\n", pkg.name, imageRef)
					cmd := exec.CommandContext(upgradeCtx, "podman", "pull", imageRef)
					cmd.Stdout = w
					cmd.Stderr = w
					if err := cmd.Run(); err != nil {
						return fmt.Errorf("pull %s: %w", pkg.name, err)
					}
					fmt.Fprintf(w, "==> Restarting %s...\n", pkg.name)
					cmd = exec.CommandContext(upgradeCtx, "podman", "restart", pkg.container)
					cmd.Stdout = w
					cmd.Stderr = w
					if err := cmd.Run(); err != nil {
						fmt.Fprintf(w, "    warning: restart %s: %v (continuing)\n", pkg.name, err)
					}
				}
				return nil
			},
			// CoreStatus reports the pinned image/digest and running state of
			// the core package (Caddy) for `ownbasectl upgrade` (check-only).
			CoreStatus: func() []explain.CorePackageStatus {
				m := core.Current
				var out []explain.CorePackageStatus
				for _, pkg := range []struct {
					name      string
					container string
					image     string
					digest    string
				}{
					{"Caddy", core.CaddyContainerName, m.CaddyImage, m.CaddyDigest},
				} {
					running := false
					if state, err := exec.Command(
						"podman", "inspect", "--format", "{{.State.Running}}", pkg.container,
					).Output(); err == nil {
						running = strings.TrimSpace(string(state)) == "true"
					}
					out = append(out, explain.CorePackageStatus{
						Name:      pkg.name,
						Container: pkg.container,
						Image:     pkg.image,
						Digest:    pkg.digest,
						Running:   running,
					})
				}
				return out
			},
			// GetConfig reads the checkout's ownbase.yaml — the read side of
			// `ownbasectl config get`.
			GetConfig: func() (string, error) {
				data, err := os.ReadFile(filepath.Join(cfg.checkoutPath, "ownbase.yaml"))
				if err != nil {
					return "", err
				}
				return string(data), nil
			},
			// SetConfig validates newContent as a well-formed ownbase.yaml,
			// commits it through the local config-repo front door (see
			// update.CommitFile) exactly like a user's own git push, then
			// wakes the reconcile loop immediately so the change doesn't
			// wait for the timer backstop. Used by `ownbasectl config set`
			// and `ownbasectl service add/remove/update`.
			SetConfig: func(newContent, commitMsg string) error {
				if _, err := schema.ParseConfig(strings.NewReader(newContent)); err != nil {
					return fmt.Errorf("resulting ownbase.yaml would be invalid: %w", err)
				}
				commitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := update.CommitFile(commitCtx, updateCfg, "ownbase.yaml", newContent, commitMsg); err != nil {
					return fmt.Errorf("commit ownbase.yaml: %w", err)
				}
				signalReconcile(reconcileSig)
				return nil
			},
			// ConfigureBackup commits core.backup.repo (and optionally
			// interval/verify_interval) to ownbase.yaml through the local
			// config-repo front door (see update.CommitFile). The independent
			// backup scheduler (see backup_scheduler.go) picks up the change
			// on its next poll — no restart needed.
			ConfigureBackup: func(repo, interval, verifyInterval string) error {
				cfgPath := filepath.Join(cfg.checkoutPath, "ownbase.yaml")
				original, err := os.ReadFile(cfgPath)
				if err != nil {
					return fmt.Errorf("read ownbase.yaml: %w", err)
				}
				updated := backup.SetCoreBackupConfig(string(original), repo, interval, verifyInterval)
				if _, err := schema.ParseConfig(strings.NewReader(updated)); err != nil {
					return fmt.Errorf("resulting ownbase.yaml would be invalid: %w", err)
				}
				commitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				msg := fmt.Sprintf("chore(backup): configure backup repo %s", repo)
				if err := update.CommitFile(commitCtx, updateCfg, "ownbase.yaml", updated, msg); err != nil {
					return fmt.Errorf("commit ownbase.yaml: %w", err)
				}
				// Wake the reconcile loop immediately so the commit above
				// propagates into the checkout without waiting for the
				// 5-minute backstop ticker. `ownbasectl backup setup` runs an
				// immediate backup right after this call and needs the
				// config on disk.
				signalReconcile(reconcileSig)
				return nil
			},
			// RunBackup triggers one backup cycle immediately (ownbasectl
			// backup run) rather than waiting for the scheduler.
			RunBackup: func() (explain.BackupRunStatus, error) {
				runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				status, err := runBackupNow(runCtx, cfg, auditLog)
				// Refresh the cached /status payload either way — even a
				// failed run updates LastError, which ownbasectl surfaces.
				signalReconcile(reconcileSig)
				if err != nil {
					return explain.BackupRunStatus{}, err
				}
				out := explain.BackupRunStatus{
					LatestSnapshot: status.LatestSnapshot,
					Restorable:     status.Restorable,
					LastError:      status.LastError,
				}
				if !status.LastBackup.IsZero() {
					out.LastBackup = status.LastBackup.Format(time.RFC3339)
				}
				return out, nil
			},
		})
		httpSrv := &http.Server{
			Addr:    cfg.statusAddr,
			Handler: mux,
		}
		go func() {
			fmt.Fprintf(os.Stderr,
				"ownbased: status API listening on %s (auth: %v)\n",
				cfg.statusAddr, cfg.apiToken != "")
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "ownbased: status server: %v\n", err)
			}
		}()
	}

	mode := "dry-run"
	if !cfg.dryRun {
		mode = applierMode(applier)
	}
	fmt.Fprintf(os.Stderr,
		"ownbased: starting (mode=%s, tick=%s, checkout=%s, repo=%s)\n",
		mode, cfg.tickInterval, cfg.checkoutPath, cfg.repoPath)

	// secProbeInterval is the minimum time between expensive secwatch probes
	// (ss + ufw + fail2ban + journald). Reconcile can fire frequently on busy
	// repos; we don't want to shell out on every push.
	const secProbeInterval = 5 * time.Minute
	var lastSecProbe time.Time
	var lastExposure secwatch.ExposureResult
	var lastAccess secwatch.AccessResult
	var lastUnexpectedCount = -1 // -1 = first run; used for transition detection

	// lastVulnStatus holds the most recent trivy scan result. It is only ever
	// read and written on the main select loop (via vulnResultCh below), so
	// no mutex is needed.
	// Pre-seed TrivyInstalled so the initial status (before the first 5-minute
	// scan fires) distinguishes "trivy present, scan pending" from "trivy not
	// installed" — both are Available=false but only one should prompt the
	// operator to install trivy.
	lastVulnStatus := vulnscan.VulnStatus{TrivyInstalled: vulnscan.TrivyAvailable()}

	// vulnResultCh carries completed scan results from background goroutines
	// back to the main select loop. Buffer 1: a second scan that completes
	// while one result is already waiting is silently dropped (daily scans
	// are low-frequency and overlapping results are equivalent).
	vulnResultCh := make(chan vulnscan.VulnStatus, 1)

	// lastReconcileState is the state from the most recent reconcile cycle.
	// It is used by the vuln result handler to push a fresh status snapshot
	// to the status server immediately after a scan completes, rather than
	// waiting up to one tick interval for the next reconcile.
	var lastReconcileState reconcileState

	// afterReconcile gathers status from the completed cycle and updates the
	// status server.
	afterReconcile := func(state reconcileState) {
		ctx := context.Background()

		// Run security probes at most once per secProbeInterval.
		// Skip when config is nil (parse failure): the expected-port allowlist
		// would be incomplete, producing false port.exposed audit events until
		// the config is repaired and the next probe runs with valid state.
		if time.Since(lastSecProbe) >= secProbeInterval && state.Config != nil {
			lastExposure = secwatch.GatherExposure(ctx, state.Config, cfg.sshPort)
			lastAccess = secwatch.GatherAccess(ctx)
			lastSecProbe = time.Now()

			// Emit a port.exposed audit record on transition into/out of unexpected
			// exposure. On the first scan after startup (lastUnexpectedCount == -1),
			// emit immediately if unexpected exposure is already present — skipping
			// the first scan would silently miss exposure that predates the restart.
			if lastExposure.Available && lastExposure.UnexpectedCount != lastUnexpectedCount {
				if lastUnexpectedCount >= 0 || lastExposure.UnexpectedCount > 0 {
					action, _ := schema.NewAction(schema.ActionPortExposed, fmt.Sprintf("%d unexpected port(s)", lastExposure.UnexpectedCount))
					_ = auditLog.Record(action, authz.OutcomeApplied, "")
				}
				lastUnexpectedCount = lastExposure.UnexpectedCount
			}
		}

		backupStatus, _ := backup.LoadStatus(backup.DefaultStatusPath)
		status := explain.Gather(explain.GatherInput{
			Config:            state.Config,
			RunningContainers: state.Current.RunningContainers,
			BackupStatus:      backupStatus,
			DriftEvents:       state.DriftEvents,
			AuditLogPath:      cfg.auditLogPath,
			Exposure:          lastExposure,
			Access:            lastAccess,
			Vulns:             lastVulnStatus,
		})
		statusSrv.Update(status)
	}

	reconcileOnce := func(reason string) {
		fmt.Fprintf(os.Stderr, "ownbased: reconcile triggered (%s)\n", reason)
		state, err := reconcileLoop(cfg, checkpoint, applier, auditLog, cfg.dryRun)
		if err != nil {
			if isConfigError(err) {
				fmt.Fprintf(os.Stderr, "ownbased: config error (fix ownbase.yaml and push): %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "ownbased: reconcile error: %v\n", err)
			}
			// When the checkout is incomplete (ownbase.yaml not yet present),
			// bootstrap may not have fully seeded it. Retry once so the next
			// tick finds the file without waiting for the 5-minute backstop.
			if isCheckoutMissingError(err) {
				if cfgOnDisk, parseErr := schema.ParseConfigFile(
					filepath.Join(cfg.checkoutPath, "ownbase.yaml"),
				); parseErr == nil {
					syncCoreForConfig(context.Background(), cfg, cfgOnDisk, cfg.dryRun)
				}
			}
		}
		afterReconcile(state)
		lastReconcileState = state
	}

	// Run immediately on start.
	reconcileOnce("startup")

	if cfg.once {
		return nil
	}

	// SIGUSR1 is sent by the post-receive hook after every git push.
	// SIGTERM / SIGINT signal graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Now that ctx and vulnResultCh are available, wire up the scan trigger
	// used by /security/fix and /security/scan.
	triggerScan = func() {
		go func() {
			result := vulnscan.GatherVulns(ctx, vulnscan.RunningContainers(ctx))
			sendVulnResult(vulnResultCh, result)
		}()
	}

	hookSig := make(chan os.Signal, 4) // buffer so the hook never blocks
	signal.Notify(hookSig, syscall.SIGUSR1)

	ticker := time.NewTicker(cfg.tickInterval)
	defer ticker.Stop()

	// Backup + verified-restore-drill scheduling runs as an independent
	// goroutine (backup_scheduler.go) rather than a ticker wired into this
	// select loop. It re-reads core.backup: from ownbase.yaml on every poll,
	// so backups activate as soon as `ownbasectl backup setup` commits
	// a repo — no daemon restart required — and credential rotations via
	// `ownbasectl secrets set <base> backup` take effect on the next poll too.
	go runBackupScheduler(ctx, cfg, auditLog, reconcileSig)

	// Update ticker: blank-ref resolution and drift reporting, read straight
	// from each service's local bare repo — always active, no credentials
	// needed.
	updateTickerObj := time.NewTicker(cfg.updateInterval)
	defer updateTickerObj.Stop()
	updateTicker := updateTickerObj.C
	fmt.Fprintf(os.Stderr,
		"ownbased: update loop enabled (interval=%s, resolves blank refs + reports drift)\n",
		cfg.updateInterval)

	// Vulnerability scan ticker: runs trivy against the host OS packages and
	// each service's container image on a configurable interval (default 24h).
	// The ticker is always active; trivy degrades gracefully when not installed.
	vulnTicker := time.NewTicker(cfg.vulnScanInterval)
	defer vulnTicker.Stop()
	fmt.Fprintf(os.Stderr,
		"ownbased: vuln scan enabled (interval=%s, trivy=%v)\n",
		cfg.vulnScanInterval, vulnscan.TrivyAvailable())

	// Run an initial vulnerability scan shortly after startup so the first
	// status report includes CVE data. Delay to let the initial reconcile
	// complete and images be built before scanning them.
	// Result is sent to vulnResultCh (not written directly) so all updates
	// to lastVulnStatus happen on the main loop — no synchronization needed.
	go func() {
		select {
		case <-time.After(5 * time.Minute):
			result := vulnscan.GatherVulns(ctx, vulnscan.RunningContainers(ctx))
			sendVulnResult(vulnResultCh, result)
		case <-ctx.Done():
		}
	}()

	for {
		select {
		case <-hookSig:
			reconcileOnce("post-receive hook")
		case <-reconcileSig:
			reconcileOnce("manual reconcile signal")
		case <-ticker.C:
			reconcileOnce("timer backstop")
		case <-updateTicker:
			runRefResolveAndDrift(ctx, cfg.checkoutPath, updateCfg, auditLog, statusSrv)
		case <-vulnTicker.C:
			// Run the scan in a goroutine — trivy can take minutes on first run
			// (DB download) and must not block the main reconcile/backup loop.
			// Result is sent to vulnResultCh so lastVulnStatus is only ever
			// written on the main loop, eliminating the need for a mutex.
			go func() {
				result := vulnscan.GatherVulns(ctx, vulnscan.RunningContainers(ctx))
				sendVulnResult(vulnResultCh, result)
			}()
		case result := <-vulnResultCh:
			// Discard this result if a newer scan already finished. ScannedAt
			// is always set by GatherVulns (even on failure) so that overlapping
			// goroutines — the startup scan and a ticker-triggered scan — cannot
			// let a slow older scan overwrite a faster newer one.
			if result.ScannedAt.Before(lastVulnStatus.ScannedAt) {
				fmt.Fprintln(os.Stderr, "ownbased: vuln scan: discarding stale result (newer scan already applied)")
				break
			}
			// Always update — even Available=false must replace a stale
			// Available=true result (e.g. trivy removed after last scan).
			lastVulnStatus = result
			fmt.Fprintf(os.Stderr,
				"ownbased: vuln scan complete (available=%v, host: %dC/%dH/%dM, %d image(s))\n",
				result.Available, result.Host.Critical, result.Host.High, result.Host.Medium,
				len(result.Images))
			if !result.Available && result.TrivyInstalled && result.HostScanError != "" {
				fmt.Fprintf(os.Stderr, "ownbased: vuln scan: host scan failed: %s\n", result.HostScanError)
			}
			for _, img := range result.Images {
				if img.ScanFailed {
					fmt.Fprintf(os.Stderr, "ownbased: vuln scan: image scan failed for %q: %s\n", img.Service, img.ScanError)
				}
			}
			// Push updated status immediately — don't wait for the next reconcile.
			afterReconcile(lastReconcileState)
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "ownbased: shutting down")
			// Remove the PID file so the hook does not signal a dead process.
			_ = os.Remove(cfg.pidPath)
			return nil
		}
	}
}

// runRefResolveAndDrift reads the current ownbase.yaml, resolves any blank
// ref: fields by committing the default-branch HEAD SHA (read from each
// service's local bare repo) to the config repo, and then computes drift for
// all services with concrete refs. The drift snapshot is pushed into the
// status server so ownbasectl updates can read it. Non-fatal — a transient
// error for one service is logged and skipped.
func runRefResolveAndDrift(ctx context.Context, checkoutPath string, cfg update.Config, al authz.AuditLogger, statusSrv *explain.StatusServer) {
	cfgPath := filepath.Join(checkoutPath, "ownbase.yaml")
	oc, err := schema.ParseConfigFile(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: update: parse ownbase.yaml: %v\n", err)
		return
	}

	services := update.ServicesFromConfig(oc)

	// Resolve blank refs: commit default-branch HEAD SHA for any service with
	// no ref: set. These commits trigger the hook → reconcile → build path.
	update.ResolveBlankRefs(ctx, cfg, services, al)

	// Re-read config after blank-ref resolution so drift sees updated refs.
	if oc2, err := schema.ParseConfigFile(cfgPath); err == nil {
		services = update.ServicesFromConfig(oc2)
	}

	// Compute drift and cache in the status server.
	drift := update.ComputeDrift(ctx, cfg, services)
	if statusSrv != nil {
		driftStatus := explain.UpdateStatus{}
		for _, d := range drift {
			driftStatus.Drift = append(driftStatus.Drift, explain.ServiceDrift{
				Service:       d.Service,
				Ref:           d.Ref,
				Branch:        d.Branch,
				CommitsBehind: d.CommitsBehind,
				NewestTag:     d.NewestTag,
				UpToDate:      d.UpToDate,
			})
		}
		statusSrv.SetUpdates(driftStatus)
	}

	behind := 0
	for _, d := range drift {
		if !d.UpToDate {
			behind++
		}
	}
	if behind == 0 {
		fmt.Fprintln(os.Stderr, "ownbased: update: all services current")
	} else {
		fmt.Fprintf(os.Stderr, "ownbased: update: %d service(s) behind — run ownbasectl updates for details\n", behind)
	}
}

// readCoreConfigFromDisk reads the core: block from ownbase.yaml if it exists.
// Returns a zero-value CoreConfig (with all defaults applied by the schema) when
// the file is missing or cannot be parsed — so pass zero always has safe defaults.
func readCoreConfigFromDisk(checkoutPath string) schema.CoreConfig {
	cfgPath := filepath.Join(checkoutPath, "ownbase.yaml")
	cfg, err := schema.ParseConfigFile(cfgPath)
	if err != nil {
		return schema.CoreConfig{}
	}
	return cfg.Core
}

// hasPublicDomainOnDisk reports whether ownbase.yaml on disk currently has
// any service with a domain configured (schema.OwnbaseConfig.HasPublicDomain).
// Returns false — the safe default — when the file is missing or cannot be
// parsed, e.g. on a Base's very first boot before any config has been pushed.
// Used to gate both the firewall's web ports (pass zero) and Caddy's
// published ports (bootstrapCore) on the same signal.
func hasPublicDomainOnDisk(checkoutPath string) bool {
	cfgPath := filepath.Join(checkoutPath, "ownbase.yaml")
	cfg, err := schema.ParseConfigFile(cfgPath)
	if err != nil {
		return false
	}
	return cfg.HasPublicDomain()
}

// loadBackupConfig builds a backup.Config for one run. It parses ownbase.yaml
// fresh on every call (so volume declarations are always current), decrypts
// credentials from the conventional age-encrypted secret, and resolves all
// Podman volume mountpoints via BuildPaths. Credentials are refreshed on every
// call so a `ownbasectl secrets set backup` rotation takes effect without
// restart. Falls back gracefully when the config or volumes cannot be resolved.
func loadBackupConfig(cfg agentConfig, repo string, auditLog authz.AuditLogger) backup.Config {
	creds, err := secrets.IssueMap(
		secrets.FileKeyCustody{},
		filepath.Join(explain.DefaultSecretsDir, "backup.yaml.age"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: backup: load credentials: %v (falling back to env)\n", err)
		creds = nil
	}

	var paths []string
	oc, err := schema.ParseConfigFile(filepath.Join(cfg.checkoutPath, "ownbase.yaml"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: backup: parse ownbase.yaml: %v (falling back to default paths)\n", err)
	} else {
		resolved, err := backup.BuildPaths(context.Background(), oc, backup.PodmanVolumeResolver{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ownbased: backup: resolve volume paths: %v (falling back to default paths)\n", err)
		} else {
			paths = resolved
		}
	}

	return backup.Config{
		Repository:  repo,
		Paths:       paths,
		Credentials: creds,
		DryRun:      cfg.dryRun,
		AuditLog:    auditLog,
	}
}

// syncCoreForConfig re-applies the core package (Caddy) and firewall
// web-port exposure to match cfg's current domain configuration. Called on
// every reconcile tick (see reconcileLoop) — not just at daemon startup —
// so that adding or removing a service's domain takes effect immediately
// without requiring a daemon restart (see internal/install.ensureFirewall
// and bootstrapCore for why both used to only ever run once). Both
// underlying calls are cheap no-ops when nothing actually changed; errors
// are logged and non-fatal, since the next tick retries.
//
// dryRun skips both calls entirely (only logging what would happen) —
// bootstrapCore has no dry-run awareness of its own (it always writes
// Quadlet files and reloads/restarts systemd units), so without this guard
// `ownbased --dry-run` would still mutate UFW and restart Caddy on every
// reconcile tick even though the rest of that tick only previews its plan.
func syncCoreForConfig(ctx context.Context, agentCfg agentConfig, cfg *schema.OwnbaseConfig, dryRun bool) {
	hasPublicDomain := cfg.HasPublicDomain()
	if dryRun {
		fmt.Fprintf(os.Stderr, "ownbased: (dry-run) would sync core + firewall exposure (hasPublicDomain=%v)\n", hasPublicDomain)
		return
	}
	if err := bootstrapCore(ctx, agentCfg, cfg.Core, hasPublicDomain); err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: sync core (non-fatal): %v\n", err)
	}
	if s := install.SyncFirewallExposure(ctx, install.PassZeroConfig{
		SSHPort:        agentCfg.sshPort,
		ExposeWebPorts: hasPublicDomain,
	}); s.Err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: sync firewall (non-fatal): %v\n", s.Err)
	}
}

// reconcileLoop runs one full: update checkout → compile → drift check →
// diff → apply cycle. It is identical whether triggered by the hook or the
// timer, satisfying the Reconstruction Model's "same code path" requirement.
//
// It returns a reconcileState populated with whatever it successfully computed,
// so the caller can gather status even when an error occurred mid-cycle.
func reconcileLoop(
	agentCfg agentConfig,
	checkpoint authz.Checkpoint,
	applier reconcile.Applier,
	auditLog authz.AuditLogger,
	dryRun bool,
) (reconcileState, error) {
	checkoutPath, repoPath := agentCfg.checkoutPath, agentCfg.repoPath
	var state reconcileState

	// 1. Pull latest from the bare repo into the checkout.
	if err := githost.UpdateCheckout(repoPath, checkoutPath); err != nil {
		// Non-fatal on empty repo (no commits yet).
		fmt.Fprintf(os.Stderr, "ownbased: update checkout: %v\n", err)
	}

	// 2. Parse ownbase.yaml from the checkout.
	cfgPath := filepath.Join(checkoutPath, "ownbase.yaml")
	cfg, err := schema.ParseConfigFile(cfgPath)
	if err != nil {
		return state, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	state.Config = cfg
	for _, w := range cfg.Warnings() {
		fmt.Fprintf(os.Stderr, "ownbased: warning: %s\n", w)
	}

	// 2a. Re-sync the core package (Caddy) and firewall exposure against the
	// config just parsed. Must happen before compiling/diffing user services
	// below, matching the startup order ("core package is always healthy
	// before user services are reconciled") — and on every tick, not just
	// startup, so a newly-added domain opens 80/443 and gets Caddy's ports
	// without waiting for a daemon restart.
	syncCoreForConfig(context.Background(), agentCfg, cfg, dryRun)

	// 3. Ensure a local bare repo exists for every service: cloning mirror:
	// services from their external URL on first sight, and fetching any
	// pinned ref: not yet available locally. Idempotent and non-fatal — a
	// service whose external source is temporarily unreachable is skipped;
	// the reconcile continues with whatever is already on disk.
	//
	// Each repo is chowned to the admin SSH user (read fresh on every tick,
	// not cached) so a repo for a brand-new service is pushable immediately,
	// and so the daemon picks up a change to /opt/ownbase/admin-user without
	// a restart.
	adminUser := install.ReadAdminUser(install.AdminUserPath)
	for _, err := range repos.EnsureRepos(cfg, adminUser) {
		fmt.Fprintf(os.Stderr, "ownbased: ensure repos: %v (non-fatal)\n", err)
	}

	// 3b. Pin each source-built service to the exact commit its ref currently
	// resolves to, so the compiled unit's BuildRef changes when the branch
	// advances. Without this, a source service built from a branch (e.g. main)
	// never redeploys on push: the unit content is ref-name-stable, the diff
	// sees no change, and no rebuild/restart is triggered — new code silently
	// never ships. Best-effort: an unresolvable ref (empty repo, missing
	// commit) is left untouched so the build falls back to the branch name.
	pinSourceRefsToCommit(cfg)

	// 4. Compile desired state.
	desired := compiler.Compile(compiler.Input{Config: cfg})

	// 4a. Read the Caddyfile snapshot that is currently on disk — i.e. what
	// was actually deployed as of the end of the previous cycle — BEFORE
	// compiler.WriteOutput overwrites it with the newly-compiled desired
	// content below. Reading it after WriteOutput (as this used to do) means
	// the "did the Caddyfile change" comparison always sees identical
	// content and a reload never fires, even on a Base's very first boot
	// (where Caddy starts with its stock default config). caddyfileSnapshotAvailable
	// distinguishes "no snapshot exists yet" (err != nil — force a reload,
	// since we don't know what's actually deployed) from "a snapshot exists
	// and is byte-identical" (skip the reload) — both cases previously
	// looked identical (an empty string), silently hiding the bug.
	runtimeDir := filepath.Join(checkoutPath, "runtime")
	currentCaddyfileBeforeWrite, caddyfileReadErr := os.ReadFile(filepath.Join(runtimeDir, "Caddyfile"))
	caddyfileSnapshotAvailable := caddyfileReadErr == nil

	// On the daemon's first reconcile after startup, force a Caddy reload by
	// treating the snapshot as unavailable. A host reboot (or a manual restart
	// of the Caddy container) brings Caddy back up reading its stock on-disk
	// config — ownbase pushes the real routes only via the admin API, in memory
	// — so the on-disk snapshot can byte-match desired while the LIVE Caddy is
	// serving nothing. Without this, the empty-plan reboot case never re-pushes
	// routes and all TLS/routing stays down until an unrelated config change
	// happens to trigger a reload. A reload is graceful and idempotent (cached
	// certs are reused), so forcing one per startup is safe.
	//
	// The flag is only *peeked* here, not consumed: it is marked done at the
	// successful-return points below. If this reconcile fails before the reload
	// lands, the next tick re-forces it instead of silently skipping it forever.
	forceStartupCaddyReload := startupCaddyReloadPending()
	if forceStartupCaddyReload {
		caddyfileSnapshotAvailable = false
	}

	// 4b. Write the informational snapshot files (Caddyfile, docker-compose.yml)
	// before drift detection so they are always present when the detector runs.
	// These files are unconditionally regenerated from the compiler on every
	// cycle, so writing them here does not hide meaningful drift — it only
	// prevents a false-positive "missing_file" on first boot (when
	// bootstrapCore already started containers but the agent has never written
	// the snapshot yet).
	if _, err := compiler.WriteOutput(desired, checkoutPath); err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: write runtime snapshot (pre-drift): %v (non-fatal)\n", err)
	}

	// 5. Drift detection: compare compiler output to runtime/ on disk.
	driftEvents, err := reconcile.DetectDrift(desired, runtimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: drift check error: %v\n", err)
	} else if len(driftEvents) > 0 {
		fmt.Fprint(os.Stderr, "ownbased: DRIFT DETECTED:\n")
		fmt.Fprint(os.Stderr, reconcile.RenderDriftReport(driftEvents))
	}
	state.DriftEvents = driftEvents

	// 6b. Read currently-installed Quadlet unit files from the actual quadlet
	// directory (e.g. /etc/containers/systemd/). This is the authoritative
	// source for both:
	//   (a) Restart detection: comparing desired unit content against what is
	//       actually deployed on disk. Previously, currentUnits was read from
	//       runtime/ which compiler.WriteOutput had just overwritten with the
	//       desired content, making the comparison always equal and preventing
	//       restarts from ever being triggered.
	//   (b) Network/volume presence: detecting when a Quadlet file is missing
	//       from the quadlet dir even though the Podman object still exists.
	// Falls back to runtime/ in noop/dev builds (installedQuadletDir() == "").
	currentUnits := readRuntimeUnits(runtimeDir) // dev fallback
	var installedUnits map[string]bool
	if qd := installedQuadletDir(); qd != "" {
		currentUnits = readRuntimeUnits(qd) // actual quadlet dir
		installedUnits = make(map[string]bool, len(currentUnits))
		for filename := range currentUnits {
			installedUnits[filename] = true
			// Strip the apply-time secrets block so the diff compares the unit
			// against the compiler's secret-free output; otherwise the injected
			// EnvironmentFile= directive looks like drift and restarts the
			// container on every reconcile tick.
			currentUnits[filename] = podman.StripInjectedSecrets(currentUnits[filename])
		}
		// Also include any network/volume units not yet in currentUnits
		// (readRuntimeUnits only reads ownbase-prefixed files, which is correct).
	}

	// 7. Query actual running state.
	current, err := runtime.QueryCurrentState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: query state: %v (using empty state)\n", err)
		current = runtime.EmptyCurrentState()
	}
	state.Current = current

	// 8. Diff desired vs current.
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{
		CurrentCaddyfile:           string(currentCaddyfileBeforeWrite),
		CaddyfileSnapshotAvailable: caddyfileSnapshotAvailable,
		CurrentUnits:               currentUnits,
		InstalledUnits:             installedUnits,
	})
	if err != nil {
		return state, fmt.Errorf("diff: %w", err)
	}

	if plan.IsEmpty() {
		fmt.Fprintln(os.Stderr, "ownbased: already converged — no changes needed")
		if forceStartupCaddyReload {
			markStartupCaddyReloadDone()
		}
		return state, nil
	}

	fmt.Fprint(os.Stderr, "ownbased: plan:\n")
	fmt.Fprint(os.Stderr, reconcile.RenderPlanText(plan))

	// 9. Apply (or dry-run preview).
	if dryRun {
		return state, reconcile.ApplyDryRun(plan, checkpoint)
	}
	if err := reconcile.Apply(plan, checkpoint, applier, auditLog); err != nil {
		return state, err
	}
	// After a successful apply, sync ALL compiler output into runtime/ so
	// the drift detector sees the full desired snapshot on the next tick.
	// This covers files that have no corresponding action (e.g. docker-compose.yml)
	// and unit files that were skipped because the resource already existed.
	if _, err := compiler.WriteOutput(desired, checkoutPath); err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: write runtime snapshot: %v (non-fatal)\n", err)
	}
	// The forced post-startup reload (if any) has now been applied by a
	// successful reconcile; stop forcing it on subsequent ticks.
	if forceStartupCaddyReload {
		markStartupCaddyReloadDone()
	}
	return state, nil
}

// isCheckoutMissingError returns true when the reconcile error is caused by a
// missing ownbase.yaml, indicating that the checkout was not yet seeded by
// bootstrapCore (e.g. the initial push has not happened yet).
func isCheckoutMissingError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ownbase.yaml") &&
		(strings.Contains(msg, "no such file") || strings.Contains(msg, "not exist"))
}

// isConfigError returns true when the reconcile error originates from parsing
// ownbase.yaml (a permanent config problem that the operator must fix), rather
// than a transient infrastructure error.
func isConfigError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "parse") && strings.Contains(msg, "ownbase.yaml")
}

// readRuntimeUnits reads all Quadlet unit files from runtimeDir and returns
// a map of filename → content. Used by Diff to detect when a running
// container's unit content has changed (triggering a restart). Non-fatal:
// returns nil when the directory does not yet exist.
func readRuntimeUnits(runtimeDir string) map[string]string {
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil
	}
	units := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".container"),
			strings.HasSuffix(name, ".network"),
			strings.HasSuffix(name, ".volume"):
			data, err := os.ReadFile(filepath.Join(runtimeDir, name))
			if err == nil {
				units[name] = string(data)
			}
		}
	}
	return units
}

// startupCaddyReload guards a one-time forced Caddy reload on the first
// *successful* reconcile after this process started (see the call site for why).
var (
	startupCaddyReloadMu   sync.Mutex
	startupCaddyReloadDone bool
)

// startupCaddyReloadPending reports whether the one-time post-startup Caddy
// reload still needs to happen. It does NOT consume the flag — the reload is
// only recorded as done once a reconcile completes successfully (see
// markStartupCaddyReloadDone), so a reconcile that fails before the reload
// lands is retried on the next tick rather than skipped forever.
func startupCaddyReloadPending() bool {
	startupCaddyReloadMu.Lock()
	defer startupCaddyReloadMu.Unlock()
	return !startupCaddyReloadDone
}

// markStartupCaddyReloadDone records that the forced post-startup Caddy reload
// has been applied by a successful reconcile. Idempotent.
func markStartupCaddyReloadDone() {
	startupCaddyReloadMu.Lock()
	defer startupCaddyReloadMu.Unlock()
	startupCaddyReloadDone = true
}

// pinSourceRefsToCommit rewrites each source-built service's Ref to the commit
// SHA it currently resolves to in the service's local bare repo. This makes the
// compiled unit's BuildRef move with the branch tip, so a `git push` to a
// tracked branch produces a unit-content change that the reconcile diff detects
// and turns into a rebuild+restart. Mirror services (svc.Source == "") are left
// alone — they are pinned to an explicit external ref by the operator.
//
// Resolution is best-effort and mutates cfg in place; any service whose ref
// cannot be resolved (empty repo, missing commit, git not yet installed) is
// skipped, and the build falls back to the branch name as before.
func pinSourceRefsToCommit(cfg *schema.OwnbaseConfig) {
	if cfg == nil {
		return
	}
	for name, svc := range cfg.Services {
		if svc.Source == "" {
			continue
		}
		ref := svc.Ref
		if ref == "" {
			ref = "HEAD"
		}
		sha, err := gitResolveCommit(repos.RepoPath(svc.Source), ref)
		if err != nil || sha == "" {
			continue
		}
		svc.Ref = sha
		cfg.Services[name] = svc
	}
}

// gitResolveCommit returns the full commit SHA that ref resolves to in the bare
// repo at repoPath (the repo path is itself the git dir). The ^{commit}
// peeling ensures annotated tags resolve to their commit, not the tag object.
func gitResolveCommit(repoPath, ref string) (string, error) {
	out, err := exec.Command("git", "--git-dir="+repoPath, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sendVulnResult sends result to ch, replacing any already-queued result when
// this one is newer. This prevents a slow older goroutine from overwriting a
// faster newer scan whose result is already in the buffer waiting to be read.
//
// ch must have a buffer size of exactly 1.
func sendVulnResult(ch chan vulnscan.VulnStatus, result vulnscan.VulnStatus) {
	for {
		select {
		case ch <- result:
			return
		default:
		}
		// Channel full. Try to swap out the queued value if ours is newer.
		select {
		case queued := <-ch:
			if !result.ScannedAt.After(queued.ScannedAt) {
				// Queued result is same age or newer — restore it and drop ours.
				// Safe non-blocking: we hold the only item that was in the buffer.
				select {
				case ch <- queued:
				default:
				}
				return
			}
			// Ours is newer — loop to send it.
		default:
			// Channel was drained by the main loop between the two selects.
			// Loop to retry the initial send.
		}
	}
}
