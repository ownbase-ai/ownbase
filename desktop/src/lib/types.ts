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

/** One problem and the command that fixes it — cmd/ownbasectl.checkupFinding */
export interface Finding {
  summary: string;
  fix: string;
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
  services?: ServiceStatus[];
  jobs?: JobStatus[];
  security: SecurityStatus;
  updates: UpdateStatus;
  audit: AuditSummary;
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
