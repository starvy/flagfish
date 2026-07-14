import { applyTheme } from "./apply";
import { readPreference, prefersDark } from "./preference";
import { resolveTheme } from "./resolve";

// bootTheme runs once, synchronously, before React renders. It resolves from the two
// inputs available without a network round-trip — the saved preference and the system
// setting — and applies them, so the first paint is already the right theme rather
// than a flash of the default. The instance default and any custom override arrive
// with the app and are folded in by the provider; on a return visit the saved
// preference already settles it here.
export function bootTheme(): void {
  applyTheme(resolveTheme({ preference: readPreference(), prefersDark: prefersDark() }));
}
