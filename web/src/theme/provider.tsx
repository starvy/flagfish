import { createContext, use, useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { instanceQuery } from "../queries";
import { applyTheme } from "./apply";
import { readPreference, writePreference, prefersDark as systemPrefersDark } from "./preference";
import { resolveTheme, SYSTEM } from "./resolve";
import type { Resolved } from "./resolve";
import { sanitizeOverrides } from "./tokens";
import { THEMES } from "./registry";
import type { Theme } from "./theme";

interface ThemeContextValue {
  resolved: Resolved;
  // The saved preference: a theme name, SYSTEM, or null for "unset".
  preference: string | null;
  setPreference: (value: string | null) => void;
  themes: readonly Theme[];
  system: typeof SYSTEM;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

// usePrefersDark tracks the OS light/dark setting live, so a system-following user
// who flips their OS theme sees the app follow without a reload.
function usePrefersDark(): boolean {
  const [dark, setDark] = useState(systemPrefersDark);
  useEffect(() => {
    if (typeof matchMedia === "undefined") return;
    const mq = matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => setDark(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);
  return dark;
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setPreferenceState] = useState<string | null>(readPreference);
  const prefersDark = usePrefersDark();

  // The instance default and custom override are public, so this loads before login.
  // A failure is not fatal: the resolver falls back to preference and system.
  const { data: instance } = useQuery(instanceQuery);

  const resolved = useMemo(
    () =>
      resolveTheme({
        preference,
        instanceDefault: instance?.theme,
        custom: sanitizeOverrides(instance?.theme_tokens),
        prefersDark,
      }),
    [preference, instance?.theme, instance?.theme_tokens, prefersDark],
  );

  useEffect(() => applyTheme(resolved), [resolved]);

  const setPreference = useCallback((value: string | null) => {
    writePreference(value);
    setPreferenceState(value);
  }, []);

  const value = useMemo<ThemeContextValue>(
    () => ({ resolved, preference, setPreference, themes: THEMES, system: SYSTEM }),
    [resolved, preference, setPreference],
  );

  return <ThemeContext value={value}>{children}</ThemeContext>;
}

export function useTheme(): ThemeContextValue {
  const ctx = use(ThemeContext);
  if (!ctx) throw new Error("useTheme must be used within a ThemeProvider");
  return ctx;
}
