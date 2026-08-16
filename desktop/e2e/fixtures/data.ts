// Canned ownbasectl --json documents for Tier-A Playwright tests.
// Shapes mirror desktop/src/lib/types.ts. Refresh from a live Base with:
//   npm run e2e:capture -- <base>

import type {
  BaseSummary,
  Checkup,
  ConfigPreview,
  KeygenResult,
  SessionMeta,
  VaultStatus,
} from "../../src/lib/types";

export const VAULT_PATH = "/tmp/ownbase-e2e/vault.kdbx";
export const MASTER_PASSWORD = "test-master-password";

export function lockedVault(overrides: Partial<VaultStatus> = {}): VaultStatus {
  return {
    running: true,
    unlocked: false,
    vault_path: VAULT_PATH,
    bases: 1,
    keys: 1,
    idle_timeout_seconds: 3600,
    pid: 4242,
    ssh_agent_socket: "/tmp/ownbase-e2e/agent.sock",
    version: "0.1.0-e2e",
    ...overrides,
  };
}

export function unlockedVault(overrides: Partial<VaultStatus> = {}): VaultStatus {
  return {
    ...lockedVault(),
    unlocked: true,
    unlocked_at: "2026-08-15T12:00:00Z",
    locks_at: "2026-08-15T13:00:00Z",
    bases: 1,
    keys: 1,
    ...overrides,
  };
}

export function absentVault(): VaultStatus {
  return {
    running: false,
    unlocked: false,
    bases: 0,
    keys: 0,
    idle_timeout_seconds: 0,
    pid: 0,
  };
}

export const demoBase: BaseSummary = {
  name: "demo",
  host: "192.168.64.10",
  kind: "vm",
  vm_state: "Running",
  registered: true,
  ssh_user: "ubuntu",
  ssh_port: 22,
  has_token: true,
  has_key: true,
  config_repo_url: "git@github.com:example/ownbase-config.git",
  config_ref: "main",
};

export const healthyCheckup: Checkup = {
  findings: [],
  status: {
    generated_at: "2026-08-15T12:00:00Z",
    schema_version: "1",
    version: "v0.4.0",
    config: {
      repo_url: "git@github.com:example/ownbase-config.git",
      ref: "main",
    },
    services: [
      {
        name: "web",
        running: true,
        healthy: true,
        health_probe_result: "ok",
        repo: "git@github.com:example/web.git",
        ref: "main",
        domain: "web.example.com",
        port: 8080,
      },
    ],
    jobs: [],
    security: {
      backup_restorable: true,
      last_verified: "2026-08-14T12:00:00Z",
      last_backup: "2026-08-15T06:00:00Z",
      drift_detected: false,
      cert_expiry_days: 60,
      disk_used_percent: 22,
      exposure: {
        available: true,
        firewall_active: true,
        unexpected_count: 0,
        listeners: [
          {
            port: 22,
            proto: "tcp",
            bind: "0.0.0.0",
            process: "sshd",
            internet_reachable: true,
            expected: true,
          },
        ],
      },
      access: {
        available: true,
        fail2ban_available: true,
        fail2ban_active: true,
        banned_ips: [],
        failed_attempts: 0,
        recent_logins: [],
      },
      vulns: {
        available: true,
        trivy_installed: true,
        scanned_at: "2026-08-15T08:00:00Z",
        host: { critical: 0, high: 0, medium: 2, low: 5 },
        images: [],
      },
    },
    updates: { drift: [] },
    audit: {
      total_seen: 3,
      recent_actions: [
        {
          time: "2026-08-15T11:00:00Z",
          action: "reconcile.apply",
          target: "web",
          outcome: "applied",
        },
      ],
    },
  },
  verify: { passed: true, checks: [{ name: "restore", passed: true, detail: "ok" }] },
};

