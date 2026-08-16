import { expect, test } from "@playwright/test";

import {
  createStreamEvents,
  demoKeygen,
  preflightFailEvents,
  remoteCreateStreamEvents,
} from "../fixtures/data";
import { callMatched, getCalls, openApp } from "../shim/install";

test.describe("setup wizard", () => {
  test("path step offers all five modes", async ({ page }) => {
    await openApp(page, { vault: "unlocked", bases: [] });

    await expect(page.getByRole("heading", { name: "Set up a Base" })).toBeVisible();
    await expect(page.getByText("Set up a new server")).toBeVisible();
    await expect(page.getByText("Local VM on this computer")).toBeVisible();
    await expect(page.getByText("Add a server that's already running OwnBase")).toBeVisible();
    await expect(page.getByText("Restore from backups (local VM)")).toBeVisible();
    await expect(page.getByText("Restore from backups (remote server)")).toBeVisible();
  });

  test("local VM path: keygen → install streams → done", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [],
      keygen: { ...demoKeygen, base: "fresh" },
      streams: [{ match: ["create", "fresh"], events: createStreamEvents }],
    });

    await page.getByText("Local VM on this computer").click();
    await expect(page.getByRole("heading", { name: "Name this Base" })).toBeVisible();
    await page.getByPlaceholder("mybase").fill("fresh");
    await page.getByRole("button", { name: "Continue" }).click();

    await expect(page.getByRole("heading", { name: "Your key for this Base" })).toBeVisible();
    await page.getByRole("button", { name: "Generate a new key" }).click();
    await expect(page.getByText(/ssh-ed25519/)).toBeVisible();
    await page.getByRole("button", { name: "Create the VM" }).click();

    await expect(page.getByText("Creating the local VM")).toBeVisible();
    // Done step (config-repo prompt) after a successful stream.
    await expect(
      page.getByRole("heading", { name: "fresh is up and hardened" }),
    ).toBeVisible({ timeout: 10_000 });
    await page.getByRole("button", { name: "Open fresh" }).click();
    await expect(page.getByRole("heading", { name: "fresh" })).toBeVisible();

    const calls = await getCalls(page);
    expect(callMatched(calls, ["keygen", "fresh"])).toBeTruthy();
    expect(callMatched(calls, ["create", "fresh"])).toBeTruthy();
    const create = calls.find((c) => c.args?.[0] === "create");
    expect(create?.args).toContain("--wait");
    expect(create?.args).not.toContain("--remote");
  });

  test("remote path: keygen shows public key; install uses --remote", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [],
      keygen: { ...demoKeygen, base: "prod" },
      streams: [{ match: ["create", "prod"], events: remoteCreateStreamEvents }],
    });

    await page.getByText("Set up a new server").click();
    await page.getByPlaceholder("mybase").fill("prod");
    await page.getByRole("button", { name: "Continue" }).click();

    await page.getByRole("button", { name: "Generate a new key" }).click();
    await expect(
      page.getByText(/ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2EfixturePublicKey/),
    ).toBeVisible();
    await page.getByRole("button", { name: "Continue" }).click();

    await page.getByPlaceholder("203.0.113.10").fill("203.0.113.10");
    await page.getByRole("button", { name: "Install OwnBase" }).click();

    await expect(page.getByText("Installing OwnBase").first()).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "prod is up and hardened" }),
    ).toBeVisible({ timeout: 10_000 });
    await page.getByRole("button", { name: "Open prod" }).click();
    await expect(page.getByRole("heading", { name: "prod" })).toBeVisible();

    const calls = await getCalls(page);
    expect(callMatched(calls, ["create", "prod"])).toBeTruthy();
    const create = calls.find((c) => c.args?.[0] === "create");
    expect(create?.args).toContain("--remote");
    expect(create?.args?.some((a) => a.includes("203.0.113.10"))).toBeTruthy();
  });

  test("preflight failure (exit 3) surfaces a specific install error", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [],
      keygen: demoKeygen,
      streams: [{ match: ["create"], events: preflightFailEvents }],
    });

    await page.getByText("Local VM on this computer").click();
    await page.getByPlaceholder("mybase").fill("tiny");
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByRole("button", { name: "Generate a new key" }).click();
    await page.getByRole("button", { name: "Create the VM" }).click();

    await expect(page.getByText("Install failed")).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText("The install did not finish")).toBeVisible();
    await expect(
      page.getByText(/did not pass|checked before install/i).first(),
    ).toBeVisible();
    await expect(page.getByText(/disk too small/i)).toBeVisible();
  });
});
