import { afterEach, describe, expect, it, vi } from "vitest";

import { ago, bytes, duration, shortRef, span, until } from "./format";

// These read as cosmetic and are not. "Locks in 4 minutes" where the truth is
// three hours tells the user to hurry for no reason; "last backup 2 days ago"
// where the truth is two months is the opposite mistake, and worse. A wrong
// duration here is indistinguishable from a right one at a glance, which is why
// it gets tests.

const minute = 60;
const hour = 60 * minute;
const day = 24 * hour;

describe("span", () => {
  it("names the unit it actually divided down to", () => {
    expect(span(30)).toBe("30 seconds");
    expect(span(90)).toBe("2 minutes");
    expect(span(45 * minute)).toBe("45 minutes");
    expect(span(4 * hour)).toBe("4 hours");
    expect(span(3 * hour + 23 * minute)).toBe("3 hours");
    expect(span(3 * day)).toBe("3 days");
    expect(span(21 * day)).toBe("3 weeks");
    expect(span(400 * day)).toBe("1 year");
  });

  it("keeps the singular singular", () => {
    expect(span(1)).toBe("1 second");
    expect(span(hour)).toBe("1 hour");
  });
});

describe("ago and until", () => {
  afterEach(() => vi.useRealTimers());

  function at(iso: string) {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(iso));
  }

  it("reads the right direction", () => {
    at("2026-07-27T12:00:00Z");
    expect(ago("2026-07-27T09:00:00Z")).toBe("3 hours ago");
    expect(until("2026-07-27T15:20:00Z")).toBe("in 3 hours");
  });

  it("does not make a person do arithmetic over a few seconds", () => {
    at("2026-07-27T12:00:00Z");
    expect(ago("2026-07-27T11:59:50Z")).toBe("just now");
    expect(until("2026-07-27T12:00:00Z")).toBe("now");
  });

  // The daemon leaves a timestamp unset by sending Go's zero time, which is a
  // real date in year 1 — rendering it as "2025 years ago" would be technically
  // true and completely useless.
  it("treats an unset timestamp as unset", () => {
    at("2026-07-27T12:00:00Z");
    expect(ago(undefined)).toBe("never");
    expect(ago("0001-01-01T00:00:00Z")).toBe("never");
    expect(ago(null, "not yet")).toBe("not yet");
    expect(until("0001-01-01T00:00:00Z")).toBe("never");
  });
});

describe("duration", () => {
  it("says when a session is still open rather than guessing", () => {
    expect(duration("2026-07-27T12:00:00Z")).toBe("still open");
    expect(duration("2026-07-27T12:00:00Z", null)).toBe("still open");
  });

  it("measures the gap", () => {
    expect(duration("2026-07-27T12:00:00Z", "2026-07-27T12:26:00Z")).toBe("26 minutes");
    expect(duration("2026-07-27T12:00:00Z", "2026-07-27T12:00:00.400Z")).toBe(
      "under a second",
    );
  });

  it("refuses to render a negative gap as a duration", () => {
    expect(duration("2026-07-27T12:00:00Z", "2026-07-27T11:00:00Z")).toBe("unknown");
  });
});

describe("bytes", () => {
  it("scales", () => {
    expect(bytes(512)).toBe("512 B");
    expect(bytes(2048)).toBe("2.0 KB");
    expect(bytes(15 * 1024)).toBe("15 KB");
    expect(bytes(3 * 1024 * 1024)).toBe("3.0 MB");
  });
});

describe("shortRef", () => {
  it("shortens a SHA and leaves a branch name alone", () => {
    expect(shortRef("a".repeat(40))).toBe("aaaaaaaa");
    expect(shortRef("main")).toBe("main");
    expect(shortRef("v1.4.0")).toBe("v1.4.0");
  });
});
