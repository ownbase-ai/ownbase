import {
  backupsConfiguredCheckup,
  demoBase,
  deployFormFindingCheckup,
  deployPreview,
  deployPreviewNoChange,
  healthyCheckup,
  serviceAddPreview,
  serviceRemovePreview,
} from "../fixtures/data";
import { expect, test } from "../fixtures/test";
import { callMatched, getCalls, openApp, realMutations } from "../shim/install";

/** Poll until call count is stable for `stableMs`, then return calls. */
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

  test("secrets delete requires confirm", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await page.getByRole("tab", { name: "Services" }).click();
    await page.getByRole("button", { name: "Manage" }).click();
    await page.getByRole("button", { name: "Secrets" }).click();
    await expect(page.getByText("DATABASE_URL")).toBeVisible();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Delete" }).first().click();
    await expect(page.getByText("DATABASE_URL")).toBeVisible();
    expect(
      callMatched(await waitForQuiet(page), ["secrets", "delete", "demo", "web"]),
    ).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Delete" }).first().click();
    await expect
      .poll(async () =>
        callMatched(await getCalls(page), ["secrets", "delete", "demo", "web"]),
      )
      .toBeTruthy();
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
