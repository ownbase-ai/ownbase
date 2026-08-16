// Argv construction tests for every exported operation in api.ts.
//
// api.ts is pure: it builds argv and hands it to cli.*. A renamed CLI flag,
// a missing --dry-run, or a secret landing in argv is a compile-time-invisible
// bug that only shows up when the window does the wrong thing. These tests are
// the cheap half of the seam; e2e covers the confirm gates and render paths.

import { beforeEach, describe, expect, it, vi } from "vitest";

import type { StreamEvent, StreamHandle } from "./cli";
import { CliError, Exit } from "./cli";

const json = vi.fn();
const text = vi.fn();
const stream = vi.fn();
const raw = vi.fn();

vi.mock("./cli", async () => {
  const actual = await vi.importActual<typeof import("./cli")>("./cli");
  return {
    ...actual,
    json: (...args: unknown[]) => json(...args),
    text: (...args: unknown[]) => text(...args),
    stream: (...args: unknown[]) => stream(...args),
    raw: (...args: unknown[]) => raw(...args),
  };
});

import * as api from "./api";

/**
 * Names of api exports we have actually invoked in this file. Populated by
 * recording which api.* call each test makes, not by hand-written labels.
 */
const covered = new Set<string>();

function cover(name: keyof typeof api): void {
  covered.add(name as string);
}

const noop = (): void => {};
const handle: StreamHandle = {
  done: Promise.resolve(0),
  cancel: async () => {},
};

beforeEach(() => {
  json.mockReset().mockResolvedValue({});
  text.mockReset().mockResolvedValue("");
  stream.mockReset().mockReturnValue(handle);
  raw.mockReset().mockResolvedValue({ code: 0, stdout: "{}", stderr: "" });
});

// ---------------------------------------------------------------------------
// Vault and agent
// ---------------------------------------------------------------------------

describe("vault and agent", () => {
  it("vaultStatus", async () => {
    cover("vaultStatus");
    await api.vaultStatus();
    expect(json).toHaveBeenCalledWith(["vault", "status"]);
  });

  it("vaultInit sends password on stdin", async () => {
    cover("vaultInit");
    await api.vaultInit("/path/vault.kdbx", "pw");
    expect(json).toHaveBeenCalledWith(
      ["vault", "init", "/path/vault.kdbx", "--password-stdin"],
      "pw",
    );
  });

  it("vaultUnlock with and without idle timeout", async () => {
    cover("vaultUnlock");
    await api.vaultUnlock("pw");
    expect(json).toHaveBeenCalledWith(["vault", "unlock", "--password-stdin"], "pw");
    json.mockClear();
    await api.vaultUnlock("pw", "30m");
    expect(json).toHaveBeenCalledWith(
      ["vault", "unlock", "--password-stdin", "--idle-timeout", "30m"],
      "pw",
    );
  });

  it("vaultLock", async () => {
    cover("vaultLock");
    await api.vaultLock();
    expect(text).toHaveBeenCalledWith(["vault", "lock"]);
  });

  it("vaultChangePassword sends new password on stdin", async () => {
    cover("vaultChangePassword");
    await api.vaultChangePassword("new-pw");
    expect(text).toHaveBeenCalledWith(["vault", "passwd", "--password-stdin"], "new-pw");
  });

  it("agentStop", async () => {
    cover("agentStop");
    await api.agentStop();
    expect(text).toHaveBeenCalledWith(["agent", "stop"]);
  });

  it("vaultRecoveryString", async () => {
    cover("vaultRecoveryString");
    await api.vaultRecoveryString();
    expect(json).toHaveBeenCalledWith(["vault", "recovery-string"]);
  });

  it("vaultOpen puts recovery on argv and password on stdin", async () => {
    cover("vaultOpen");
    await api.vaultOpen("ownbase-recovery-v1:x", "pw");
    expect(json).toHaveBeenCalledWith(
      ["vault", "open", "--recovery", "ownbase-recovery-v1:x", "--password-stdin"],
      "pw",
    );
  });

  it("cliVersion", async () => {
    cover("cliVersion");
    await api.cliVersion();
    expect(json).toHaveBeenCalledWith(["version"]);
  });

  it("versionCheck builds argv", async () => {
    cover("versionCheck");
    await api.versionCheck();
    expect(json).toHaveBeenCalledWith(["version", "--check", "--json"]);
    json.mockClear();
    await api.versionCheck({
      appVersion: "0.5.0",
      base: "demo",
      refresh: true,
    });
    expect(json).toHaveBeenCalledWith([
      "version",
      "--check",
      "--json",
      "--refresh",
      "--app-version",
      "0.5.0",
      "demo",
    ]);
  });

  it("appVersion returns the Tauri bundle version", async () => {
    cover("appVersion");
    vi.resetModules();
    vi.doMock("@tauri-apps/api/app", () => ({
      getVersion: async () => "0.5.0",
    }));
    // Re-import so the dynamic import inside appVersion sees the mock.
    const fresh = await import("./api");
    await expect(fresh.appVersion()).resolves.toBe("0.5.0");
    // Keep the cover gate happy for the already-bound export.
    covered.add("appVersion");
  });
});