export const findingsCheckup: Checkup = {
  ...healthyCheckup,
  findings: [
    {
      summary: "Host packages need patching",
      fix: "ownbasectl security fix demo",
      action: {
        kind: "run",
        run: "security fix",
        label: "Apply patches",
        confirm: "This installs available OS package updates on the Base.",
      },
    },
    {
      summary: "Backup has not been verified recently",
      fix: "ownbasectl checkup demo --verify",
      action: {
        kind: "open",
        tab: "backups",
        label: "Open backups",
      },
    },
  ],
  status: {
    ...healthyCheckup.status,
    security: {
      ...healthyCheckup.status.security,
      backup_restorable: false,
      reboot_required: true,
      reboot_packages: ["linux-image-6.8.0"],
      vulns: {
        available: true,
        trivy_installed: true,
        scanned_at: "2026-08-15T08:00:00Z",
        host: {
          critical: 1,
          high: 3,
          medium: 4,
          low: 10,
          fixable_critical: 1,
          fixable_high: 2,
        },
        images: [],
      },
    },
  },
};

export const demoKeygen: KeygenResult = {
  base: "fresh",
  public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2EfixturePublicKey ownbase-e2e",
  created: true,
  stored_in: VAULT_PATH,
};

export const serviceAddPreview: ConfigPreview = {
  status: "ok",
  would_change: true,
  commit_message: "service: add api",
  diff: `--- a/ownbase.yaml
+++ b/ownbase.yaml
@@ -1,3 +1,8 @@
 services:
+  api:
+    repo: git@github.com:example/api.git
+    ref: main
+    port: 8080
   web:
     repo: git@github.com:example/web.git
`,
  service: "api",
  action: "add",
};

function minutesAgo(n: number): string {
  return new Date(Date.now() - n * 60_000).toISOString();
}

export const demoSessions: SessionMeta[] = [
  {
    id: "20260815T120000Z-demo-abcd",
    base: "demo",
    host: "192.168.64.10",
    user: "ubuntu",
    interactive: true,
    started_at: minutesAgo(30),
    ended_at: minutesAgo(25),
    exit_code: 0,
    bytes: 4096,
    invoker: "cli",
    cast_path: "/tmp/ownbase-e2e/sessions/20260815T120000Z-demo-abcd.cast",
  },
  {
    id: "20260815T110000Z-demo-efgh",
    base: "demo",
    host: "192.168.64.10",
    user: "ubuntu",
    command: "systemctl status ownbased",
    interactive: false,
    started_at: minutesAgo(90),
    ended_at: minutesAgo(89),
    exit_code: 0,
    bytes: 512,
    invoker: "cli",
    cast_path: "/tmp/ownbase-e2e/sessions/20260815T110000Z-demo-efgh.cast",
  },
];

/** Minimal asciicast v2 header + one output frame. */
export const sampleCast = [
  JSON.stringify({
    version: 2,
    width: 80,
    height: 24,
    timestamp: 1690000000,
    env: { SHELL: "/bin/bash", TERM: "xterm-256color" },
  }),
  JSON.stringify([0.0, "o", "ubuntu@demo:~$ "]),
  JSON.stringify([0.1, "o", "echo hello\r\n"]),
  JSON.stringify([0.2, "o", "hello\r\n"]),
  JSON.stringify([0.3, "o", "ubuntu@demo:~$ "]),
].join("\n");

export const sampleTranscript = "ubuntu@demo:~$ echo hello\nhello\nubuntu@demo:~$ ";

/** Lines shaped like real create --wait progress so the wizard checklist advances. */
export const createStreamEvents = [
  { kind: "stderr" as const, line: "Provisioning local VM fresh…" },
  { kind: "stderr" as const, line: "VM launched." },
  { kind: "stderr" as const, line: "Transferring installer…" },
  { kind: "stderr" as const, line: "Running the installer…" },
  { kind: "stderr" as const, line: "Reading the API token…" },
  { kind: "stderr" as const, line: "Registered base fresh in the vault." },
  { kind: "stderr" as const, line: "Hardening the host…" },
  { kind: "finished" as const, code: 0 },
];

export const remoteCreateStreamEvents = [
  { kind: "stderr" as const, line: "Waiting for SSH…" },
  { kind: "stderr" as const, line: "Preflight checks passed." },
  { kind: "stderr" as const, line: "Installing OwnBase…" },
  { kind: "stderr" as const, line: "API token read." },
  { kind: "stderr" as const, line: "Registered base prod in the vault." },
  { kind: "stderr" as const, line: "Hardening the host…" },
  { kind: "finished" as const, code: 0 },
];

