import { demoBase, findingsForRun, healthyCheckup } from "../fixtures/data";
import { expect, test } from "../fixtures/test";
import { callMatched, getCalls, openApp } from "../shim/install";

type RunCase = {
  run: string;
  label: string;
  /** Non-flag argv prefix the CLI must receive. */
  match: string[];
  /** Flags that must be present. */
  flags?: string[];
  /** Flags that must be absent. */
  forbid?: string[];
  confirm?: string;
  /** Streamed host actions use cli_stream; security scan uses cli_run. */
  cmd: "cli_run" | "cli_stream";
};

const RUN_ACTIONS: RunCase[] = [
  {
    run: "security fix",
    label: "Apply patches",
    match: ["security", "fix", "demo"],
    forbid: ["--reboot"],
    cmd: "cli_stream",
  },
  {
    run: "security fix --reboot",
    label: "Patch and reboot",
    match: ["security", "fix", "demo"],
    flags: ["--reboot"],
    confirm: "Install updates and reboot?",
    cmd: "cli_stream",
  },
  {
    run: "security scan",
    label: "Rescan CVEs",
    match: ["security", "scan", "demo"],
    forbid: ["--wait"],
    cmd: "cli_run",
  },
  {
    run: "security reboot",
    label: "Reboot",
    match: ["security", "reboot", "demo"],
    forbid: ["--wait"],
    confirm: "Reboot the Base now?",
    cmd: "cli_stream",
  },
  {
    run: "security reboot --wait",
    label: "Reboot and wait",
    match: ["security", "reboot", "demo"],
    flags: ["--wait"],
    confirm: "Reboot and wait?",
    cmd: "cli_stream",
  },
  {
    run: "security install-scanner",
    label: "Install scanner",
    match: ["security", "install-scanner", "demo"],
    cmd: "cli_stream",
  },
  {
    run: "self-update",
    label: "Self-update",
    match: ["self-update", "demo"],
    flags: ["--version", "latest"],
    confirm: "Replace the daemon?",
    cmd: "cli_stream",
  },
  {
    run: "upgrade --apply",
    label: "Apply upgrade",
    match: ["upgrade", "demo"],
    flags: ["--apply"],
    confirm: "Apply core upgrade?",
    cmd: "cli_stream",
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
        page.once("dialog", (d) => {
          expect(d.message()).toContain(action.confirm!.slice(0, 10));
          void d.accept();
        });
      } else {
        // A surprise confirm would auto-dismiss and hang the action — fail fast.
        page.once("dialog", (d) => {
          throw new Error(`unexpected confirm for ${action.run}: ${d.message()}`);
        });
      }
      await btn.click();

      await expect
        .poll(async () =>
          callMatched(await getCalls(page), action.match, {
            cmd: action.cmd,
            includeFlags: action.flags,
          }),
        )
        .toBeTruthy();

      const call = (await getCalls(page)).find(
        (c) =>
          c.cmd === action.cmd &&
          c.args &&
          callMatched([c], action.match, { includeFlags: action.flags }),
      );
      expect(call, `expected ${action.cmd} ${action.match.join(" ")}`).toBeTruthy();
      for (const f of action.forbid ?? []) {
        expect(call!.args, `must not include ${f}`).not.toContain(f);
      }
      for (const f of action.flags ?? []) {
        expect(call!.args, `must include ${f}`).toContain(f);
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
    // Button returns to idle; no stream recorded.
    await expect(page.getByRole("button", { name: "Self-update" })).toBeEnabled();
    await expect
      .poll(async () => callMatched(await getCalls(page), ["self-update", "demo"]))
      .toBeFalsy();
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
    await expect(page.getByRole("tab", { name: "Backups" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    // healthyCheckup has last_backup → configured backups panel.
    await expect(page.getByText("Last snapshot")).toBeVisible();

    const extra = (await getCalls(page)).slice(before.length);
    // Allow only read-only follow-ups.
    const allowed = (c: (typeof extra)[number]) => {
      if (!c.args) return true;
      if (c.cmd === "cli_stream") return false;
      return (
        callMatched([c], ["checkup"]) ||
        callMatched([c], ["list"]) ||
        callMatched([c], ["vault", "status"]) ||
        callMatched([c], ["db", "status"])
      );
    };
    const offenders = extra.filter((c) => !allowed(c));
    expect(offenders, JSON.stringify(offenders.map((c) => c.args))).toEqual([]);
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
    await expect(page.getByText("Unknown action: not-a-real-action")).toBeVisible();
    const streams = (await getCalls(page)).filter((c) => c.cmd === "cli_stream");
    expect(streams).toHaveLength(0);
  });
});
