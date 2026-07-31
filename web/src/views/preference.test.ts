import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const KEY = "flagfish.portalView";

function fakeStorage(seed: Record<string, string> = {}): Storage {
  const map = new Map<string, string>(Object.entries(seed));
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

// The store seeds itself from storage when the module loads, so every test gets a fresh one.
async function load() {
  vi.resetModules();
  return import("./preference");
}

describe("portal view preference", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", fakeStorage());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("round-trips a choice", async () => {
    const { readPreference, writePreference } = await load();
    expect(readPreference()).toBeNull();
    writePreference("standard");
    expect(localStorage.getItem(KEY)).toBe("standard");
    expect(readPreference()).toBe("standard");
  });

  it("clears the key rather than storing an empty choice", async () => {
    const { readPreference, writePreference } = await load();
    writePreference("globe");
    writePreference(null);
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(readPreference()).toBeNull();
  });

  it("starts from what the last visit saved", async () => {
    vi.stubGlobal("localStorage", fakeStorage({ [KEY]: "globe" }));
    const { readPreference } = await load();
    expect(readPreference()).toBe("globe");
  });

  // The reason this is a store at all: the board and the theme both read it, and one click has
  // to move both.
  it("hands one write to every consumer", async () => {
    const { readPreference, subscribePreference, writePreference } = await load();
    const seen: (string | null)[][] = [[], []];
    subscribePreference(() => seen[0]!.push(readPreference()));
    subscribePreference(() => seen[1]!.push(readPreference()));

    writePreference("globe");

    expect(seen[0]).toEqual(["globe"]);
    expect(seen[1]).toEqual(["globe"]);
  });

  it("says nothing when the write does not change the value", async () => {
    const { subscribePreference, writePreference } = await load();
    writePreference("globe");
    let notified = 0;
    subscribePreference(() => (notified += 1));
    writePreference("globe");
    expect(notified).toBe(0);
  });

  it("stops notifying once a consumer unsubscribes", async () => {
    const { subscribePreference, writePreference } = await load();
    let notified = 0;
    const off = subscribePreference(() => (notified += 1));
    off();
    writePreference("globe");
    expect(notified).toBe(0);
  });

  it("follows a choice another tab made", async () => {
    const handlers = new Map<string, (event: StorageEvent) => void>();
    vi.stubGlobal("window", {
      addEventListener: (type: string, handler: (event: StorageEvent) => void) =>
        void handlers.set(type, handler),
    });

    const { readPreference, subscribePreference } = await load();
    let notified = 0;
    subscribePreference(() => (notified += 1));

    localStorage.setItem(KEY, "globe");
    handlers.get("storage")!({ key: KEY } as StorageEvent);

    expect(readPreference()).toBe("globe");
    expect(notified).toBe(1);
  });

  it("ignores another key changing", async () => {
    const handlers = new Map<string, (event: StorageEvent) => void>();
    vi.stubGlobal("window", {
      addEventListener: (type: string, handler: (event: StorageEvent) => void) =>
        void handlers.set(type, handler),
    });

    const { readPreference, subscribePreference } = await load();
    subscribePreference(() => {});
    localStorage.setItem(KEY, "globe");
    handlers.get("storage")!({ key: "flagfish.theme" } as StorageEvent);

    expect(readPreference()).toBeNull();
  });

  // A browser with storage switched off must still render a board; it just cannot remember.
  it("survives a browser that refuses storage", async () => {
    vi.stubGlobal("localStorage", undefined);
    const { readPreference, writePreference } = await load();
    expect(readPreference()).toBeNull();
    expect(() => writePreference("globe")).not.toThrow();
    expect(readPreference()).toBe("globe");
  });
});
