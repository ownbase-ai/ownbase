// Shared Playwright test object. After every test, fail if the shim's
// fall-through error leaked into the UI — "a call was recorded" is not enough.

import { expect, test as base, type Page } from "@playwright/test";

import { getCalls, type CliCall } from "../shim/install";

/* eslint-disable react-hooks/rules-of-hooks -- Playwright fixture `use`, not React */
export const test = base.extend({
  page: async ({ page }, use) => {
    await use(page);
    // Skip pages that never loaded the app (failed before goto).
    const url = page.url();
    if (!url || url === "about:blank") return;
    const body = await page.locator("body").innerText().catch(() => "");
    expect(body, "shim fall-through leaked into the UI").not.toContain(
      "e2e mock has no handler",
    );
  },
});
/* eslint-enable react-hooks/rules-of-hooks */

export { expect };

/**
 * Poll until the recorded call count is stable for `stableMs`, then return
 * calls. Fails with a clear message if quiet never arrives (unbounded loops
 * used to surface as a 30s test timeout).
 */
export async function waitForQuiet(
  page: Page,
  stableMs = 300,
  timeoutMs = 5_000,
): Promise<CliCall[]> {
  const deadline = Date.now() + timeoutMs;
  let last = -1;
  let stableSince = Date.now();
  for (;;) {
    if (Date.now() > deadline) {
      throw new Error(
        `waitForQuiet: call count never stayed stable for ${stableMs}ms within ${timeoutMs}ms (last count=${last})`,
      );
    }
    const calls = await getCalls(page);
    if (calls.length === last) {
      if (Date.now() - stableSince >= stableMs) return calls;
    } else {
      last = calls.length;
      stableSince = Date.now();
    }
    await page.waitForTimeout(50);
  }
}
