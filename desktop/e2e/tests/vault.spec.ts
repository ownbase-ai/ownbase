import { expect, test } from "@playwright/test";

import {
  MASTER_PASSWORD,
  VAULT_PATH,
  demoBase,
  healthyCheckup,
} from "../fixtures/data";
import { getCalls, openApp } from "../shim/install";

test.describe("vault view", () => {
  test("shows path, lock state, and agent", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      vaultPath: VAULT_PATH,
      password: MASTER_PASSWORD,
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await page.getByRole("navigation").getByText("Vault", { exact: true }).click();
    await expect(page.getByRole("heading", { name: "Vault" })).toBeVisible();
    await expect(page.getByText(VAULT_PATH)).toBeVisible();
    await expect(page.getByText("unlocked").first()).toBeVisible();
    await expect(page.getByText(/pid 4242/)).toBeVisible();
    await expect(page.getByText("demo").first()).toBeVisible();
  });

  test("Lock now returns to the unlock screen", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      vaultPath: VAULT_PATH,
      password: MASTER_PASSWORD,
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await page.getByRole("navigation").getByText("Vault", { exact: true }).click();
    await page.getByRole("button", { name: "Lock now" }).click();
    await expect(page.getByRole("heading", { name: "Unlock your vault" })).toBeVisible();

    const calls = await getCalls(page);
    expect(calls.some((c) => c.args?.[0] === "vault" && c.args?.[1] === "lock")).toBeTruthy();
  });

  test("change password sends new password on stdin", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      vaultPath: VAULT_PATH,
      password: MASTER_PASSWORD,
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await page.getByRole("navigation").getByText("Vault", { exact: true }).click();

    // Panel starts collapsed behind a "Change" button.
    await page.getByRole("button", { name: "Change", exact: true }).click();
    const passwords = page.locator('input[type="password"]');
    await passwords.nth(0).fill("new-password-long");
    await passwords.nth(1).fill("new-password-long");
    await page.getByRole("button", { name: "Change password" }).click();

    await expect
      .poll(async () => {
        const calls = await getCalls(page);
        return calls.find((c) => c.args?.[0] === "vault" && c.args?.[1] === "passwd");
      })
      .toMatchObject({ stdin: "new-password-long" });
  });
});
