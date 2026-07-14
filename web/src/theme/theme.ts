import type { Tokens } from "./tokens";

// A theme is a name, a label for the switcher, the light/dark hint the browser
// needs for form controls and scrollbars, and one value for every contract token.
// Nothing else — adding a theme is adding one of these and one registry line.
export interface Theme {
  // Stable identifier. Matches the `ctf_theme` config value an instance sets as
  // its default, and the value persisted to localStorage.
  name: string;
  label: string;
  colorScheme: "dark" | "light";
  tokens: Tokens;
}
