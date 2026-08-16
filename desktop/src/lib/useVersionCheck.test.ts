import { describe, expect, it } from "vitest";

import type { VersionCheck } from "./types";
import { behindCount } from "./useVersionCheck";

describe("behindCount", () => {
  it("returns 0 for null/empty/current", () => {
    expect(behindCount(null)).toBe(0);
    expect(behindCount(undefined)).toBe(0);
    expect(
      behindCount({
        components: [
          { name: "cli", current: "v0.5.0", status: "current" },
          { name: "app", current: "v0.5.0", status: "dev" },
        ],
      }),
    ).toBe(0);
  });

  it("counts behind components and skew", () => {
    const check: VersionCheck = {
      components: [
        {
          name: "cli",
          current: "v0.4.0",
          latest: "v0.5.0",
          status: "behind",
          guide: "brew upgrade --cask ownbase-ai/tap/ownbasectl",
        },
        {
          name: "app",
          current: "v0.4.0",
          latest: "v0.5.0",
          status: "behind",
          guide: "brew upgrade --cask ownbase-ai/tap/ownbase",
        },
      ],
      skew: {
        direction: "cli_ahead",
        cli: "v0.4.0",
        daemon: "v0.3.0",
        guide: "ownbasectl self-update demo",
        summary: "Base daemon v0.3.0 is behind your CLI v0.4.0",
      },
    };
    expect(behindCount(check)).toBe(3);
  });
});
