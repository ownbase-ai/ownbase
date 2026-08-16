import {
  MASTER_PASSWORD,
  VAULT_PATH,
  backupsConfiguredCheckup,
  demoBase,
  demoSessions,
  dbStatusFixture,
  healthyCheckup,
  recoveryKitFixture,
  sampleCast,
  sampleTranscript,
  upgradeCheckFixture,
} from "../fixtures/data";
import { expect, test } from "../fixtures/test";
import { callMatched, getCalls, openApp } from "../shim/install";

test.describe("read-only panels", () => {
  test("Backups tab shows lifecycle when configured", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
      dbStatus: dbStatusFixture,
      recoveryKit: recoveryKitFixture,
    });

    await page.getByRole("tab", { name: "Backups" }).click();
    await expect(page.getByText("Last snapshot")).toBeVisible();
    await expect(page.getByText("Provably restorable")).toBeVisible();
    await expect(page.getByText("Backup lifecycle")).toBeVisible();
    await expect(page.getByText("Postgres recovery")).toBeVisible();
    await expect(page.getByText("Earliest")).toBeVisible();
  });

  test("Updates tab shows drift and core upgrade check", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: backupsConfiguredCheckup },
      upgradeCheck: upgradeCheckFixture,
    });

    await page.getByRole("tab", { name: "Updates" }).click();
    await expect(page.getByText("Service updates")).toBeVisible();
    await expect(page.getByText("3 commits behind")).toBeVisible();

    await page.getByRole("button", { name: "Check" }).click();
    await expect
      .poll(async () =>
        callMatched(await getCalls(page), ["upgrade", "demo"], {
          cmd: "cli_run",
        }),
      )
      .toBeTruthy();
    const check = (await getCalls(page)).find(
      (c) =>
        c.cmd === "cli_run" &&
        c.args &&
        callMatched([c], ["upgrade", "demo"]) &&
        !c.args.includes("--apply"),
    );
    expect(check).toBeTruthy();
    await expect(page.getByText("caddy", { exact: true })).toBeVisible();
  });

  test("ownbase.yaml panel loads raw config without --json", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
      configYaml: "services:\n  web:\n    ref: main\n",
    });

    await page.getByRole("tab", { name: "Services" }).click();
    await page.getByRole("button", { name: "Show" }).click();
    await expect(page.getByText("ref: main")).toBeVisible();
    const call = (await getCalls(page)).find((c) =>
      callMatched([c], ["config", "get", "demo"]),
    );
    expect(call?.args).toBeTruthy();
    expect(call?.args).not.toContain("--json");
  });

  test("Security tab reboot and rescan actions", async ({ page }) => {
    const withReboot = {
      ...healthyCheckup,
      status: {
        ...healthyCheckup.status,
        security: {
          ...healthyCheckup.status.security,
          reboot_required: true,
          reboot_packages: ["linux-image-6.8.0"],
        },
      },
    };
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: withReboot },
    });

    await page.getByRole("tab", { name: "Security" }).click();
    await expect(page.getByText("Reboot required")).toBeVisible();

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Reboot now" }).click();
    await expect
      .poll(async () =>
        callMatched(await getCalls(page), ["security", "reboot", "demo"], {
          cmd: "cli_stream",
          includeFlags: ["--wait"],
        }),
      )
      .toBeTruthy();

    await page.getByRole("button", { name: "Rescan" }).click();
    await expect
      .poll(async () =>
        callMatched(await getCalls(page), ["security", "scan", "demo"], {
          cmd: "cli_run",
        }),
      )
      .toBeTruthy();
  });

  test("Vault: recovery string and version", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      vaultPath: VAULT_PATH,
      password: MASTER_PASSWORD,
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
    });

    await page.getByRole("navigation").getByText("Vault", { exact: true }).click();

    await page.getByRole("button", { name: "Reveal" }).click();
    await expect(page.getByText("ownbase-recovery-v1:e2e-fixture")).toBeVisible();
    expect(callMatched(await getCalls(page), ["vault", "recovery-string"])).toBeTruthy();

    await expect(page.getByText("About & updates")).toBeVisible();
    await expect(page.getByText("ownbasectl", { exact: true })).toBeVisible();
    // Shell loads version --check on unlock; Vault reuses that snapshot.
    expect(
      callMatched(await getCalls(page), ["version"], { includeFlags: ["--check"] }),
    ).toBeTruthy();
  });

  test("Sessions replay path loads cast into the player", async ({ page }) => {
    const id = demoSessions[0]!.id;
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
      sessions: demoSessions,
      sessionCast: { [id]: sampleCast },
      sessionTranscript: { [id]: sampleTranscript },
    });

    await page.getByRole("navigation").getByText("Sessions").click();
    await page.getByText("interactive shell").click();
    await page.getByRole("button", { name: "Replay" }).click();

    await expect
      .poll(async () =>
        (await getCalls(page)).some(
          (c) =>
            c.args &&
            callMatched([c], ["sessions", "show", id]) &&
            c.args.includes("--cast"),
        ),
      )
      .toBeTruthy();
    // Real asciinema-player root is div.ap-wrapper (not .asciinema-player).
    // Glyphs are canvas-drawn, so assert the terminal canvas mounted with a
    // real size — empty/broken cast data leaves width 0.
    const wrapper = page.locator("div.ap-wrapper").first();
    await expect(wrapper).toBeVisible({ timeout: 10_000 });
    const canvas = wrapper.locator("canvas").first();
    await expect(canvas).toBeVisible();
    await expect
      .poll(async () => canvas.evaluate((el: HTMLCanvasElement) => el.width))
      .toBeGreaterThan(0);
  });
});
