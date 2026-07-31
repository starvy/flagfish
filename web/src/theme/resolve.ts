import type { Theme } from "./theme";
import type { TokenOverrides, Tokens } from "./tokens";
import { SYSTEM_DARK, SYSTEM_LIGHT, themeByName } from "./registry";

// SYSTEM is the sentinel a user picks to say "follow the OS", distinct from having
// no preference at all.
export const SYSTEM = "system";

export interface ResolveInput {
  // The colour theme the active portal view brings with it, if it brings one. A view is a whole
  // presentation, and while it is the one being rendered it owns the palette — the player's way
  // out is switching back to the standard board, which takes its theme with it.
  viewTheme?: string | null;
  // The user's saved choice: a theme name, SYSTEM, or null/undefined for "unset".
  preference?: string | null;
  // The instance default from the backend `ctf_theme` config.
  instanceDefault?: string | null;
  // The admin custom-override blob, merged over whichever base theme wins.
  custom?: TokenOverrides | null;
  prefersDark: boolean;
}

export type ResolveSource = "view" | "preference" | "instance" | "system";

export interface Resolved {
  theme: Theme;
  // theme.tokens with the custom override applied. This is what gets written to the
  // document root.
  tokens: Tokens;
  source: ResolveSource;
}

function systemTheme(prefersDark: boolean): Theme {
  return prefersDark ? SYSTEM_DARK : SYSTEM_LIGHT;
}

// resolveTheme is the whole resolution order, as one pure function: the active
// view's own theme wins, then a saved preference, then the instance default, then
// the system light/dark choice. An unknown name at any level is not an error — it
// simply does not match and the next source is tried, so a stale localStorage value
// or a `ctf_theme` naming a theme this build does not ship both fall back safely.
export function resolveTheme(input: ResolveInput): Resolved {
  const { viewTheme, preference, instanceDefault, custom, prefersDark } = input;

  let base: Theme | undefined;
  let source: ResolveSource;

  if ((base = themeByName(viewTheme))) {
    source = "view";
  } else if (preference === SYSTEM) {
    base = systemTheme(prefersDark);
    source = "preference";
  } else if ((base = themeByName(preference))) {
    source = "preference";
  } else if ((base = themeByName(instanceDefault))) {
    source = "instance";
  } else {
    base = systemTheme(prefersDark);
    source = "system";
  }

  const tokens: Tokens = custom ? { ...base.tokens, ...custom } : base.tokens;
  return { theme: base, tokens, source };
}
