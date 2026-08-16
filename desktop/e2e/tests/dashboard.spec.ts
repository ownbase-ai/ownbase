import {
  demoBase,
  findingsCheckup,
  healthyCheckup,
} from "../fixtures/data";
import { expect, test } from "../fixtures/test";
import { openApp } from "../shim/install";

test.describe("dashboard", () => {
  test("healthy Base: All clear and tabs render", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await expect(page.getByRole("heading", { name: "demo" })).toBeVisible();
    await expect(page.getByText("All clear")).toBeVisible();
    await expect(page.getByText("Nothing needs attention")).toBeVisible();
    await expect(page.getByText("This machine")).toBeVisible();
    await expect(page.getByText("192.168.64.10").first()).toBeVisible();

    await page.getByRole("tab", { name: "Services" }).click();
    await expect(page.getByRole("button", { name: "web" })).toBeVisible();
    await expect(page.getByText("web.example.com")).toBeVisible();

    await page.getByRole("tab", { name: "Security" }).click();
    await expect(page.getByText("Network exposure")).toBeVisible();
    await expect(page.getByText("Firewall", { exact: true })).toBeVisible();

    await page.getByRole("tab", { name: "Activity" }).click();
    await expect(page.getByText("reconcile.apply")).toBeVisible();
  });

  test("findings render on Overview with actions", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: findingsCheckup },
    });

    await expect(page.getByText("2 things to look at")).toBeVisible();
    await expect(page.getByText("Host packages need patching")).toBeVisible();
    await expect(page.getByText("Backup has not been verified recently")).toBeVisible();
    await expect(page.getByRole("button", { name: "Apply patches" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open backups" })).toBeVisible();
  });

  test("sidebar lists Bases and navigates", async ({ page }) => {
    const other = {
      ...demoBase,
      name: "other",
      host: "10.0.0.5",
      kind: "remote" as const,
    };
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase, other],
      checkup: {
        demo: healthyCheckup,
        other: healthyCheckup,
      },
    });

    await expect(page.getByRole("navigation").getByText("demo")).toBeVisible();
    await expect(page.getByRole("navigation").getByText("other")).toBeVisible();

    await page.getByRole("navigation").getByText("other").click();
    await expect(page.getByRole("heading", { name: "other" })).toBeVisible();
    await expect(page.getByText("10.0.0.5").first()).toBeVisible();
  });

  test("unreachable Base shows error and retry recovers", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
      fails: [
        {
          match: ["checkup", "demo"],
          code: 1,
          stderr: "Error: dial tcp: connection refused",
        },
      ],
    });

    await expect(page.getByText("demo did not answer")).toBeVisible();
    await expect(page.getByText("connection refused")).toBeVisible();

    await page.evaluate(() => window.__E2E__?.clearFails());
    await page.getByRole("button", { name: "Try again" }).click();

    await expect(page.getByText("All clear")).toBeVisible();
    await expect(page.getByText("Nothing needs attention")).toBeVisible();
  });
});
