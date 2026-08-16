// Guard against hand-written e2e fixtures drifting from the TypeScript types
// the app actually consumes. This does not replace a live CLI capture, but it
// catches missing required fields when types change.

import { describe, expect, it } from "vitest";

import type {
  BaseSummary,
  Checkup,
  ConfigPreview,
  KeygenResult,
  SessionMeta,
} from "./types";

// Import fixtures from the e2e tree — path is stable relative to src/.
import {
  backupSetupPreview,
  backupsConfiguredCheckup,
  backupsUnconfiguredCheckup,
  demoBase,
  demoKeygen,
  demoSessions,
  deployPreview,
  findingsCheckup,
  healthyCheckup,
  serviceAddPreview,
  serviceRemovePreview,
} from "../../e2e/fixtures/data";

function assertCheckup(c: Checkup): void {
  expect(c).toHaveProperty("findings");
  expect(Array.isArray(c.findings)).toBe(true);
  expect(c.status).toHaveProperty("security");
  expect(c.status.security).toHaveProperty("exposure");
  expect(c.status.security).toHaveProperty("access");
  expect(c.status.security).toHaveProperty("vulns");
  expect(c.status).toHaveProperty("updates");
  expect(c.status).toHaveProperty("audit");
}

describe("e2e fixture shapes", () => {
  it("demoBase satisfies BaseSummary", () => {
    const b: BaseSummary = demoBase;
    expect(b.name).toBeTruthy();
    expect(["remote", "vm", "key-only", "unregistered-vm"]).toContain(b.kind);
    expect(typeof b.registered).toBe("boolean");
    expect(typeof b.has_token).toBe("boolean");
    expect(typeof b.has_key).toBe("boolean");
  });

  it("checkup fixtures satisfy Checkup", () => {
    for (const c of [
      healthyCheckup,
      findingsCheckup,
      backupsConfiguredCheckup,
      backupsUnconfiguredCheckup,
    ]) {
      assertCheckup(c);
    }
    expect(findingsCheckup.findings.length).toBeGreaterThan(0);
    expect(healthyCheckup.findings).toHaveLength(0);
    expect(backupsConfiguredCheckup.status.security.last_backup).toBeTruthy();
    expect(backupsUnconfiguredCheckup.status.security.last_backup).toBeFalsy();
    expect(backupsUnconfiguredCheckup.status.config?.repo_url).toBeTruthy();
  });

  it("backup setup preview fixtures satisfy ConfigPreview", () => {
    expect(typeof backupSetupPreview.would_change).toBe("boolean");
    expect(backupSetupPreview.would_change).toBe(true);
  });

  it("preview fixtures satisfy ConfigPreview", () => {
    for (const p of [serviceAddPreview, serviceRemovePreview, deployPreview]) {
      const preview: ConfigPreview = p;
      expect(typeof preview.would_change).toBe("boolean");
      expect(typeof preview.commit_message).toBe("string");
      expect(typeof preview.diff).toBe("string");
    }
  });

  it("keygen and sessions fixtures", () => {
    const k: KeygenResult = demoKeygen;
    expect(k.public_key.startsWith("ssh-")).toBe(true);
    expect(demoSessions.length).toBeGreaterThan(0);
    for (const s of demoSessions) {
      const m: SessionMeta = s;
      expect(m.id).toBeTruthy();
      expect(m.base).toBeTruthy();
      expect(typeof m.interactive).toBe("boolean");
      expect(m.cast_path).toBeTruthy();
    }
  });
});