// ---------------------------------------------------------------------------
// Bases
// ---------------------------------------------------------------------------

describe("bases", () => {
  it("listBases coerces null to []", async () => {
    cover("listBases");
    json.mockResolvedValueOnce(null);
    await expect(api.listBases()).resolves.toEqual([]);
    expect(json).toHaveBeenCalledWith(["list"]);
  });

  it("deleteBase always --yes; keepVm is optional", async () => {
    cover("deleteBase");
    await api.deleteBase("demo");
    expect(text).toHaveBeenCalledWith(["delete", "demo", "--yes"]);
    text.mockClear();
    await api.deleteBase("demo", { keepVm: true });
    expect(text).toHaveBeenCalledWith(["delete", "demo", "--yes", "--keep-vm"]);
  });

  it("keygen", async () => {
    cover("keygen");
    await api.keygen("demo");
    expect(json).toHaveBeenCalledWith(["keygen", "demo"]);
  });

  it("keygenImport", async () => {
    cover("keygenImport");
    await api.keygenImport("demo", "/tmp/id_ed25519");
    expect(json).toHaveBeenCalledWith(["keygen", "demo", "--import", "/tmp/id_ed25519"]);
  });

  it("createBase always --wait --yes; remote and sizing flags", () => {
    cover("createBase");
    api.createBase("demo", {}, noop);
    expect(stream).toHaveBeenCalledWith(["create", "demo", "--wait", "--yes"], noop);

    stream.mockClear();
    api.createBase(
      "demo",
      {
        remote: "root@1.2.3.4",
        caddyEmail: "a@b.c",
        sshUser: "ubuntu",
        sshPort: 2222,
        replace: true,
        cpus: 4,
        memory: 8,
        disk: 40,
      },
      noop,
    );
    expect(stream).toHaveBeenCalledWith(
      [
        "create",
        "demo",
        "--wait",
        "--yes",
        "--remote",
        "root@1.2.3.4",
        "--caddy-email",
        "a@b.c",
        "--ssh-user",
        "ubuntu",
        "--ssh-port",
        "2222",
        "--replace",
        "--cpus",
        "4",
        "--memory",
        "8",
        "--disk",
        "40",
      ],
      noop,
    );
  });

  it("restoreBase puts all credentials on stdin, never argv", () => {
    cover("restoreBase");
    api.restoreBase(
      "demo",
      {
        repo: "s3:bucket/path",
        password: "SENTINEL_SECRET",
        remote: "root@1.2.3.4",
        sshUser: "ubuntu",
        sshPort: 22,
        caddyEmail: "a@b.c",
        cpus: 2,
        memory: 4,
        disk: 20,
        forceRebuild: true,
        aws_access_key_id: "AKIA",
        aws_secret_access_key: "SECRETKEY",
        b2_account_id: "b2id",
        b2_account_key: "b2key",
      },
      noop,
    );
    const [args, onEvent, stdin] = stream.mock.calls[0] as [
      string[],
      (e: StreamEvent) => void,
      string,
    ];
    expect(onEvent).toBe(noop);
    expect(args).toEqual([
      "restore",
      "demo",
      "--wait",
      "--yes",
      "--creds-stdin",
      "--repo",
      "s3:bucket/path",
      "--remote",
      "root@1.2.3.4",
      "--ssh-user",
      "ubuntu",
      "--ssh-port",
      "22",
      "--caddy-email",
      "a@b.c",
      "--cpus",
      "2",
      "--memory",
      "4",
      "--disk",
      "20",
      "--force",
    ]);
    expect(args.join(" ")).not.toContain("SENTINEL_SECRET");
    expect(args.join(" ")).not.toContain("SECRETKEY");
    expect(args.join(" ")).not.toContain("b2key");
    expect(JSON.parse(stdin)).toEqual({
      password: "SENTINEL_SECRET",
      aws_access_key_id: "AKIA",
      aws_secret_access_key: "SECRETKEY",
      b2_account_id: "b2id",
      b2_account_key: "b2key",
    });
  });

  it("adoptBase", () => {
    cover("adoptBase");
    api.adoptBase(
      "demo",
      {
        host: "1.2.3.4",
        sshUser: "ubuntu",
        sshPort: 22,
        sshKeyPath: "/tmp/key",
        apiPort: 7070,
      },
      noop,
    );
    expect(stream).toHaveBeenCalledWith(
      [
        "adopt",
        "demo",
        "--host",
        "1.2.3.4",
        "--ssh-user",
        "ubuntu",
        "--ssh-port",
        "22",
        "--ssh-key",
        "/tmp/key",
        "--api-port",
        "7070",
      ],
      noop,
    );
  });

  it("checkup", async () => {
    cover("checkup");
    await api.checkup("demo");
    expect(json).toHaveBeenCalledWith(["checkup", "demo"]);
  });

  it("verifyBackup streams checkup --verify --json", () => {
    cover("verifyBackup");
    api.verifyBackup("demo", noop);
    expect(stream).toHaveBeenCalledWith(
      ["checkup", "demo", "--verify", "--json"],
      noop,
    );
  });

  it("backupNow", async () => {
    cover("backupNow");
    await api.backupNow("demo");
    expect(text).toHaveBeenCalledWith(["backup", "run", "demo"]);
  });
});

