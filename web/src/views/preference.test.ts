import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readPreference, writePreference } from "./preference";

const KEY = "flagfish.portalView";

function fakeStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() {
      return map.size;
    },
    clear: () => map.clear(),
    getItem: (k: string) => map.get(k) ?? null,
    key: (i: number) => [...map.keys()][i] ?? null,
    removeItem: (k: string) => void map.delete(k),
    setItem: (k: string, v: string) => void map.set(k, v),
  };
}

describe("portal view preference", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", fakeStorage());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("round-trips a choice", () => {
    expect(readPreference()).toBeNull();
    writePreference("standard");
    expect(localStorage.getItem(KEY)).toBe("standard");
    expect(readPreference()).toBe("standard");
  });

  it("clears the key rather than storing an empty choice", () => {
    writePreference("globe");
    writePreference(null);
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(readPreference()).toBeNull();
  });

  // A browser with storage switched off must still render a board; it just cannot remember.
  it("survives a browser that refuses storage", () => {
    vi.stubGlobal("localStorage", undefined);
    expect(readPreference()).toBeNull();
    expect(() => writePreference("globe")).not.toThrow();
  });
});
