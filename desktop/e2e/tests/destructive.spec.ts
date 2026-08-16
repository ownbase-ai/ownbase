import {
  backupsConfiguredCheckup,
  demoBase,
  dbRestoreOutcome,
  dbStatusFixture,
  recoveryKitFixture,
} from "../fixtures/data";
import { expect, test } from "../fixtures/test";
import { callMatched, getCalls, openApp, realMutations } from "../shim/install";

async function waitForQuiet(
  page: import("@playwright/test").Page,
  stableMs = 300,
): Promise<Awaited<ReturnType<typeof getCalls>>> {
  let last = -1;
  let stableSince = Date.now();
  for (;;) {
    const calls = await getCalls(page);
    if (calls.length === last) {
      if (Date.now() - stableSince >= stableMs) return calls;
    } else {
      last = calls.length;
      stableSince = Date.now();
    }
    await page.waitForTimeout(50);
  }
}

test.describe("destructive flows", () => {
  test("RemoveBase: type-to-confirm; keep-vm when destroy unchecked", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
    });

    await page.getByRole("button", { name: "Remove from this computer…" }).click();
    await expect(page.getByRole("button", { name: "Remove from this computer" })).toBeDisabled();

    await page.getByPlaceholder("demo").fill("wrong");
    await expect(page.getByRole("button", { name: "Remove from this computer" })).toBeDisabled();

    const destroy = page.getByLabel(/Also destroy the local Multipass VM/i);
    // Local VM with profile defaults destroyVM to false (not unregistered-vm).
    await expect(destroy).not.toBeChecked();

    await page.getByPlaceholder("demo").fill("demo");
    await page.getByRole("button", { name: "Remove from this computer" }).click();

    await expect
      .poll(async () => callMatched(await getCalls(page), ["delete", "demo"]))
      .toBeTruthy();
    const del = (await getCalls(page)).find((c) => callMatched([c], ["delete", "demo"]));
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
    const del = (await getCalls(page)).find((c) => callMatched([c], ["delete", "demo"]));
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
    await expect(page.getByRole("button", { name: "Prune snapshots" })).toBeEnabled();
    expect(callMatched(await waitForQuiet(page), ["backup", "prune", "demo"])).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Prune snapshots" }).click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["backup", "prune", "demo"]))
      .toBeTruthy();
    await expect(page.getByText("pruned 2 snapshots")).toBeVisible();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Rekey (generate password)" }).click();
    await expect(page.getByText("e2e-generated-restic-pw")).toBeVisible();
    const rekey = (await getCalls(page)).find((c) =>
      callMatched([c], ["backup", "rekey", "demo"]),
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
    expect(callMatched(await getCalls(page), ["backup", "recovery-kit", "demo"])).toBeTruthy();
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
    await expect(page.getByText("Earliest")).toBeVisible();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Restore to scratch" }).click();
    await expect(page.getByRole("button", { name: "Restore to scratch" })).toBeEnabled();
    expect(callMatched(await waitForQuiet(page), ["db", "restore", "demo"])).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Restore to scratch" }).click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["db", "restore", "demo"]))
      .toBeTruthy();
    await expect(page.getByText("Scratch restore ready at localhost:5433")).toBeVisible();

    const restore = (await getCalls(page)).find((c) =>
      callMatched([c], ["db", "restore", "demo"]),
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

    const restoreBtn = page.getByRole("button", { name: "Restore over production" });
    await expect(restoreBtn).toBeDisabled();

    await page
      .getByRole("textbox", { name: /Type demo to confirm production restore/i })
      .fill("demo");
    await expect(restoreBtn).toBeEnabled();

    page.once("dialog", (d) => d.dismiss());
    await restoreBtn.click();
    await expect(restoreBtn).toBeEnabled();
    expect(callMatched(await waitForQuiet(page), ["db", "restore", "demo"])).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await restoreBtn.click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["db", "restore", "demo"]))
      .toBeTruthy();
    const restore = (await getCalls(page)).find((c) =>
      callMatched([c], ["db", "restore", "demo"]),
    );
    expect(restore?.args).toContain("--into");
    expect(restore?.args).toContain("production");
    expect(restore?.args).toContain("--yes");
  });

  test("backup now from Backups tab shows output", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await page.getByRole("button", { name: "Back up now" }).click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["backup", "run", "demo"]))
      .toBeTruthy();
    await expect(page.getByText("snapshot saved")).toBeVisible();
    expect(realMutations(await getCalls(page), ["backup", "run", "demo"])).toHaveLength(1);
  });
});
