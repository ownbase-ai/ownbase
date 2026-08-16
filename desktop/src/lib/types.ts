// Mirrors of the JSON that `ownbasectl --json` prints.
//
// These are hand-written rather than generated, and the Go structs they mirror
// are named in comments so the pairing is findable. When a Go field changes,
// this file is the other half of the change.

/** `ownbasectl vault status --json` — internal/agentd.Status */
export interface VaultStatus {
  running: boolean;
  unlocked: boolean;
  vault_path?: string;
  bases: number;
  keys: number;
  unlocked_at?: string;
  idle_timeout_seconds: number;
  locks_at?: string;
  pid: number;
  ssh_agent_socket?: string;
  version?: string;
}

/** `ownbasectl list --json` — cmd/ownbasectl.listedBase */
export interface BaseSummary {
  name: string;
  host?: string;
  kind: "remote" | "vm" | "key-only" | "unregistered-vm";
  vm_state?: string;
  registered: boolean;
  ssh_user?: string;
  ssh_port?: number;
  has_token: boolean;
  has_key: boolean;
  config_repo_url?: string;
  config_ref?: string;
}

/** `ownbasectl sessions list --json` — internal/sshsession.Meta */
export interface SessionMeta {
  id: string;
  base: string;
  host: string;
  user: string;
  command?: string;
  interactive: boolean;
  started_at: string;
  ended_at?: string;
  exit_code: number;
  error?: string;
  bytes: number;
  invoker?: string;
  cast_path: string;
}

/** `ownbasectl keygen <name> --json` */
export interface KeygenResult {
  base: string;
  public_key: string;
  created: boolean;
  stored_in: string;
}

/** `ownbasectl vault init --json` */
export interface VaultInitResult {
  vault_path: string;
  created: boolean;
  unlocked: boolean;
  status: VaultStatus;
}

// ---------------------------------------------------------------------------
// The Base's own status document
// ---------------------------------------------------------------------------
//
// One request to the Base returns all of this, which is why the app makes one
// call per Base rather than five. `security`, `updates`, and `backup status`
// are all views onto the same document, and each of those CLI commands is that
// document with a section picked out.

/** `ownbasectl checkup <name> --json` — cmd/ownbasectl.printCheckupJSON */
export interface Checkup {
  /** What the CLI decided is worth attention. Empty means all clear. */
  findings: Finding[];
  status: BaseStatus;
  verify?: VerifyResult;
}

/**
 * How to address a finding — cmd/ownbasectl.checkupAction.
 *
 * The CLI decides the kind so the app never reimplements the rule:
 * - `run` — the app finishes it (security fix / scan / reboot / self-update)
 * - `open` — open a tab and read (nothing to execute)
 * - `form` — open a named form flow that still ends in a CLI call
 * - `manual` — genuine dead-end; plain text only
 */
export interface FindingAction {
  kind: "run" | "open" | "form" | "manual";
  /** ownbasectl subcommand path without the binary or base, e.g. "security fix". */
  run?: string;
  /** Desktop tab to open for kind=open. */
  tab?: "security" | "backups" | "updates";
  /** Named form flow for kind=form. */
  form?: "backup-setup" | "deploy" | "config-setup";
  /** Service the form targets (deploy). */
  service?: string;
  /** Default --ref for deploy. */
  suggested_ref?: string;
  /** Dry-run and show the diff before committing. */
  preview?: boolean;
  label: string;
  /** Prose shown before a run that has a cost (reboot / self-update). */
  confirm?: string;
}

/** Dry-run preview from deploy / backup setup / service / config set --dry-run --json. */
export interface ConfigPreview {
  status: string;
  would_change: boolean;
  commit_message: string;
  diff: string;
  current?: string;
  proposed?: string;
  service?: string;
  action?: string;
  ref?: string;
  repo?: string;
}

/** `ownbasectl secrets list <base> --json` */
export interface SecretsServicesList {
  services: string[];
}

/** `ownbasectl secrets list <base> <service> --json` */
export interface SecretsKeysList {
  service: string;
  keys: string[];
}

/** `ownbasectl secrets get <base> <service> <key> --json` */
export interface SecretValue {
  service: string;
  key: string;
  value: string;
}

