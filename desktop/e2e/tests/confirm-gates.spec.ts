import { expect, test } from "@playwright/test";

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
import { callMatched, getCalls, openApp } from "../shim/install";

/** Non-dry-run service/deploy/backup/config mutations. */
function realMutations(calls: Awaited<ReturnType<typeof getCalls>>, match: string[]) {
  return calls.filter(
    (c) =>
      c.args &&
      callMatched([c], match) &&
      !c.args.includes("--dry-run"),
  );
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

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and deploy" }).click();
    await page.waitForTimeout(200);
    expect(realMutations(await getCalls(page), ["deploy", "demo", "web"])).toHaveLength(0);

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Commit and deploy" }).click();
    await expect
      .poll(async () => realMutations(await getCalls(page), ["deploy", "demo", "web"]).length)
      .toBe(1);

    const apply = realMutations(await getCalls(page), ["deploy", "demo", "web"])[0];
    expect(apply?.args).not.toContain("--dry-run");
    expect(apply?.args).toContain("--ref");
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
    await page.waitForTimeout(200);
    expect(realMutations(await getCalls(page), ["deploy"])).toHaveLength(0);
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
    // Wrong name keeps commit disabled.
    await confirm.fill("nope");
    await page.getByRole("button", { name: "Preview removal" }).click();
    await expect(page.getByText("service: remove web")).toBeVisible();
    await expect(page.getByRole("button", { name: "Commit and remove" })).toBeDisabled();

    await confirm.fill("web");
    await expect(page.getByRole("button", { name: "Commit and remove" })).toBeEnabled();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and remove" }).click();
    await page.waitForTimeout(200);
    expect(realMutations(await getCalls(page), ["service", "remove"])).toHaveLength(0);

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Commit and remove" }).click();
    await expect
      .poll(async () => realMutations(await getCalls(page), ["service", "remove"]).length)
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

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and apply" }).click();
    await page.waitForTimeout(200);
    expect(realMutations(await getCalls(page), ["service", "add"])).toHaveLength(0);
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
    await page.waitForTimeout(200);
    expect(callMatched(await getCalls(page), ["secrets", "delete"])).toBeFalsy();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Delete" }).first().click();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["secrets", "delete"]))
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
    await page.getByPlaceholder("••••••••").fill("sentinal-value");
    await page.getByRole("button", { name: "Set", exact: true }).click();

    await expect
      .poll(async () => {
        const calls = await getCalls(page);
        return calls.find((c) => c.args && callMatched([c], ["secrets", "set"]));
      })
      .toBeTruthy();

    const setCall = (await getCalls(page)).find(
      (c) => c.args && callMatched([c], ["secrets", "set"]),
    );
    expect(setCall?.args?.join(" ")).not.toContain("sentinal-value");
    expect(setCall?.stdin).toContain("sentinal-value");
  });
});
