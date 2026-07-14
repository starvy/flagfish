import { describe, expect, it } from "vitest";
import { resolveTheme, SYSTEM } from "./resolve";
import { TOKEN_KEYS, sanitizeOverrides } from "./tokens";
import { THEMES } from "./registry";

describe("resolveTheme", () => {
  it("prefers a saved preference over instance default and system", () => {
    const r = resolveTheme({
      preference: "amber",
      instanceDefault: "light",
      prefersDark: false,
    });
    expect(r.theme.name).toBe("amber");
    expect(r.source).toBe("preference");
  });

  it("falls back to the instance default when there is no preference", () => {
    const r = resolveTheme({
      preference: null,
      instanceDefault: "light",
      prefersDark: true,
    });
    expect(r.theme.name).toBe("light");
    expect(r.source).toBe("instance");
  });

  it("falls back to the system light/dark choice when nothing else matches", () => {
    expect(resolveTheme({ prefersDark: true }).theme.colorScheme).toBe("dark");
    expect(resolveTheme({ prefersDark: false }).theme.colorScheme).toBe("light");
    expect(resolveTheme({ prefersDark: true }).source).toBe("system");
  });

  it("treats the system sentinel as an explicit follow-OS preference", () => {
    const dark = resolveTheme({ preference: SYSTEM, instanceDefault: "light", prefersDark: true });
    expect(dark.theme.colorScheme).toBe("dark");
    expect(dark.source).toBe("preference");

    const lightPref = resolveTheme({ preference: SYSTEM, instanceDefault: "amber", prefersDark: false });
    expect(lightPref.theme.colorScheme).toBe("light");
  });

  it("ignores an unknown preference and an unknown instance default", () => {
    const r = resolveTheme({
      preference: "does-not-exist",
      instanceDefault: "also-gone",
      prefersDark: true,
    });
    expect(r.source).toBe("system");
    expect(r.theme.colorScheme).toBe("dark");
  });

  it("ignores an unknown preference but still honours a valid instance default", () => {
    const r = resolveTheme({
      preference: "core", // a name this build does not ship
      instanceDefault: "amber",
      prefersDark: false,
    });
    expect(r.theme.name).toBe("amber");
    expect(r.source).toBe("instance");
  });

  it("merges a custom override over the resolved base theme", () => {
    const r = resolveTheme({
      preference: "terminal",
      custom: { "color-accent": "#ff00ff", radius: "0px" },
      prefersDark: true,
    });
    expect(r.tokens["color-accent"]).toBe("#ff00ff");
    expect(r.tokens.radius).toBe("0px");
    // Untouched tokens keep the base theme's value.
    expect(r.tokens["color-bg"]).toBe(THEMES[0].tokens["color-bg"]);
  });

  it("does not mutate the base theme when merging an override", () => {
    const base = THEMES.find((t) => t.name === "terminal")!;
    const before = base.tokens["color-accent"];
    resolveTheme({ preference: "terminal", custom: { "color-accent": "#123456" }, prefersDark: true });
    expect(base.tokens["color-accent"]).toBe(before);
  });
});

describe("sanitizeOverrides", () => {
  it("keeps known token keys and drops everything else", () => {
    const clean = sanitizeOverrides({
      "color-bg": "#000",
      "not-a-token": "#fff",
      radius: "4px",
    });
    expect(clean).toEqual({ "color-bg": "#000", radius: "4px" });
  });

  it("drops non-string values and handles null/undefined", () => {
    expect(sanitizeOverrides({ "color-bg": 123 as unknown as string })).toEqual({});
    expect(sanitizeOverrides(null)).toEqual({});
    expect(sanitizeOverrides(undefined)).toEqual({});
  });
});

describe("theme registry", () => {
  it("every theme defines a value for every contract token", () => {
    for (const theme of THEMES) {
      for (const key of TOKEN_KEYS) {
        expect(theme.tokens[key], `${theme.name} is missing --${key}`).toBeTruthy();
      }
    }
  });

  it("names are unique", () => {
    const names = THEMES.map((t) => t.name);
    expect(new Set(names).size).toBe(names.length);
  });
});
