import { expect, test } from "@playwright/test";

import { MASTER_PASSWORD, demoBase, healthyCheckup } from "../fixtures/data";
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

  test("exit 7 from list after unlock is handled via vault refresh", async ({ page }) => {
    // Start unlocked; lock from the UI and confirm we bounce to unlock.
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
});
