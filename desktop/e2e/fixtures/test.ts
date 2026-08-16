// Shared Playwright test object. After every test, fail if the shim's
// fall-through error leaked into the UI — "a call was recorded" is not enough.

import { expect, test as base } from "@playwright/test";

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
