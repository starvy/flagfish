import { describe, expect, it } from "vitest";
import { formatRelative, relativeTickMs } from "./timeFormat";

const now = new Date("2026-07-14T12:00:00Z");
const ago = (ms: number) => new Date(now.getTime() - ms);

describe("formatRelative", () => {
  it.each([
    [ago(5_000), "just now"],
    [ago(3 * 60_000), "3m ago"],
    [ago(90 * 60_000), "2h ago"],
    [ago(50 * 3_600_000), "2d ago"],
    [ago(60 * 86_400_000), "2mo ago"],
    [new Date(now.getTime() + 10 * 60_000), "in 10m"],
  ])("formats %s as %s", (at, want) => {
    expect(formatRelative(at, now)).toBe(want);
  });
});

describe("relativeTickMs", () => {
  it("widens the interval as the timestamp ages", () => {
    expect(relativeTickMs(ago(30_000), now)).toBeLessThan(relativeTickMs(ago(7_200_000), now));
    expect(relativeTickMs(ago(3 * 86_400_000), now)).toBe(3_600_000);
  });
});