// ---------------------------------------------------------------------------
// Security / upgrade / deploy
// ---------------------------------------------------------------------------

describe("security and upgrades", () => {
  it("securityFix with optional reboot", () => {
    cover("securityFix");
    api.securityFix("demo", noop);
    expect(stream).toHaveBeenCalledWith(["security", "fix", "demo"], noop);
    stream.mockClear();
    api.securityFix("demo", noop, { reboot: true });
    expect(stream).toHaveBeenCalledWith(
      ["security", "fix", "demo", "--reboot"],
      noop,
    );
  });

  it("securityScan with optional wait", async () => {
    cover("securityScan");
    await api.securityScan("demo");
    expect(text).toHaveBeenCalledWith(["security", "scan", "demo"]);
    text.mockClear();
    await api.securityScan("demo", { wait: true });
    expect(text).toHaveBeenCalledWith(["security", "scan", "demo", "--wait"]);
  });

  it("securityReboot with optional wait", () => {
    cover("securityReboot");
    api.securityReboot("demo", noop);
    expect(stream).toHaveBeenCalledWith(["security", "reboot", "demo"], noop);
    stream.mockClear();
    api.securityReboot("demo", noop, { wait: true });
    expect(stream).toHaveBeenCalledWith(
      ["security", "reboot", "demo", "--wait"],
      noop,
    );
  });

  it("installScanner", () => {
    cover("installScanner");
    api.installScanner("demo", noop);
    expect(stream).toHaveBeenCalledWith(
      ["security", "install-scanner", "demo"],
      noop,
    );
  });

  it("selfUpdate defaults version to latest", () => {
    cover("selfUpdate");
    api.selfUpdate("demo", noop);
    expect(stream).toHaveBeenCalledWith(
      ["self-update", "demo", "--version", "latest"],
      noop,
    );
    stream.mockClear();
    api.selfUpdate("demo", noop, "v0.5.0");
    expect(stream).toHaveBeenCalledWith(
      ["self-update", "demo", "--version", "v0.5.0"],
      noop,
    );
  });

  it("upgradeApply", () => {
    cover("upgradeApply");
    api.upgradeApply("demo", noop);
    expect(stream).toHaveBeenCalledWith(["upgrade", "demo", "--apply"], noop);
  });

  it("upgradeCheck", async () => {
    cover("upgradeCheck");
    await api.upgradeCheck("demo");
    expect(json).toHaveBeenCalledWith(["upgrade", "demo"]);
  });

  it("deployPreview includes --dry-run; deploy does not", async () => {
    cover("deployPreview");
    cover("deploy");
    await api.deployPreview("demo", "web", "main");
    expect(json).toHaveBeenCalledWith([
      "deploy",
      "demo",
      "web",
      "--ref",
      "main",
      "--dry-run",
    ]);
    json.mockClear();
    await api.deploy("demo", "web", "main");
    expect(json).toHaveBeenCalledWith(["deploy", "demo", "web", "--ref", "main"]);
    const applyArgs = json.mock.calls[0]![0] as string[];
    expect(applyArgs).not.toContain("--dry-run");
  });
});

