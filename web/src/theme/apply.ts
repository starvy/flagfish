import { TOKEN_KEYS } from "./tokens";
import type { Resolved } from "./resolve";

// Writing the resolved tokens straight onto the document root's inline style makes a
// theme switch a set of property writes — instant, no rebuild, no stylesheet swap.
// color-scheme rides along so the browser paints form controls and scrollbars to
// match; data-theme is set so CSS can special-case a theme if it ever needs to.
export function applyTheme(resolved: Resolved): void {
  const root = document.documentElement;
  for (const key of TOKEN_KEYS) {
    root.style.setProperty(`--${key}`, resolved.tokens[key]);
  }
  root.style.colorScheme = resolved.theme.colorScheme;
  root.dataset.theme = resolved.theme.name;
}
