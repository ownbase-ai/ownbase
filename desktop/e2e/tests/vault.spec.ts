import {
  MASTER_PASSWORD,
  VAULT_PATH,
  demoBase,
  healthyCheckup,
} from "../fixtures/data";
import { expect, test } from "../fixtures/test";
import { callMatched, getCalls, openApp } from "../shim/install";

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
    await expect(page.getByText("unlocked", { exact: true })).toBeVisible();
    await expect(page.getByText(/pid 4242/)).toBeVisible();
    await expect(page.getByRole("navigation").getByText("demo")).toBeVisible();
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

    expect(callMatched(await getCalls(page), ["vault", "lock"])).toBeTruthy();
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
    await page.getByRole("button", { name: "Change", exact: true }).click();
    const passwords = page.locator('input[type="password"]');
    await passwords.nth(0).fill("new-password-long");
    await passwords.nth(1).fill("new-password-long");
    await page.getByRole("button", { name: "Change password" }).click();

    await expect
      .poll(async () => {
        const calls = await getCalls(page);
        return calls.find((c) => callMatched([c], ["vault", "passwd"]));
      })
      .toMatchObject({ stdin: "new-password-long" });
  });
});