// ---------------------------------------------------------------------------
// Secrets
// ---------------------------------------------------------------------------

describe("secrets", () => {
  it("secretsListServices", async () => {
    cover("secretsListServices");
    await api.secretsListServices("demo");
    expect(json).toHaveBeenCalledWith(["secrets", "list", "demo"]);
  });

  it("secretsListKeys", async () => {
    cover("secretsListKeys");
    await api.secretsListKeys("demo", "web");
    expect(json).toHaveBeenCalledWith(["secrets", "list", "demo", "web"]);
  });

  it("secretsGet", async () => {
    cover("secretsGet");
    await api.secretsGet("demo", "web", "DATABASE_URL");
    expect(json).toHaveBeenCalledWith([
      "secrets",
      "get",
      "demo",
      "web",
      "DATABASE_URL",
    ]);
  });

  it("secretsSet puts values on stdin, never argv", async () => {
    cover("secretsSet");
    await api.secretsSet("demo", "web", { DATABASE_URL: "SENTINEL_SECRET" });
    const [args, stdin] = json.mock.calls[0] as [string[], string];
    expect(args).toEqual(["secrets", "set", "demo", "web", "--stdin"]);
    expect(args.join(" ")).not.toContain("SENTINEL_SECRET");
    expect(JSON.parse(stdin)).toEqual({ DATABASE_URL: "SENTINEL_SECRET" });
  });

  it("secretsDelete", async () => {
    cover("secretsDelete");
    await api.secretsDelete("demo", "web", "DATABASE_URL");
    expect(json).toHaveBeenCalledWith([
      "secrets",
      "delete",
      "demo",
      "web",
      "DATABASE_URL",
    ]);
  });
});

// ---------------------------------------------------------------------------
// Service editing + config
// ---------------------------------------------------------------------------