export const preflightFailEvents = [
  { kind: "stderr" as const, line: "Error: preflight failed: disk too small (need ≥ 18 GB)" },
  { kind: "finished" as const, code: 3 },
];

export const deployPreview: ConfigPreview = {
  status: "ok",
  would_change: true,
  commit_message: "deploy web @ abc1234",
  diff: `--- a/ownbase.yaml
+++ b/ownbase.yaml
@@ -2,3 +2,3 @@
   web:
-    ref: main
+    ref: abc1234
`,
  service: "web",
  action: "deploy",
  ref: "abc1234",
};

export const deployPreviewNoChange: ConfigPreview = {
  status: "ok",
  would_change: false,
  commit_message: "already current",
  diff: "",
  service: "web",
  action: "deploy",
};

export const backupSetupPreview: ConfigPreview = {
  status: "ok",
  would_change: true,
  commit_message: "backup: set up restic repo",
  diff: `--- a/ownbase.yaml
+++ b/ownbase.yaml
@@ -0,0 +1,4 @@
+backup:
+  repo: s3:bucket/path
+  interval: 1h
`,
};

export const serviceRemovePreview: ConfigPreview = {
  status: "ok",
  would_change: true,
  commit_message: "service: remove web",
  diff: `--- a/ownbase.yaml
+++ b/ownbase.yaml
@@ -1,6 +1,0 @@
-  web:
-    repo: git@github.com:example/web.git
`,
  service: "web",
  action: "remove",
};

export const upgradeCheckFixture = {
  status: "ok",
  packages: [
    {
      name: "caddy",
      image: "ghcr.io/ownbase/caddy:2",
      digest: "sha256:abc",
      running: true,
    },
  ],
};

export const dbStatusFixture = {
  stanza: "main",
  stanza_ok: true,
  postgres_version: "16",
  earliest_recovery: "2026-08-01T00:00:00Z",
  latest_recovery: "2026-08-15T12:00:00Z",
  backups: [
    {
      label: "20260815-060000F",
      type: "full",
      size_bytes: 1_024_000_000,
      started: "2026-08-15T06:00:00Z",
      stopped: "2026-08-15T06:10:00Z",
    },
  ],
};

export const dbRestoreOutcome = {
  into: "scratch",
  scratch_endpoint: "localhost:5433",
  databases: 1,
  relations: 12,
};

export const recoveryKitFixture = {
  repo: "s3:bucket/path",
  password: "restic-recovery-password",
  note: "Store offline.",
  restic_command: "restic -r s3:bucket/path snapshots",
  cloud_env_vars: ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"],
};

export const genericStreamOk = [
  { kind: "stderr" as const, line: "working…" },
  { kind: "stderr" as const, line: "done." },
  { kind: "finished" as const, code: 0 },
];

/** Checkup with backup configured so the Backups tab shows lifecycle + DB panels. */
export const backupsConfiguredCheckup: Checkup = {
  ...healthyCheckup,
  status: {
    ...healthyCheckup.status,
    security: {
      ...healthyCheckup.status.security,
      backup_restorable: true,
      last_backup: "2026-08-15T06:00:00Z",
      last_verified: "2026-08-14T12:00:00Z",
    },
    updates: {
      drift: [
        {
          service: "web",
          ref: "main",
          branch: "main",
          commits_behind: 3,
          newest_tag: "v1.2.0",
          up_to_date: false,
        },
      ],
    },
  },
};

/** Findings covering every FindingRow action.kind=run dispatch entry. */
export function findingsForRun(run: string, label: string, confirm?: string): Checkup {
  return {
    ...healthyCheckup,
    findings: [
      {
        summary: `Finding for ${run}`,
        fix: `ownbasectl ${run} demo`,
        action: {
          kind: "run",
          run,
          label,
          ...(confirm ? { confirm } : {}),
        },
      },
    ],
  };
}

export const deployFormFindingCheckup: Checkup = {
  ...backupsConfiguredCheckup,
  findings: [
    {
      summary: "web is behind its branch",
      fix: "ownbasectl deploy demo web --ref main",
      action: {
        kind: "form",
        form: "deploy",
        service: "web",
        suggested_ref: "main",
        label: "Update web",
        preview: true,
      },
    },
  ],
};
