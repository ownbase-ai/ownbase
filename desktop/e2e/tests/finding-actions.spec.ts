import { expect, test } from "@playwright/test";

import { demoBase, findingsForRun, healthyCheckup } from "../fixtures/data";
import { callMatched, getCalls, openApp } from "../shim/install";

const RUN_ACTIONS: Array<{ run: string; label: string; match: string[]; confirm?: string }> = [
  { run: "security fix", label: "Apply patches", match: ["security", "fix", "demo"] },
  {
    run: "security fix --reboot",
    label: "Patch and reboot",
    match: ["security", "fix", "demo"],
    confirm: "Install updates and reboot?",
  },
  { run: "security scan", label: "Rescan CVEs", match: ["security", "scan", "demo"] },
  {
    run: "security reboot",
    label: "Reboot",
    match: ["security", "reboot", "demo"],
    confirm: "Reboot the Base now?",
  },
  {
    run: "security reboot --wait",
    label: "Reboot and wait",
    match: ["security", "reboot", "demo"],
    confirm: "Reboot and wait?",
  },
  {
    run: "security install-scanner",
    label: "Install scanner",
    match: ["security", "install-scanner", "demo"],
  },
  {
    run: "self-update",
    label: "Self-update",
    match: ["self-update", "demo"],
    confirm: "Replace the daemon?",
  },
  {
    run: "upgrade --apply",
    label: "Apply upgrade",
    match: ["upgrade", "demo"],
    confirm: "Apply core upgrade?",
  },
];

test.describe("FindingRow action dispatch", () => {
  for (const action of RUN_ACTIONS) {
    test(`run "${action.run}" dispatches the right CLI`, async ({ page }) => {
      await openApp(page, {
        vault: "unlocked",
        bases: [demoBase],
        checkup: {
          demo: findingsForRun(action.run, action.label, action.confirm),
        },
      });

      await expect(page.getByText(`Finding for ${action.run}`)).toBeVisible();
      const btn = page.getByRole("button", { name: action.label });
      await expect(btn).toBeVisible();

      if (action.confirm) {
        page.once("dialog", (d) => d.accept());
      }
      await btn.click();

      await expect
        .poll(async () => callMatched(await getCalls(page), action.match))
        .toBeTruthy();

      const call = (await getCalls(page)).find(
        (c) => c.args && callMatched([c], action.match),
      );
      if (action.run.includes("--reboot") && action.run.startsWith("security fix")) {
        expect(call?.args).toContain("--reboot");
      }
      if (action.run === "security reboot --wait") {
        expect(call?.args).toContain("--wait");
      }
      if (action.run === "upgrade --apply") {
        expect(call?.args).toContain("--apply");
      }
    });
  }

  test("confirm dismiss blocks a confirmed run action", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: {
        demo: findingsForRun("self-update", "Self-update", "Replace the daemon?"),
      },
    });

    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Self-update" }).click();
    await page.waitForTimeout(200);
    expect(callMatched(await getCalls(page), ["self-update"])).toBeFalsy();
  });

  test("open action switches tab without a mutating CLI call", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: {
        demo: {
          ...healthyCheckup,
          findings: [
            {
              summary: "Look at backups",
              fix: "ownbasectl checkup demo --verify",
              action: { kind: "open", tab: "backups", label: "Open backups" },
            },
          ],
        },
      },
    });

    const before = await getCalls(page);
    await page.getByRole("button", { name: "Open backups" }).click();
    // Backups tab is selected (healthy fixture has last_backup → lifecycle view).
    await expect(page.getByRole("tab", { name: "Backups" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await expect(page.getByText(/Last snapshot|No snapshot has ever been taken/i).first()).toBeVisible();
    const after = await getCalls(page);
    const extra = after.slice(before.length);
    // kind=open must not stream or mutate.
    expect(extra.filter((c) => c.cmd === "cli_stream")).toHaveLength(0);
    expect(
      extra.every(
        (c) =>
          !c.args ||
          ["checkup", "list", "vault", "db", "backup"].includes(c.args[0] ?? ""),
      ),
    ).toBeTruthy();
  });

  test("unknown run action shows an error, no stream", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: {
        demo: findingsForRun("not-a-real-action", "Do the thing"),
      },
    });

    await page.getByRole("button", { name: "Do the thing" }).click();
    await expect(page.getByText(/Unknown action: not-a-real-action/)).toBeVisible();
    const streams = (await getCalls(page)).filter((c) => c.cmd === "cli_stream");
    expect(streams).toHaveLength(0);
  });
});
