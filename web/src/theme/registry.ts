import type { Theme } from "./theme";
import { terminal } from "./themes/terminal";
import { light } from "./themes/light";
import { amber } from "./themes/amber";
import { nocturne } from "./themes/nocturne";

// The registry. Adding a theme is one import above and one entry here — nothing
// else in the app references a theme by name.
export const THEMES: readonly Theme[] = [terminal, light, amber, nocturne];

// The built-in default, and the theme a paint before any resolution should assume.
export const DEFAULT_THEME = terminal;

// The theme a system dark/light preference maps to when nothing more specific wins.
export const SYSTEM_DARK = terminal;
export const SYSTEM_LIGHT = light;

export function themeByName(name: string | null | undefined): Theme | undefined {
  if (!name) return undefined;
  return THEMES.find((t) => t.name === name);
}
