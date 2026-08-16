import {
  demoBase,
  healthyCheckup,
  serviceAddPreview,
} from "../fixtures/data";
import { expect, test, waitForQuiet } from "../fixtures/test";
import { callMatched, getCalls, openApp, realMutations } from "../shim/install";

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

    const previewCall = (await getCalls(page)).find(
      (c) =>
        c.args &&
        callMatched([c], ["service", "add", "demo", "api"]) &&
        c.args.includes("--dry-run"),
    );
    expect(previewCall).toBeTruthy();

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Commit and apply" }).click();
    await expect(page.getByRole("button", { name: "Commit and apply" })).toBeVisible();
    expect(
      realMutations(await waitForQuiet(page), ["service", "add", "demo", "api"]),
    ).toHaveLength(0);

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Commit and apply" }).click();
    await expect
      .poll(
        async () =>
          realMutations(await getCalls(page), ["service", "add", "demo", "api"]).length,
      )
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
    await expect(page.getByText("secret-value-for-DATABASE_URL")).toHaveCount(0);
    await expect(page.getByText("secret-value-for-API_TOKEN")).toHaveCount(0);

    const dbRow = page
      .locator("li")
      .filter({ has: page.locator(":scope > span.font-mono", { hasText: "DATABASE_URL" }) });
    await dbRow.getByRole("button", { name: "Reveal" }).click();
    await expect(page.getByText("secret-value-for-DATABASE_URL")).toBeVisible();
    await expect(page.getByText("secret-value-for-API_TOKEN")).toHaveCount(0);

    expect(
      callMatched(await getCalls(page), [
        "secrets",
        "get",
        "demo",
        "web",
        "DATABASE_URL",
      ]),
    ).toBeTruthy();
  });
});
