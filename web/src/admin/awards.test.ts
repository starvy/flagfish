import { describe, expect, it } from "vitest";
import { formatSignedPoints, isGrantValid, netContribution, parsePoints } from "./awards";

describe("netContribution", () => {
  it("sums positive make-goods and negative penalties into a signed net", () => {
    expect(netContribution([{ value: 250 }, { value: -100 }, { value: 50 }])).toBe(200);
  });

  it("is zero for an empty ledger and for adjustments that cancel out", () => {
    expect(netContribution([])).toBe(0);
    expect(netContribution([{ value: 100 }, { value: -100 }])).toBe(0);
  });
});

describe("formatSignedPoints", () => {
  it("prefixes a positive with + and leaves the sign on the rest", () => {
    expect(formatSignedPoints(250)).toBe("+250");
    expect(formatSignedPoints(-100)).toBe("-100");
    expect(formatSignedPoints(0)).toBe("0");
  });
});

describe("parsePoints", () => {
  it("accepts a signed whole number", () => {
    expect(parsePoints("250")).toBe(250);
    expect(parsePoints("-100")).toBe(-100);
  });

  it("rejects empty, fractional, non-numeric and the no-op zero", () => {
    expect(parsePoints("")).toBeNull();
    expect(parsePoints("   ")).toBeNull();
    expect(parsePoints("1.5")).toBeNull();
    expect(parsePoints("abc")).toBeNull();
    expect(parsePoints("0")).toBeNull();
  });
});

describe("isGrantValid", () => {
  it("requires both a nonzero integer and a non-blank reason", () => {
    expect(isGrantValid("-250", "flag sharing")).toBe(true);
    expect(isGrantValid("-250", "   ")).toBe(false);
    expect(isGrantValid("0", "reason")).toBe(false);
    expect(isGrantValid("", "reason")).toBe(false);
  });
});
