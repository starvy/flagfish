import { createContext, use, useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { instanceQuery } from "../queries";
// The module, not the barrel: this provider is in the root chunk, and the barrel would drag the
// view outlet into it for the login page's benefit.
import { useResolvedPortalView } from "../views/usePortalView";
import { applyTheme } from "./apply";
import { readPreference, writePreference, prefersDark as systemPrefersDark } from "./preference";
import { resolveTheme, SYSTEM } from "./resolve";
import type { Resolved } from "./resolve";
import { sanitizeOverrides } from "./tokens";
import { THEMES } from "./registry";
import type { Theme } from "./theme";

interface ThemeContextValue {
  resolved: Resolved;
  /** The active view's theme is what is on screen, and it is not what the player asked for. */
  viewOverrides: boolean;
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

  // A view may carry a skin, and it is resolved here rather than inside the board so that the
  // header, the scoreboard and every other page wear it too — half an app in another palette is
  // not a look, it is a bug.
  const view = useResolvedPortalView();
  const viewTheme = view.view.theme ?? null;

  const input = useMemo(
    () => ({
      preference,
      instanceDefault: instance?.theme,
      custom: sanitizeOverrides(instance?.theme_tokens),
      prefersDark,
    }),
    [preference, instance?.theme, instance?.theme_tokens, prefersDark],
  );

  // What the player's own settings come to, resolved whether or not a view is currently sitting
  // on top of them: the picker in settings can only explain itself if it knows both answers.
  const own = useMemo(() => resolveTheme(input), [input]);
  const resolved = useMemo(
    () => (viewTheme === null ? own : resolveTheme({ ...input, viewTheme })),
    [input, own, viewTheme],
  );

  useEffect(() => applyTheme(resolved), [resolved]);

  const setPreference = useCallback((value: string | null) => {
    writePreference(value);
    setPreferenceState(value);
  }, []);

  const value = useMemo<ThemeContextValue>(
    () => ({
      resolved,
      viewOverrides: resolved.theme !== own.theme,
      preference,
      setPreference,
      themes: THEMES,
      system: SYSTEM,
    }),
    [resolved, own, preference, setPreference],
  );

  return <ThemeContext value={value}>{children}</ThemeContext>;
}

export function useTheme(): ThemeContextValue {
  const ctx = use(ThemeContext);
  if (!ctx) throw new Error("useTheme must be used within a ThemeProvider");
  return ctx;
}
