import { expect, test } from "@playwright/test";

import {
  backupsConfiguredCheckup,
  demoBase,
  dbRestoreOutcome,
  dbStatusFixture,
  recoveryKitFixture,
} from "../fixtures/data";
import { callMatched, getCalls, openApp } from "../shim/install";

test.describe("destructive flows", () => {
  test("RemoveBase: type-to-confirm; keep-vm when destroy unchecked", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
    });

    // Overview has RemoveBase at the bottom.
    await page.getByRole("button", { name: "Remove from this computer…" }).click();
    await expect(page.getByRole("button", { name: "Remove from this computer" })).toBeDisabled();

    // Wrong name stays disabled.
    await page.getByPlaceholder("demo").fill("wrong");
    await expect(page.getByRole("button", { name: "Remove from this computer" })).toBeDisabled();

    // Leave "Also destroy the local Multipass VM" unchecked → --keep-vm.
    const destroy = page.getByLabel(/Also destroy the local Multipass VM/i);
    if (await destroy.isChecked()) await destroy.uncheck();

    await page.getByPlaceholder("demo").fill("demo");
    await page.getByRole("button", { name: "Remove from this computer" }).click();

    await expect
      .poll(async () => callMatched(await getCalls(page), ["delete", "demo"]))
      .toBeTruthy();
    const del = (await getCalls(page)).find(
      (c) => c.args && callMatched([c], ["delete", "demo"]),
    );
    expect(del?.args).toContain("--yes");
    expect(del?.args).toContain("--keep-vm");
  });

  test("RemoveBase: destroy VM omits --keep-vm", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
    });

    await page.getByRole("button", { name: "Remove from this computer…" }).click();
    await page.getByLabel(/Also destroy the local Multipass VM/i).check();
    await page.getByPlaceholder("demo").fill("demo");
    await page.getByRole("button", { name: "Remove and destroy VM" }).click();

    await expect
      .poll(async () => callMatched(await getCalls(page), ["delete", "demo"]))
      .toBeTruthy();
    const del = (await getCalls(page)).find(
      (c) => c.args && callMatched([c], ["delete", "demo"]),
    );
    expect(del?.args).toContain("--yes");
    expect(del?.args).not.toContain("--keep-vm");
  });

  test("backup prune and rekey require confirm; rekey surfaces password", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
      recoveryKit: recoveryKitFixture,
      rekeyStderr:
        "Generated restic password (save this):\n  e2e-generated-restic-pw\nrekey done\n",
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await expect(page.getByText("Backup lifecycle")).toBeVisible();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Prune snapshots" }).click();
    await page.waitForTimeout(200);
    expect(callMatched(await getCalls(page), ["backup", "prune"])).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Prune snapshots" }).click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["backup", "prune"]))
      .toBeTruthy();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Rekey (generate password)" }).click();
    await expect(page.getByText("e2e-generated-restic-pw")).toBeVisible();
    expect(callMatched(await getCalls(page), ["backup", "rekey"])).toBeTruthy();
    const rekey = (await getCalls(page)).find(
      (c) => c.args && callMatched([c], ["backup", "rekey"]),
    );
    expect(rekey?.args).toContain("--generate");
  });

  test("recovery kit reveal", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
      recoveryKit: recoveryKitFixture,
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await page.getByRole("button", { name: "Show recovery kit" }).click();
    await expect(page.getByText("s3:bucket/path")).toBeVisible();
    await expect(page.getByText("restic-recovery-password")).toBeVisible();
    expect(callMatched(await getCalls(page), ["backup", "recovery-kit"])).toBeTruthy();
  });

  test("db restore scratch: confirm then call", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
      dbStatus: dbStatusFixture,
      dbRestore: dbRestoreOutcome,
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await expect(page.getByText("Postgres recovery")).toBeVisible();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: /Restore to scratch/i }).click();
    await page.waitForTimeout(200);
    expect(callMatched(await getCalls(page), ["db", "restore"])).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: /Restore to scratch/i }).click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["db", "restore"]))
      .toBeTruthy();
    await expect(page.getByText(/Scratch restore ready|localhost:5433/i)).toBeVisible();

    const restore = (await getCalls(page)).find(
      (c) => c.args && callMatched([c], ["db", "restore"]),
    );
    expect(restore?.args).toContain("--yes");
  });

  test("db restore production: type-to-confirm + confirm", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
      dbStatus: dbStatusFixture,
      dbRestore: { into: "production", databases: 1 },
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await page.getByText("Production (replaces live DB)").click();

    const restoreBtn = page.getByRole("button", { name: /Restore over production/i });
    await expect(restoreBtn).toBeDisabled();

    await page
      .getByRole("textbox", { name: /Type demo to confirm production restore/i })
      .fill("demo");
    await expect(restoreBtn).toBeEnabled();

    page.once("dialog", (d) => d.dismiss());
    await restoreBtn.click();
    await page.waitForTimeout(200);
    expect(callMatched(await getCalls(page), ["db", "restore"])).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await restoreBtn.click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["db", "restore"]))
      .toBeTruthy();
    const restore = (await getCalls(page)).find(
      (c) => c.args && callMatched([c], ["db", "restore"]),
    );
    expect(restore?.args).toContain("--into");
    expect(restore?.args).toContain("production");
  });

  test("backup now from Backups tab", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await page.getByRole("button", { name: /Back up now/i }).click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["backup", "run", "demo"]))
      .toBeTruthy();
  });
});