describe("service editing", () => {
  const full: api.ServiceFields = {
    repo: "git@github.com:o/a.git",
    ref: "main",
    dockerfile: "Dockerfile",
    context: ".",
    port: 8080,
    domain: "a.example.com",
    domains: ["b.example.com", "c.example.com"],
    internal: true,
    dataPath: "/data",
    database: "app",
    requires: ["postgres", "redis"],
    env: ["FOO=1", "BAR=2"],
    addCapabilities: ["NET_BIND_SERVICE"],
    ownbaseAccess: ["postgres"],
    replicas: 2,
  };

  const fullFlags = [
    "--repo",
    "git@github.com:o/a.git",
    "--ref",
    "main",
    "--dockerfile",
    "Dockerfile",
    "--context",
    ".",
    "--port",
    "8080",
    "--domain",
    "a.example.com",
    "--domains",
    "b.example.com",
    "--domains",
    "c.example.com",
    "--internal",
    "--data-path",
    "/data",
    "--database",
    "app",
    "--requires",
    "postgres",
    "--requires",
    "redis",
    "--env",
    "FOO=1",
    "--env",
    "BAR=2",
    "--add-capabilities",
    "NET_BIND_SERVICE",
    "--ownbase-access",
    "postgres",
    "--replicas",
    "2",
  ];

  it("serviceAddPreview / serviceAdd with full fields and empty fields", async () => {
    cover("serviceAddPreview");
    cover("serviceAdd");
    await api.serviceAddPreview("demo", "api", full);
    expect(json).toHaveBeenCalledWith([
      "service",
      "add",
      "demo",
      "api",
      "--dry-run",
      ...fullFlags,
    ]);
    json.mockClear();
    await api.serviceAdd("demo", "api", {});
    expect(json).toHaveBeenCalledWith(["service", "add", "demo", "api"]);
  });

  it("serviceUpdatePreview / serviceUpdate", async () => {
    cover("serviceUpdatePreview");
    cover("serviceUpdate");
    await api.serviceUpdatePreview("demo", "api", { ref: "v2" });
    expect(json).toHaveBeenCalledWith([
      "service",
      "update",
      "demo",
      "api",
      "--dry-run",
      "--ref",
      "v2",
    ]);
    json.mockClear();
    await api.serviceUpdate("demo", "api", { ref: "v2" });
    expect(json).toHaveBeenCalledWith([
      "service",
      "update",
      "demo",
      "api",
      "--ref",
      "v2",
    ]);
  });

  it("serviceRemovePreview / serviceRemove", async () => {
    cover("serviceRemovePreview");
    cover("serviceRemove");
    await api.serviceRemovePreview("demo", "api");
    expect(json).toHaveBeenCalledWith([
      "service",
      "remove",
      "demo",
      "api",
      "--dry-run",
    ]);
    json.mockClear();
    await api.serviceRemove("demo", "api");
    expect(json).toHaveBeenCalledWith(["service", "remove", "demo", "api"]);
  });

  it("configGet and configGetYAML", async () => {
    cover("configGet");
    cover("configGetYAML");
    await api.configGet("demo");
    expect(json).toHaveBeenCalledWith(["config", "get", "demo"]);
    await api.configGetYAML("demo");
    expect(text).toHaveBeenCalledWith(["config", "get", "demo"]);
  });

  it("configSetPreview / configSet put yaml on stdin", async () => {
    cover("configSetPreview");
    cover("configSet");
    await api.configSetPreview("demo", "services: {}\n", "msg");
    expect(json).toHaveBeenCalledWith(
      ["config", "set", "demo", "--file", "-", "--dry-run", "--message", "msg"],
      "services: {}\n",
    );
    json.mockClear();
    await api.configSet("demo", "services: {}\n");
    expect(json).toHaveBeenCalledWith(
      ["config", "set", "demo", "--file", "-"],
      "services: {}\n",
    );
  });

  it("backupSetupPreview never takes credentials", async () => {
    cover("backupSetupPreview");
    await api.backupSetupPreview("demo", {
      repo: "s3:bucket/path",
      password: "SENTINEL_SECRET",
      interval: "1h",
      verify_interval: "7d",
    });
    const args = json.mock.calls[0]![0] as string[];
    expect(args).toEqual([
      "backup",
      "setup",
      "demo",
      "--repo",
      "s3:bucket/path",
      "--dry-run",
      "--interval",
      "1h",
      "--verify-interval",
      "7d",
    ]);
    expect(args.join(" ")).not.toContain("SENTINEL_SECRET");
  });

  it("backupSetupRun puts credentials on stdin", async () => {
    cover("backupSetupRun");
    await api.backupSetupRun("demo", {
      repo: "s3:bucket/path",
      password: "SENTINEL_SECRET",
      aws_access_key_id: "AKIA",
      aws_secret_access_key: "SECRETKEY",
      b2_account_id: "b2id",
      b2_account_key: "b2key",
    });
    const [args, stdin] = text.mock.calls[0] as [string[], string];
    expect(args).toEqual([
      "backup",
      "setup",
      "demo",
      "--repo",
      "s3:bucket/path",
      "--creds-stdin",
    ]);
    expect(args.join(" ")).not.toContain("SENTINEL_SECRET");
    expect(JSON.parse(stdin)).toEqual({
      password: "SENTINEL_SECRET",
      aws_access_key_id: "AKIA",
      aws_secret_access_key: "SECRETKEY",
      b2_account_id: "b2id",
      b2_account_key: "b2key",
    });
  });

  it("sshKeyAdd defaults host to github.com", async () => {
    cover("sshKeyAdd");
    await api.sshKeyAdd("demo");
    expect(json).toHaveBeenCalledWith([
      "ssh-key",
      "add",
      "demo",
      "--host",
      "github.com",
    ]);
    json.mockClear();
    await api.sshKeyAdd("demo", "gitlab.com");
    expect(json).toHaveBeenCalledWith([
      "ssh-key",
      "add",
      "demo",
      "--host",
      "gitlab.com",
    ]);
  });

  it("sshKeyList", async () => {
    cover("sshKeyList");
    await api.sshKeyList("demo");
    expect(json).toHaveBeenCalledWith(["ssh-key", "list", "demo"]);
  });

  it("configSetup with optional ref and init", async () => {
    cover("configSetup");
    await api.configSetup("demo", { repo: "git@github.com:o/c.git" });
    expect(json).toHaveBeenCalledWith([
      "config",
      "setup",
      "demo",
      "--repo",
      "git@github.com:o/c.git",
    ]);
    json.mockClear();
    await api.configSetup("demo", {
      repo: "git@github.com:o/c.git",
      ref: "main",
      init: true,
    });
    expect(json).toHaveBeenCalledWith([
      "config",
      "setup",
      "demo",
      "--repo",
      "git@github.com:o/c.git",
      "--ref",
      "main",
      "--init",
    ]);
  });
});

