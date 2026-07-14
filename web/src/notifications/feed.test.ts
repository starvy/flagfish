import { describe, expect, it } from "vitest";
import { excerpt, isFirstBlood, mergeNotifications, newestId, type Notification } from "./feed";

const n = (id: number, title = `n${id}`, content = ""): Notification => ({
  id,
  title,
  content,
  date: new Date(Date.UTC(2026, 0, 1, 0, 0, id)).toISOString(),
});

describe("mergeNotifications", () => {
  it("dedupes the replay overlap and orders newest first", () => {
    const stream = [n(3), n(2)]; // the stream hands them back newest-first
    const backfill = [n(3), n(2), n(1)]; // the page repeats what the replay already sent

    expect(mergeNotifications(stream, backfill).map((x) => x.id)).toEqual([3, 2, 1]);
  });

  it("keeps the first copy of an id, whichever source it came from", () => {
    const live = [{ ...n(7), title: "live" }];
    const paged = [{ ...n(7), title: "paged" }];

    expect(mergeNotifications(live, paged)[0].title).toBe("live");
  });

  it("tolerates an absent page — the backfill can 403 or simply not have answered", () => {
    expect(mergeNotifications([n(1)], null, undefined).map((x) => x.id)).toEqual([1]);
    expect(mergeNotifications()).toEqual([]);
  });
});

describe("newestId", () => {
  it("is 0 for an empty feed, so a waterline is never NaN", () => {
    expect(newestId([])).toBe(0);
    expect(newestId([n(4), n(9), n(2)])).toBe(9);
  });
});

describe("isFirstBlood", () => {
  it.each([
    ["First blood: heap-overflow", true],
    ["firstblood on rev/1", true],
    ["FIRST  BLOOD", true],
    ["Scoreboard frozen", false],
    ["Blood drive in room 2", false],
  ])("%s → %s", (title, want) => {
    expect(isFirstBlood(n(1, title))).toBe(want);
  });
});

describe("excerpt", () => {
  it("takes the first prose line and skips a fence", () => {
    expect(excerpt("```\ncode\n```\nthe answer is 42")).toBe("code");
    expect(excerpt("\n\nhello **world**\nsecond line")).toBe("hello **world**");
  });

  it("caps the length", () => {
    expect(excerpt("x".repeat(200), 10)).toBe(`${"x".repeat(9)}…`);
  });
});
