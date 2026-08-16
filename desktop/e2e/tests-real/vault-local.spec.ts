// Tier B smoke: real ownbasectl against an isolated HOME. No Multipass required.
// Covers vault create/unlock through the UI with the bridge-backed shim.

import { expect, test } from "@playwright/test";

const BRIDGE = `http://127.0.0.1:${process.env.E2E_BRIDGE_PORT || 7391}`;

test.beforeAll(async ({ request }) => {
  const health = await request.get(`${BRIDGE}/health`);
  expect(health.ok()).toBeTruthy();
});

test("create vault via real ownbasectl", async ({ page }) => {
  // Install a bridge-backed Tauri mock (forwards cli_* to the local bridge).
  await page.addInitScript(
    ({ bridge }) => {
      type CliResult = { code: number; stdout: string; stderr: string };
      const callbacks = new Map<number, (data: unknown) => void>();

      function transformCallback(cb?: (data: unknown) => void, once = false): number {
        const id = crypto.getRandomValues(new Uint32Array(1))[0]!;
        callbacks.set(id, (data) => {
          if (once) callbacks.delete(id);
          cb?.(data);
        });
        return id;
      }

      function runCallback(id: number, data: unknown) {
        callbacks.get(id)?.(data);
      }

      function channelId(onEvent: unknown): number | null {
        if (onEvent && typeof onEvent === "object" && onEvent !== null && "id" in onEvent) {
          return (onEvent as { id: number }).id;
        }
        return null;
      }

      async function invoke(cmd: string, args: Record<string, unknown> = {}) {
        if (cmd === "cli_run") {
          const res = await fetch(`${bridge}/run`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ args: args.args, stdin: args.stdin ?? null }),
          });
          return (await res.json()) as CliResult;
        }
        if (cmd === "cli_stream") {
          const id = String(args.id ?? "");
          const ch = channelId(args.onEvent);
          void (async () => {
            const res = await fetch(`${bridge}/stream`, {
              method: "POST",
              headers: { "content-type": "application/json" },
              body: JSON.stringify({
                id,
                args: args.args,
                stdin: args.stdin ?? null,
              }),
            });
            const reader = res.body?.getReader();
            if (!reader || ch == null) return;
            const dec = new TextDecoder();
            let buf = "";
            let index = 0;
            for (;;) {
              const { done, value } = await reader.read();
              if (done) break;
              buf += dec.decode(value, { stream: true });
              const parts = buf.split("\n\n");
              buf = parts.pop() ?? "";
              for (const part of parts) {
                const line = part
                  .split("\n")
                  .find((l) => l.startsWith("data: "));
                if (!line) continue;
                const event = JSON.parse(line.slice(6));
                runCallback(ch, { index, message: event });
                index++;
                if (event.kind === "finished" || event.kind === "failed") {
                  runCallback(ch, { index, end: true });
                  return;
                }
              }
            }
          })();
          return null;
        }
        if (cmd === "cli_cancel") {
          await fetch(`${bridge}/cancel`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ id: args.id }),
          });
          return null;
        }
        if (cmd === "plugin:dialog|open") {
          return "/tmp/ownbase-e2e-real";
        }
        throw new Error(`unhandled ${cmd}`);
      }

      (window as unknown as { __TAURI_INTERNALS__: unknown }).__TAURI_INTERNALS__ = {
        invoke,
        transformCallback,
        unregisterCallback: (id: number) => callbacks.delete(id),
        runCallback,
        callbacks,
        convertFileSrc: (p: string) => p,
        metadata: {
          currentWindow: { label: "main" },
          currentWebview: { windowLabel: "main", label: "main" },
        },
      };
      (window as unknown as { __TAURI_EVENT_PLUGIN_INTERNALS__: unknown }).__TAURI_EVENT_PLUGIN_INTERNALS__ =
        { unregisterListener: () => {} };
    },
    { bridge: BRIDGE },
  );

  await page.goto("/");

  // Fresh isolated HOME → absent vault.
  await expect(page.getByRole("heading", { name: "Create your vault" })).toBeVisible({
    timeout: 30_000,
  });

  await page.getByPlaceholder("~/Dropbox/OwnBase").fill("/tmp/ownbase-e2e-real");
  await page.locator('input[type="password"]').nth(0).fill("e2e-real-master-password");
  await page.locator('input[type="password"]').nth(1).fill("e2e-real-master-password");
  await page.getByRole("button", { name: "Create vault" }).click();

  await expect(page.getByRole("heading", { name: "Set up a Base" })).toBeVisible({
    timeout: 30_000,
  });
});
