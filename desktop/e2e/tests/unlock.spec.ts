import { expect, test } from "@playwright/test";

import {
  MASTER_PASSWORD,
  VAULT_PATH,
  demoBase,
} from "../fixtures/data";
import { getCalls, openApp } from "../shim/install";

test.describe("unlock gate", () => {
  test("first launch: create a vault", async ({ page }) => {
    await openApp(page, {
      vault: "absent",
      password: MASTER_PASSWORD,
      dialogPath: "/tmp/ownbase-e2e",
    });

    await expect(page.getByRole("heading", { name: "Create your vault" })).toBeVisible();

    await page.getByPlaceholder("~/Dropbox/OwnBase").fill("/tmp/ownbase-e2e");
    await page.locator('input[type="password"]').nth(0).fill(MASTER_PASSWORD);
    await page.locator('input[type="password"]').nth(1).fill(MASTER_PASSWORD);
    await page.getByRole("button", { name: "Create vault" }).click();

    // Empty vault → wizard landing.
    await expect(page.getByRole("heading", { name: "Set up a Base" })).toBeVisible();
    await expect(page.getByText("None yet.")).toBeVisible();

    const calls = await getCalls(page);
    const init = calls.find((c) => c.args && c.args[0] === "vault" && c.args[1] === "init");
    expect(init?.stdin).toBe(MASTER_PASSWORD);
    expect(init?.args).toContain("--password-stdin");
    expect(init?.args?.some((a) => a.includes("ownbase-e2e"))).toBeTruthy();
  });

  test("locked vault: unlock with master password", async ({ page }) => {
    await openApp(page, {
      vault: "locked",
      password: MASTER_PASSWORD,
      vaultPath: VAULT_PATH,
      bases: [demoBase],
    });

    await expect(page.getByRole("heading", { name: "Unlock your vault" })).toBeVisible();
    await expect(page.getByText(VAULT_PATH)).toBeVisible();

    await page.getByPlaceholder("••••••••••••").fill(MASTER_PASSWORD);
    await page.getByRole("button", { name: "Unlock" }).click();

    await expect(page.getByRole("heading", { name: "demo" })).toBeVisible();
    await expect(page.getByText("ubuntu@192.168.64.10").first()).toBeVisible();
  });

  test("wrong password shows an error and stays locked", async ({ page }) => {
    await openApp(page, {
      vault: "locked",
      password: MASTER_PASSWORD,
      vaultPath: VAULT_PATH,
    });

    await page.getByPlaceholder("••••••••••••").fill("not-the-password");
    await page.getByRole("button", { name: "Unlock" }).click();

    await expect(page.getByText("Could not unlock")).toBeVisible();
    await expect(page.getByRole("heading", { name: "Unlock your vault" })).toBeVisible();
  });

  test("password mismatch blocks create", async ({ page }) => {
    await openApp(page, { vault: "absent", password: MASTER_PASSWORD });

    await page.getByPlaceholder("~/Dropbox/OwnBase").fill("/tmp/ownbase-e2e");
    await page.locator('input[type="password"]').nth(0).fill("aaaa");
    await page.locator('input[type="password"]').nth(1).fill("bbbb");

    await expect(page.getByText("The two passwords do not match.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Create vault" })).toBeDisabled();
  });

  test("choose folder uses the dialog plugin", async ({ page }) => {
    await openApp(page, {
      vault: "absent",
      dialogPath: "/tmp/from-dialog",
    });

    await page.getByRole("button", { name: "Choose…" }).click();
    await expect(page.getByPlaceholder("~/Dropbox/OwnBase")).toHaveValue("/tmp/from-dialog");

    const calls = await getCalls(page);
    expect(calls.some((c) => c.cmd === "plugin:dialog|open")).toBeTruthy();
  });
});
