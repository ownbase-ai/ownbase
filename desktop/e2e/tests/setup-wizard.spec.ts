import {
  createStreamEvents,
  demoKeygen,
  preflightFailEvents,
  remoteCreateStreamEvents,
} from "../fixtures/data";
import { expect, test } from "../fixtures/test";
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

    // Stream can finish before the intermediate "Creating…" heading paints.
    await expect(
      page.getByRole("heading", { name: "fresh is up and hardened" }),
    ).toBeVisible({ timeout: 10_000 });
    await page.getByRole("button", { name: "Open fresh" }).click();
    await expect(page.getByRole("heading", { name: "fresh" })).toBeVisible();

    const calls = await getCalls(page);
    expect(callMatched(calls, ["keygen", "fresh"])).toBeTruthy();
    expect(callMatched(calls, ["create", "fresh"], { cmd: "cli_stream" })).toBeTruthy();
    const create = calls.find((c) => c.cmd === "cli_stream" && callMatched([c], ["create", "fresh"]));
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

    // Stream finishes quickly; either the install heading or the done step is fine.
    await expect(
      page.getByRole("heading", { name: "prod is up and hardened" }),
    ).toBeVisible({ timeout: 10_000 });
    await page.getByRole("button", { name: "Open prod" }).click();
    await expect(page.getByRole("heading", { name: "prod", exact: true })).toBeVisible();

    const create = (await getCalls(page)).find(
      (c) => c.cmd === "cli_stream" && callMatched([c], ["create", "prod"]),
    );
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
    // Exit 3 local copy — must not match the generic default branch.
    await expect(
      page.getByText(
        /The local VM was checked before install finished, and something did not pass/i,
      ),
    ).toBeVisible();
    await expect(page.getByText(/disk too small/i)).toBeVisible();
  });

  test("adopt path: key file + host → register stream", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [],
      dialogPath: "/tmp/e2e-adopt-key",
      streams: [
        {
          match: ["adopt", "adopted"],
          events: [
            { kind: "stderr", line: "verifying SSH connection…" },
            { kind: "stderr", line: "connected to adopted" },
            { kind: "finished", code: 0 },
          ],
        },
      ],
    });

    await page.getByText("Add a server that's already running OwnBase").click();
    await page.getByPlaceholder("mybase").fill("adopted");
    await page.getByRole("button", { name: "Continue" }).click();

    await page.getByRole("button", { name: /Choose private key file/i }).click();
    await expect(page.getByText("/tmp/e2e-adopt-key")).toBeVisible();
    await page.getByRole("button", { name: "Continue" }).click();

    await expect(page.getByRole("heading", { name: "Where is it?" })).toBeVisible();
    await page.getByRole("textbox", { name: "Host" }).fill("10.0.0.9");
    await page.getByRole("button", { name: "Verify and register" }).click();

    await expect
      .poll(async () =>
        callMatched(await getCalls(page), ["adopt", "adopted"], { cmd: "cli_stream" }),
      )
      .toBeTruthy();
    const adopt = (await getCalls(page)).find(
      (c) => c.cmd === "cli_stream" && callMatched([c], ["adopt", "adopted"]),
    );
    expect(adopt?.args).toContain("--host");
    expect(adopt?.args).toContain("10.0.0.9");
    expect(adopt?.args).toContain("--ssh-key");
  });

  test("restore local: creds on stdin, never argv", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [],
      streams: [
        {
          match: ["restore", "restored"],
          events: [
            { kind: "stderr", line: "Provisioning…" },
            { kind: "stderr", line: "Restoring from restic…" },
            { kind: "stderr", line: "Registered base restored" },
            { kind: "finished", code: 0 },
          ],
        },
      ],
    });

    await page.getByText("Restore from backups (local VM)").click();
    await page.getByPlaceholder("mybase").fill("restored");
    await page.getByRole("button", { name: "Continue" }).click();

    await page.getByRole("textbox", { name: "Restic repo" }).fill("s3:bucket/path");
    await page.getByLabel("Restic password").fill("restic-secret-password");
    await page.getByRole("button", { name: "Start restore" }).click();

    await expect
      .poll(
        async () =>
          callMatched(await getCalls(page), ["restore", "restored"], { cmd: "cli_stream" }),
        { timeout: 15_000 },
      )
      .toBeTruthy();

    const restore = (await getCalls(page)).find(
      (c) => c.cmd === "cli_stream" && callMatched([c], ["restore", "restored"]),
    );
    expect(restore?.args).toContain("--creds-stdin");
    expect(restore?.args?.join(" ")).not.toContain("restic-secret-password");
    expect(restore?.stdin).toContain("restic-secret-password");
  });

  test("stream cancel during create", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [],
      keygen: demoKeygen,
      streams: [
        {
          match: ["create", "slow"],
          delayMs: 200,
          events: [
            { kind: "stderr", line: "Provisioning local VM slow…" },
            { kind: "stderr", line: "VM launched." },
            { kind: "stderr", line: "still working…" },
            { kind: "finished", code: 0 },
          ],
        },
      ],
    });

    await page.getByText("Local VM on this computer").click();
    await page.getByPlaceholder("mybase").fill("slow");
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByRole("button", { name: "Generate a new key" }).click();
    await page.getByRole("button", { name: "Create the VM" }).click();

    await expect(page.getByText("Creating the local VM")).toBeVisible();
    await page.getByRole("button", { name: "Stop" }).click();

    await expect
      .poll(async () => (await getCalls(page)).some((c) => c.cmd === "cli_cancel"))
      .toBeTruthy();
  });

  test("restore local Cancel: userCancelled guard + Cancelled. copy", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [],
      streams: [
        {
          match: ["restore", "slowrest"],
          delayMs: 250,
          events: [
            { kind: "stderr", line: "Provisioning…" },
            { kind: "stderr", line: "Restoring from restic…" },
            { kind: "stderr", line: "still restoring…" },
            { kind: "finished", code: 0 },
          ],
        },
      ],
    });

    await page.getByText("Restore from backups (local VM)").click();
    await page.getByPlaceholder("mybase").fill("slowrest");
    await page.getByRole("button", { name: "Continue" }).click();

    await page.getByRole("textbox", { name: "Restic repo" }).fill("s3:bucket/path");
    await page.getByLabel("Restic password").fill("restic-secret-password");
    await page.getByRole("button", { name: "Start restore" }).click();

    await expect(page.getByText("Restoring from backup")).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();

    await expect(page.getByRole("heading", { name: "Restore cancelled" })).toBeVisible();
    await expect(page.getByText("Cancelled.", { exact: true })).toBeVisible();
    // Must not flip to the success "Restored" path after cancel (PR #51 regression).
    await expect(page.getByRole("heading", { name: "Restored" })).toHaveCount(0);
    await expect
      .poll(async () => (await getCalls(page)).some((c) => c.cmd === "cli_cancel"))
      .toBeTruthy();
  });
});
