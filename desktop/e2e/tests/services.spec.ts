import { expect, test } from "@playwright/test";

import {
  demoBase,
  healthyCheckup,
  serviceAddPreview,
} from "../fixtures/data";
import { callMatched, getCalls, openApp } from "../shim/install";

test.describe("services", () => {
  test("add service: dry-run preview before commit", async ({ page }) => {
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
    await page.getByRole("textbox", { name: "Ref" }).fill("main");
    await page.getByRole("textbox", { name: "Port" }).fill("8080");

    await page.getByRole("button", { name: "Preview change" }).click();
    await expect(page.getByText("Commit:")).toBeVisible();
    await expect(page.getByText("service: add api")).toBeVisible();
    await expect(page.getByText(/repo: git@github.com:example\/api.git/)).toBeVisible();

    let calls = await getCalls(page);
    const previewCall = calls.find(
      (c) =>
        c.args &&
        callMatched([c], ["service", "add", "demo", "api"]) &&
        c.args.includes("--dry-run"),
    );
    expect(previewCall).toBeTruthy();

    // Dismiss confirm → no real apply.
    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and apply" }).click();
    await page.waitForTimeout(300);

    calls = await getCalls(page);
    const applies = calls.filter(
      (c) =>
        c.args &&
        callMatched([c], ["service", "add", "demo", "api"]) &&
        !c.args.includes("--dry-run"),
    );
    expect(applies).toHaveLength(0);

    // Accept confirm → real apply.
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Commit and apply" }).click();
    await expect
      .poll(async () => {
        const c = await getCalls(page);
        return c.filter(
          (x) =>
            x.args &&
            callMatched([x], ["service", "add", "demo", "api"]) &&
            !x.args.includes("--dry-run"),
        ).length;
      })
      .toBe(1);
  });

  test("secrets reveal-on-click; values hidden by default", async ({ page }) => {
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
    await expect(page.getByText("super-secret-value")).toHaveCount(0);

    await page.getByRole("button", { name: "Reveal" }).first().click();
    await expect(page.getByText("super-secret-value")).toBeVisible();

    const calls = await getCalls(page);
    expect(callMatched(calls, ["secrets", "get", "demo", "web"])).toBeTruthy();
  });
});
