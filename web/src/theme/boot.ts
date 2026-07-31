import { readPreference as readViewChoice } from "../views/preference";
import { viewById } from "../views/registry";
import { applyTheme } from "./apply";
import { readPreference, prefersDark } from "./preference";
import { resolveTheme } from "./resolve";

// bootTheme runs once, synchronously, before React renders. It resolves from the
// inputs available without a network round-trip — the saved theme preference, the
// saved portal view and the system setting — and applies them, so the first paint is
// already the right theme rather than a flash of the default. The instance default,
// any custom override, and a view the instance leads with arrive with the app and are
// folded in by the provider.
export function bootTheme(): void {
  applyTheme(
    resolveTheme({
      // A player who chose the globe for themselves is knowable here, and is the player who lands
      // on it every time. An instance leading everyone into it is not: that takes the snapshot,
      // and the snapshot takes a request.
      viewTheme: viewById(readViewChoice())?.theme ?? null,
      preference: readPreference(),
      prefersDark: prefersDark(),
    }),
  );
}