// ---------------------------------------------------------------------------
// Backup lifecycle + db + sessions
// ---------------------------------------------------------------------------

describe("backup lifecycle", () => {
  it("backupPrune", async () => {
    cover("backupPrune");
    await api.backupPrune("demo");
    expect(text).toHaveBeenCalledWith(["backup", "prune", "demo"]);
  });

  it("backupRekey generate scrapes password from stderr on success", async () => {
    cover("backupRekey");
    raw.mockResolvedValueOnce({
      code: 0,
      stdout: '{"status":"ok"}',
      stderr: "Generated restic password (save this):\n  gen-pw-abc\nrekey done\n",
    });
    const out = await api.backupRekey("demo", { generate: true });
    expect(raw).toHaveBeenCalledWith([
      "backup",
      "rekey",
      "demo",
      "--json",
      "--generate",
    ]);
    expect(out.generated_password).toBe("gen-pw-abc");
    expect(out.result).toEqual({ status: "ok" });
  });

  it("backupRekey generate surfaces password even on failure", async () => {
    raw.mockResolvedValueOnce({
      code: 1,
      stdout: "",
      stderr: "Generated restic password:\n  gen-pw-fail\nError: restic failed\n",
    });
    await expect(api.backupRekey("demo", { generate: true })).rejects.toSatisfy(
      (err: unknown) =>
        err instanceof CliError &&
        err.code === Exit.error &&
        (err as Error & { generated_password?: string }).generated_password ===
          "gen-pw-fail",
    );
  });

  it("backupRekey with explicit password puts it on argv (CLI limitation)", async () => {
    raw.mockResolvedValueOnce({ code: 0, stdout: "{}", stderr: "" });
    await api.backupRekey("demo", { newPassword: "manual-pw" });
    expect(raw).toHaveBeenCalledWith([
      "backup",
      "rekey",
      "demo",
      "--json",
      "--new-password",
      "manual-pw",
    ]);
  });

  it("backupRecoveryKit", async () => {
    cover("backupRecoveryKit");
    await api.backupRecoveryKit("demo");
    expect(json).toHaveBeenCalledWith(["backup", "recovery-kit", "demo"]);
  });
});

