// Guard the CLI ↔ app JSON seam.
//
// Both e2e/fixtures/data.ts (hand-written) and desktop/src/lib/types.ts are
// authored by humans from the same Go structs. A field renamed on the Go side
// can pass typecheck, unit tests, and hermetic e2e until someone refreshes the
// golden files under e2e/fixtures/captured/ from a live Base:
//
//   npm run e2e:capture -- <base>
//
// This suite:
//   1. Decodes each captured doc against the TypeScript types the app uses.
//   2. Fails on unknown top-level keys (the rename signal).
//   3. Asserts hand-written fixtures stay structurally congruent with capture.

import { describe, expect, it } from "vitest";

import checkupCaptured from "../../e2e/fixtures/captured/checkup.json";
import listCaptured from "../../e2e/fixtures/captured/list.json";
import sessionsCaptured from "../../e2e/fixtures/captured/sessions-list.json";
import vaultStatusCaptured from "../../e2e/fixtures/captured/vault-status.json";
import versionCaptured from "../../e2e/fixtures/captured/version.json";
import {
  backupsConfiguredCheckup,
  demoBase,
  demoSessions,
  healthyCheckup,
} from "../../e2e/fixtures/data";
import type {
  BaseSummary,
  Checkup,
  CliVersion,
  SessionMeta,
  VaultStatus,
} from "./types";

/** Every key in `sample` must exist on `captured` (required-field drift). */
function assertSubsetKeys(
  label: string,
  sample: unknown,
  captured: unknown,
  path = "",
): void {
  if (sample === null || sample === undefined) return;
  if (typeof sample !== "object") return;
  if (Array.isArray(sample)) {
    if (!Array.isArray(captured) || captured.length === 0 || sample.length === 0) {
      return;
    }
    assertSubsetKeys(label, sample[0], captured[0], `${path}[]`);
    return;
  }
  if (!captured || typeof captured !== "object" || Array.isArray(captured)) {
    throw new Error(`${label}: expected object at ${path || "/"}`);
  }
  const cap = captured as Record<string, unknown>;
  for (const [k, v] of Object.entries(sample as Record<string, unknown>)) {
    if (v === undefined) continue;
    expect(cap, `${label}: missing key ${path}/${k}`).toHaveProperty(k);
    if (v !== null && typeof v === "object") {
      assertSubsetKeys(label, v, cap[k], `${path}/${k}`);
    }
  }
}

/** Keys present on the captured doc that types.ts does not declare at this level. */
function unknownTopLevel(
  captured: Record<string, unknown>,
  allowed: readonly string[],
): string[] {
  const allow = new Set(allowed);
  return Object.keys(captured).filter((k) => !allow.has(k)).sort();
}

describe("captured CLI JSON shapes", () => {
  it("vault-status decodes as VaultStatus; no unknown top-level keys", () => {
    const raw = vaultStatusCaptured as VaultStatus & Record<string, unknown>;
    const typed: VaultStatus = raw;
    expect(typeof typed.running).toBe("boolean");
    expect(typeof typed.unlocked).toBe("boolean");
    expect(typeof typed.bases).toBe("number");
    expect(typeof typed.keys).toBe("number");
    expect(typeof typed.idle_timeout_seconds).toBe("number");
    expect(typeof typed.pid).toBe("number");

    expect(
      unknownTopLevel(raw, [
        "running",
        "unlocked",
        "vault_path",
        "bases",
        "keys",
        "unlocked_at",
        "idle_timeout_seconds",
        "locks_at",
        "pid",
        "ssh_agent_socket",
        "version",
      ]),
    ).toEqual([]);
  });

  it("list decodes as BaseSummary[]; no unknown top-level keys per row", () => {
    const raw = listCaptured as BaseSummary[];
    expect(Array.isArray(raw)).toBe(true);
    expect(raw.length).toBeGreaterThan(0);
    const allowed = [
      "name",
      "host",
      "kind",
      "vm_state",
      "registered",
      "ssh_user",
      "ssh_port",
      "has_token",
      "has_key",
      "config_repo_url",
      "config_ref",
    ] as const;
    for (const row of raw) {
      const typed: BaseSummary = row;
      expect(typed.name).toBeTruthy();
      expect(["remote", "vm", "key-only", "unregistered-vm"]).toContain(typed.kind);
      expect(typeof typed.registered).toBe("boolean");
      expect(typeof typed.has_token).toBe("boolean");
      expect(typeof typed.has_key).toBe("boolean");
      expect(
        unknownTopLevel(row as unknown as Record<string, unknown>, allowed),
      ).toEqual([]);
    }
  });

  it("checkup decodes as Checkup; no unknown top-level keys", () => {
    const raw = checkupCaptured as Checkup & Record<string, unknown>;
    const typed: Checkup = raw;
    expect(Array.isArray(typed.findings)).toBe(true);
    expect(typed.status).toBeTruthy();
    expect(typed.status.security).toBeTruthy();
    expect(typed.status.security).toHaveProperty("exposure");
    expect(typed.status.security).toHaveProperty("access");
    expect(typed.status.security).toHaveProperty("vulns");
    expect(typed.status).toHaveProperty("updates");
    expect(typed.status).toHaveProperty("audit");

    expect(unknownTopLevel(raw, ["findings", "status", "verify"])).toEqual([]);
    expect(
      unknownTopLevel(typed.status as unknown as Record<string, unknown>, [
        "generated_at",
        "schema_version",
        "version",
        "config",
        "services",
        "jobs",
        "security",
        "updates",
        "audit",
      ]),
    ).toEqual([]);
  });

  it("sessions-list decodes as SessionMeta[]; no unknown top-level keys", () => {
    const raw = sessionsCaptured as SessionMeta[];
    expect(Array.isArray(raw)).toBe(true);
    const allowed = [
      "id",
      "base",
      "host",
      "user",
      "command",
      "interactive",
      "started_at",
      "ended_at",
      "exit_code",
      "error",
      "bytes",
      "invoker",
      "cast_path",
    ] as const;
    for (const row of raw) {
      const typed: SessionMeta = row;
      expect(typed.id).toBeTruthy();
      expect(typed.base).toBeTruthy();
      expect(typeof typed.interactive).toBe("boolean");
      expect(typed.cast_path).toBeTruthy();
      expect(
        unknownTopLevel(row as unknown as Record<string, unknown>, allowed),
      ).toEqual([]);
    }
  });

  it("version decodes as CliVersion; no unknown top-level keys", () => {
    const raw = versionCaptured as CliVersion & Record<string, unknown>;
    const typed: CliVersion = raw;
    expect(typeof typed.version).toBe("string");
    expect(typeof typed.commit).toBe("string");
    expect(typeof typed.date).toBe("string");
    expect(typeof typed.string).toBe("string");
    expect(unknownTopLevel(raw, ["version", "commit", "date", "string"])).toEqual(
      [],
    );
  });

  it("hand-written fixtures are structurally congruent with capture", () => {
    const list = listCaptured as BaseSummary[];
    const checkup = checkupCaptured as Checkup;
    const sessions = sessionsCaptured as SessionMeta[];

    assertSubsetKeys("demoBase↔list", demoBase, list[0]);
    assertSubsetKeys("healthyCheckup↔checkup", healthyCheckup, checkup);
    assertSubsetKeys(
      "backupsConfiguredCheckup↔checkup",
      backupsConfiguredCheckup,
      checkup,
    );

    if (sessions.length > 0 && demoSessions.length > 0) {
      assertSubsetKeys("demoSessions↔sessions", demoSessions[0], sessions[0]);
    }
  });
});