/** `ownbasectl secrets set … --json` */
export interface SecretsSetResult {
  status: string;
  service: string;
  updated: number;
  escrow_warning?: string;
}

/** `ownbasectl secrets delete … --json` */
export interface SecretsDeleteResult {
  status: string;
  service: string;
  deleted: string;
  escrow_note?: string;
}

/** `ownbasectl upgrade <base> --json` (check-mode) — cmd/ownbasectl.corePackage */
export interface CorePackage {
  name: string;
  image: string;
  digest: string;
  running: boolean;
}

export interface UpgradeCheck {
  status: string;
  packages: CorePackage[];
}

/** `ownbasectl version --json` */
export interface CliVersion {
  version: string;
  commit: string;
  date: string;
  string: string;
}

/** One component in `ownbasectl version --check --json` — internal/release.Component */
export type VersionStatus = "current" | "behind" | "ahead" | "dev" | "unknown";

export interface VersionComponent {
  name: string;
  current: string;
  latest?: string;
  status: VersionStatus;
  /** Human update command when status is behind (CLI/app). Empty for daemon. */
  guide?: string;
}

/** CLI/daemon mismatch on one Base — internal/release.Skew */
export interface VersionSkew {
  direction: "cli_ahead" | "daemon_ahead";
  cli: string;
  daemon: string;
  guide: string;
  summary: string;
}

/** `ownbasectl version --check [--app-version] [base] --json` */
export interface VersionCheck {
  components: VersionComponent[];
  skew?: VersionSkew;
  manifest?: {
    error?: string;
    source?: string;
  };
}

/** `ownbasectl backup recovery-kit <base> --json` — cmd/ownbasectl.recoveryKit */
export interface RecoveryKit {
  repo: string;
  password: string;
  note?: string;
  /** Stock restic one-liner; JSON field is restic_command. */
  restic_command?: string;
  fingerprint?: string;
  cloud_env_vars?: string[];
}

/** `ownbasectl db status <base> --json` — cmd/ownbasectl.dbStatus */
export interface DBStatus {
  stanza?: string;
  stanza_ok?: boolean;
  stanza_message?: string;
  postgres_version?: string;
  backups?: Array<{
    label: string;
    type: string;
    size_bytes?: number;
    repo_size_bytes?: number;
    started?: string;
    stopped?: string;
    error?: boolean;
  }>;
  archive_min_wal?: string;
  archive_max_wal?: string;
  archiver?: {
    archived_count?: number;
    last_archived_wal?: string;
    last_archived_time?: string;
    failed_count?: number;
    last_failed_wal?: string;
    last_failed_time?: string;
  };
  earliest_recovery?: string;
  latest_recovery?: string;
}

/** `ownbasectl db restore … --json` — cmd/ownbasectl.dbRestoreOutcome */
export interface DBRestoreOutcome {
  into: string;
  target?: string;
  timeline?: string;
  databases?: number;
  relations?: number;
  last_transaction?: string;
  scratch_endpoint?: string;
  backup_after_promote?: boolean;
}

/** `ownbasectl vault recovery-string --json` */
export interface VaultRecoveryString {
  recovery_string: string;
  location: string;
}

/** One problem and how to address it — cmd/ownbasectl.checkupFinding */
export interface Finding {
  summary: string;
  /** Full command string for terminals and kind=manual. */
  fix: string;
  action: FindingAction;
}

/** The verified-restore drill's verdict — cmd/ownbased backup verify trailer */
export interface VerifyResult {
  passed: boolean;
  checks?: Array<{ name: string; passed: boolean; detail: string }>;
}

/** `ownbasectl status <name> --json` — internal/explain.BaseStatus */
export interface BaseStatus {
  generated_at: string;
  schema_version: string;
  /** Running ownbased release tag, e.g. "v0.4.0". */
  version?: string;
  /** External config repo the Base tracks — preferred over the vault profile. */
  config?: ConfigSourceStatus;
  services?: ServiceStatus[];
  jobs?: JobStatus[];
  security: SecurityStatus;
  updates: UpdateStatus;
  audit: AuditSummary;
}

