import {
  backupSetupPreview,
  backupSetupPreviewNoChange,
  backupsConfiguredCheckup,
  backupsUnconfiguredCheckup,
  demoBase,
  deployFormFindingCheckup,
  deployPreview,
  deployPreviewNoChange,
  healthyCheckup,
  serviceAddPreview,
  serviceRemovePreview,
} from "../fixtures/data";
import { expect, test, waitForQuiet } from "../fixtures/test";
import { callMatched, getCalls, openApp, realMutations } from "../shim/install";

test.describe("confirm gates", () => {
  test("deploy: dismiss keeps dry-run only; accept applies once", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
      deployPreview,
    });

    await page.getByRole("tab", { name: "Updates" }).click();
    await page.getByRole("button", { name: "Update" }).first().click();
    await page.getByRole("button", { name: "Preview change" }).click();
    await expect(page.getByText("Commit:")).toBeVisible();
    await expect(page.getByText("deploy web @ abc1234")).toBeVisible();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and deploy" }).click();
    // UI stays on the preview (button still visible) after dismiss.
    await expect(page.getByRole("button", { name: "Commit and deploy" })).toBeVisible();
    expect(realMutations(await waitForQuiet(page), ["deploy", "demo", "web"])).toHaveLength(0);

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Commit and deploy" }).click();
    await expect
      .poll(async () => realMutations(await getCalls(page), ["deploy", "demo", "web"]).length)
      .toBe(1);

    const apply = realMutations(await getCalls(page), ["deploy", "demo", "web"])[0];
    expect(apply?.args).not.toContain("--dry-run");
    expect(apply?.args).toContain("--ref");
    expect(apply?.cmd).toBe("cli_run");
  });

  test("deploy already-current disables commit", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
      deployPreview: deployPreviewNoChange,
    });

    await page.getByRole("tab", { name: "Updates" }).click();
    await page.getByRole("button", { name: "Update" }).first().click();
    await page.getByRole("button", { name: "Preview change" }).click();
    await expect(page.getByRole("button", { name: "Already current" })).toBeDisabled();
  });

  test("deploy form from FindingRow uses the same gate", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: deployFormFindingCheckup },
      deployPreview,
    });

    await page.getByRole("button", { name: "Update web" }).click();
    await page.getByRole("button", { name: "Preview change" }).click();
    await expect(page.getByText("deploy web @ abc1234")).toBeVisible();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and deploy" }).click();
    await expect(page.getByRole("button", { name: "Commit and deploy" })).toBeVisible();
    expect(realMutations(await waitForQuiet(page), ["deploy", "demo", "web"])).toHaveLength(0);
  });

  test("service remove: type-to-confirm + dismiss/accept", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
      servicePreview: serviceRemovePreview,
    });

    await page.getByRole("tab", { name: "Services" }).click();
    await page.getByRole("button", { name: "Manage" }).click();
    await page.getByRole("button", { name: "Remove" }).click();

    const confirm = page.getByRole("textbox", { name: /Type web to confirm/i });
    await confirm.fill("nope");
    await page.getByRole("button", { name: "Preview removal" }).click();
    await expect(page.getByText("service: remove web")).toBeVisible();
    await expect(page.getByRole("button", { name: "Commit and remove" })).toBeDisabled();

    await confirm.fill("web");
    await expect(page.getByRole("button", { name: "Commit and remove" })).toBeEnabled();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and remove" }).click();
    await expect(page.getByRole("button", { name: "Commit and remove" })).toBeVisible();
    expect(realMutations(await waitForQuiet(page), ["service", "remove", "demo", "web"])).toHaveLength(
      0,
    );

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Commit and remove" }).click();
    await expect
      .poll(
        async () =>
          realMutations(await getCalls(page), ["service", "remove", "demo", "web"]).length,
      )
      .toBe(1);
  });

  test("service add still enforces confirm (regression)", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
      servicePreview: serviceAddPreview,
    });

    await page.getByRole("tab", { name: "Services" }).click();
    await page.getByRole("button", { name: "Add service" }).click();
    await page.getByRole("textbox", { name: "Service name" }).fill("api");
    await page
      .getByRole("textbox", { name: "Repo" })
      .fill("git@github.com:example/api.git");
    await page.getByRole("button", { name: "Preview change" }).click();
    await expect(page.getByText("service: add api")).toBeVisible();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and apply" }).click();
    await expect(page.getByRole("button", { name: "Commit and apply" })).toBeVisible();
    expect(realMutations(await waitForQuiet(page), ["service", "add", "demo", "api"])).toHaveLength(
      0,
    );
  });

  test("secrets delete requires confirm and targets the key", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await page.getByRole("tab", { name: "Services" }).click();
    await page.getByRole("button", { name: "Manage" }).click();
    await page.getByRole("button", { name: "Secrets" }).click();
    await expect(page.getByText("DATABASE_URL")).toBeVisible();
    await expect(page.getByText("API_TOKEN")).toBeVisible();

    // Direct child span so the outer service <li> (which contains the secrets
    // panel) does not match — wrong-key delete used to pass with .first().
    const dbRow = page
      .locator("li")
      .filter({ has: page.locator(":scope > span.font-mono", { hasText: "DATABASE_URL" }) });
    page.once("dialog", (d) => d.dismiss());
    await dbRow.getByRole("button", { name: "Delete" }).click();
    await expect(page.getByText("DATABASE_URL", { exact: true })).toBeVisible();
    expect(
      callMatched(await waitForQuiet(page), [
        "secrets",
        "delete",
        "demo",
        "web",
        "DATABASE_URL",
      ]),
    ).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await dbRow.getByRole("button", { name: "Delete" }).click();
    await expect
      .poll(async () =>
        callMatched(await getCalls(page), [
          "secrets",
          "delete",
          "demo",
          "web",
          "DATABASE_URL",
        ]),
      )
      .toBeTruthy();
    await expect(page.getByText("DATABASE_URL", { exact: true })).toHaveCount(0);
    await expect(page.getByText("API_TOKEN", { exact: true })).toBeVisible();
  });

  test("backup setup: dismiss keeps dry-run only; accept puts creds on stdin", async ({
    page,
  }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsUnconfiguredCheckup },
      backupSetupPreview,
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await expect(page.getByText(/No snapshot has ever been taken/i)).toBeVisible();
    await page
      .getByLabel("Restic repository URL")
      .fill("s3:s3.amazonaws.com/example/ownbase");
    await page.getByLabel("Restic password").fill("restic-setup-secret");
    await page.getByLabel("AWS access key (for s3: repos)").fill("AKIAEXAMPLE");
    await page.getByLabel("AWS secret key (for s3: repos)").fill("aws-secret-value");

    await page.getByRole("button", { name: "Preview change" }).click();
    await expect(page.getByText("backup: configure restic repo")).toBeVisible();

    page.once("dialog", async (d) => {
      // The unrecoverable-password warning lives only in this confirm.
      expect(d.message()).toMatch(/never recoverable/i);
      await d.dismiss();
    });
    await page.getByRole("button", { name: "Confirm and set up" }).click();
    await expect(page.getByRole("button", { name: "Confirm and set up" })).toBeVisible();
    expect(
      realMutations(await waitForQuiet(page), ["backup", "setup", "demo"]),
    ).toHaveLength(0);

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Confirm and set up" }).click();
    await expect
      .poll(
        async () => realMutations(await getCalls(page), ["backup", "setup", "demo"]).length,
      )
      .toBe(1);

    const apply = realMutations(await getCalls(page), ["backup", "setup", "demo"])[0];
    expect(apply?.args).toContain("--creds-stdin");
    expect(apply?.args?.join(" ")).not.toContain("restic-setup-secret");
    expect(apply?.stdin).toContain("restic-setup-secret");
  });

  test("backup setup already-configured path uses store-credentials confirm", async ({
    page,
  }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsUnconfiguredCheckup },
      backupSetupPreview: backupSetupPreviewNoChange,
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await page
      .getByLabel("Restic repository URL")
      .fill("s3:s3.amazonaws.com/example/ownbase");
    await page.getByLabel("Restic password").fill("pw");
    await page.getByRole("button", { name: "Preview change" }).click();
    await expect(
      page.getByRole("button", { name: "Store credentials and back up" }),
    ).toBeVisible();

    page.once("dialog", async (d) => {
      expect(d.message()).toMatch(/already has this backup configuration/i);
      await d.dismiss();
    });
    await page.getByRole("button", { name: "Store credentials and back up" }).click();
    expect(
      realMutations(await waitForQuiet(page), ["backup", "setup", "demo"]),
    ).toHaveLength(0);
  });

  test("secrets set posts values on stdin", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await page.getByRole("tab", { name: "Services" }).click();
    await page.getByRole("button", { name: "Manage" }).click();
    await page.getByRole("button", { name: "Secrets" }).click();

    await page.getByPlaceholder("API_KEY").fill("NEW_SECRET");
    await page.getByPlaceholder("••••••••").fill("sentinel-value");
    await page.getByRole("button", { name: "Set", exact: true }).click();

    await expect
      .poll(async () => callMatched(await getCalls(page), ["secrets", "set", "demo", "web"]))
      .toBeTruthy();

    const setCall = (await getCalls(page)).find(
      (c) => c.args && callMatched([c], ["secrets", "set", "demo", "web"]),
    );
    expect(setCall?.args?.join(" ")).not.toContain("sentinel-value");
    expect(setCall?.stdin).toContain("sentinel-value");
    expect(setCall?.args).toContain("--stdin");
  });
});
