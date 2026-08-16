import { expect, test } from "@playwright/test";

import {
  demoBase,
  demoSessions,
  healthyCheckup,
  sampleCast,
  sampleTranscript,
} from "../fixtures/data";
import { openApp } from "../shim/install";

test.describe("sessions", () => {
  test("lists recorded sessions and shows transcript", async ({ page }) => {
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
    await expect(page.getByRole("heading", { name: "Sessions" })).toBeVisible();
    await expect(page.getByText("interactive shell")).toBeVisible();
    await expect(page.getByText("systemctl status ownbased")).toBeVisible();

    await page.getByText("interactive shell").click();
    await expect(page.getByText(id, { exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Transcript" }).click();
    await expect(page.getByText(/echo hello/)).toBeVisible();
  });

  test("empty state when nothing is recorded", async ({ page }) => {
    await openApp(page, {
      vault: "unlocked",
      bases: [demoBase],
      checkup: { demo: healthyCheckup },
      sessions: [],
    });

    await page.getByRole("navigation").getByText("Sessions").click();
    await expect(page.getByText(/No sessions recorded yet/)).toBeVisible();
  });
});