/** internal/explain.ConfigSourceStatus */
export interface ConfigSourceStatus {
  repo_url: string;
  ref?: string;
}

/** internal/explain.ServiceStatus */
export interface ServiceStatus {
  name: string;
  running: boolean;
  healthy: boolean;
  health_probe_result?: string;
  repo?: string;
  ref?: string;
  domain?: string;
  domains?: string[];
  port?: number;
  requires?: string[];
}

/** internal/explain.JobStatus */
export interface JobStatus {
  name: string;
  service: string;
  schedule: string;
  command?: string[];
  timer_enabled: boolean;
  timer_active: boolean;
  next_run?: string;
  last_run?: string;
  last_result?: string;
}

/** internal/explain.SecurityStatus */
export interface SecurityStatus {
  backup_restorable: boolean;
  last_verified?: string;
  last_backup?: string;
  drift_detected: boolean;
  drift_count?: number;
  drift_files?: string[];
  cert_expiry_days?: number;
  disk_used_percent?: number;
  exposure: ExposureResult;
  access: AccessResult;
  vulns: VulnStatus;
  /** True when /var/run/reboot-required is present (usually a new kernel). */
  reboot_required?: boolean;
  reboot_packages?: string[];
}

/**
 * internal/secwatch.ExposureResult
 *
 * `available` is load-bearing: false means the probe could not run, which is
 * "unknown" and must never be rendered as "secure".
 */
export interface ExposureResult {
  available: boolean;
  firewall_active: boolean;
  unexpected_count: number;
  listeners?: Listener[];
}

/** internal/secwatch.Listener */
export interface Listener {
  port: number;
  proto: string;
  bind: string;
  process?: string;
  internet_reachable: boolean;
  expected: boolean;
}

/** internal/secwatch.AccessResult */
export interface AccessResult {
  available: boolean;
  fail2ban_available: boolean;
  fail2ban_active: boolean;
  banned_ips?: string[];
  failed_attempts: number;
  recent_logins?: LoginEvent[];
}

/** internal/secwatch.LoginEvent */
export interface LoginEvent {
  time: string;
  user: string;
  source_ip: string;
  method?: string;
}

/** internal/vulnscan.VulnStatus */
export interface VulnStatus {
  available: boolean;
  trivy_installed: boolean;
  scanned_at?: string;
  host_scan_error?: string;
  host: VulnSummary;
  images?: ImageVulns[];
  /** True while a trivy run is in flight. */
  scanning?: boolean;
  scan_started_at?: string;
  /** When /security/fix last finished; counts older than this are pre-patch. */
  last_patch_at?: string;
}

/** internal/vulnscan.VulnSummary */
export interface VulnSummary {
  critical: number;
  high: number;
  medium: number;
  low: number;
  fixable_critical?: number;
  fixable_high?: number;
  top?: VulnFinding[];
}

/** internal/vulnscan.VulnFinding */
export interface VulnFinding {
  vuln_id: string;
  package: string;
  version?: string;
  fixed_in?: string;
  severity: string;
  title?: string;
}

/** internal/vulnscan.ImageVulns */
export interface ImageVulns {
  service: string;
  image: string;
  summary: VulnSummary;
  scan_failed?: boolean;
  scan_error?: string;
}

/** internal/explain.UpdateStatus */
export interface UpdateStatus {
  drift?: ServiceDrift[];
}

/** internal/explain.ServiceDrift */
export interface ServiceDrift {
  service: string;
  ref: string;
  branch?: string;
  commits_behind: number;
  newest_tag?: string;
  up_to_date: boolean;
}

/** internal/explain.AuditSummary */
export interface AuditSummary {
  recent_actions?: RecentAction[];
  total_seen: number;
}

/**
 * internal/explain.RecentAction
 *
 * outcome mirrors the Outcome* constants in internal/authz/audit.go exactly —
 * the daemon never emits "success". Keeping this a union instead of `string`
 * is what makes a typo here (like the one this comment used to sit next to)
 * a compile error instead of every action rendering as a failure.
 */
export interface RecentAction {
  time: string;
  action: string;
  target: string;
  outcome: "applied" | "rolled_back" | "refused" | "error" | "rollback_error";
}