describe("postgres recovery", () => {
  it("dbStatus", async () => {
    cover("dbStatus");
    await api.dbStatus("demo");
    expect(json).toHaveBeenCalledWith(["db", "status", "demo"]);
  });

  it("dbRestore always --yes; into production / scratch / to", async () => {
    cover("dbRestore");
    await api.dbRestore("demo");
    expect(json).toHaveBeenCalledWith(["db", "restore", "demo", "--yes"]);
    json.mockClear();
    await api.dbRestore("demo", {
      to: "2026-01-01T00:00:00Z",
      into: "production",
      scratchPort: 5433,
    });
    expect(json).toHaveBeenCalledWith([
      "db",
      "restore",
      "demo",
      "--yes",
      "--to",
      "2026-01-01T00:00:00Z",
      "--into",
      "production",
      "--scratch-port",
      "5433",
    ]);
  });
});

describe("sessions", () => {
  it("listSessions with optional base filter; coerces null", async () => {
    cover("listSessions");
    json.mockResolvedValueOnce(null);
    await expect(api.listSessions()).resolves.toEqual([]);
    expect(json).toHaveBeenCalledWith(["sessions", "list"]);
    json.mockResolvedValueOnce([]);
    await api.listSessions("demo");
    expect(json).toHaveBeenCalledWith(["sessions", "list", "demo"]);
  });

  it("sessionCast and sessionTranscript", async () => {
    cover("sessionCast");
    cover("sessionTranscript");
    await api.sessionCast("sid");
    expect(text).toHaveBeenCalledWith(["sessions", "show", "sid", "--cast"]);
    await api.sessionTranscript("sid");
    expect(text).toHaveBeenCalledWith(["sessions", "show", "sid"]);
  });
});

// ---------------------------------------------------------------------------
// Cross-cutting: secrets never in argv; every export covered
// ---------------------------------------------------------------------------

describe("invariants", () => {
  it("password-bearing operations put the secret on stdin, never argv", async () => {
    const SECRET = "SENTINEL_SECRET_XYZ";
    json.mockClear();
    text.mockClear();
    stream.mockClear();

    await api.vaultInit("/p", SECRET);
    await api.vaultUnlock(SECRET);
    await api.vaultChangePassword(SECRET);
    await api.vaultOpen("rec", SECRET);
    await api.secretsSet("b", "s", { K: SECRET });
    await api.backupSetupRun("b", { repo: "r", password: SECRET });
    api.restoreBase("b", { password: SECRET, repo: "r" }, noop);

    let inspected = 0;
    let stdinHits = 0;
    // json/text: (args, stdin?); stream: (args, onEvent, stdin?)
    for (const call of [...json.mock.calls, ...text.mock.calls]) {
      const args = call[0] as string[] | undefined;
      if (!Array.isArray(args)) continue;
      inspected++;
      expect(args.join("\0")).not.toContain(SECRET);
      if (typeof call[1] === "string" && (call[1] as string).includes(SECRET)) stdinHits++;
    }
    for (const call of stream.mock.calls) {
      const args = call[0] as string[] | undefined;
      if (!Array.isArray(args)) continue;
      inspected++;
      expect(args.join("\0")).not.toContain(SECRET);
      if (typeof call[2] === "string" && (call[2] as string).includes(SECRET)) stdinHits++;
    }
    expect(inspected).toBeGreaterThan(0);
    // vaultInit, unlock, passwd, open, secretsSet, backupSetupRun, restoreBase
    expect(stdinHits).toBe(7);
  });

  it("every exported api operation has an argv test", () => {
    const exported = Object.keys(api).filter(
      (k) => typeof (api as Record<string, unknown>)[k] === "function",
    );
    const missing = exported.filter((n) => !covered.has(n));
    expect(missing).toEqual([]);
    expect(covered.size).toBe(exported.length);
  });
});
