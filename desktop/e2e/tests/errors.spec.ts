import { MASTER_PASSWORD, demoBase, healthyCheckup } from "../fixtures/data";
import { expect, test } from "../fixtures/test";
import { openApp } from "../shim/install";

test.describe("error surfaces", () => {
  test("broken CLI shows install problem, not unlock", async ({ page }) => {
    await openApp(page, { vault: "broken" });

    await expect(
      page.getByText("OwnBase could not run its command-line tool"),
    ).toBeVisible();
    await expect(page.getByText(/e2e: CLI sidecar is broken/)).toBeVisible();
    await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  });

  test("sidebar Lock returns to the unlock screen", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      password: MASTER_PASSWORD,
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await expect(page.getByRole("heading", { name: "demo" })).toBeVisible();
    await page.getByRole("button", { name: "Lock" }).click();
    await expect(page.getByRole("heading", { name: "Unlock your vault" })).toBeVisible();
  });

  test("exit 7 from list surfaces the locked vault error", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      password: MASTER_PASSWORD,
      bases: [demoBase],
      fails: [{ match: ["list"], code: 7, stderr: "Error: the vault is locked" }],
    });

    await expect(page.getByText("Could not list your Bases")).toBeVisible();
    await expect(page.getByText("the vault is locked")).toBeVisible();
    await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  });
});
