# Theming

Themes are design-token data. A theme is one value for each key in the token
contract (`tokens.ts`); nothing renders a raw colour — every component rule in
`styles.css` reads a `--color-*` / `--radius` / `--shadow` / `--font-*` variable, and
the active theme sets those on the document root at runtime.

## The token contract

The canonical semantic tokens (`TOKEN_KEYS` in `tokens.ts`):

| Token | Role |
|---|---|
| `--color-bg` | page background |
| `--color-surface` | panels, cards, dialogs |
| `--color-inset` | inputs, code, sunken areas |
| `--color-border` / `--color-border-strong` | dividers, outlines |
| `--color-text` / `--color-text-muted` | body text, secondary text |
| `--color-accent` / `--color-accent-dim` | primary accent, resting fill |
| `--color-accent-contrast` | text drawn on a solid accent |
| `--color-danger` / `--color-danger-contrast` | destructive actions |
| `--color-warn` | warnings, token reveal |
| `--color-blood` | first-blood highlight |
| `--color-success` | success states |
| `--color-overlay` | modal backdrop |
| `--radius` / `--shadow` | panel radius, elevation |
| `--font-body` / `--font-mono` | body and monospace faces |

## Adding a theme (one file + one line)

1. Add `themes/<name>.ts` exporting a `Theme` — a `name`, a `label`, a
   `colorScheme` (`"dark"` | `"light"`), and a value for every contract token. TypeScript
   fails the build if a key is missing.
2. Add it to the `THEMES` array in `registry.ts` (one import, one entry).

That is the whole contract. No stylesheet change, no build wiring, no component edit.
The picker, resolution, and persistence pick it up from the registry.

## Resolution order

`resolveTheme` (`resolve.ts`) is a pure function applying, in order:

1. **The active view's theme** — a portal view (`views/`) may name a colour theme it brings
   with it; the globe brings `nocturne`. While that view is the resolved one it owns the
   palette, because a view is a whole presentation and one that repaints the board but not
   the header is half a design. The player's way out is the switch back to the standard
   board, which takes its theme with it.
2. **User preference** — `localStorage["flagfish.theme"]`: a theme name, or the `system`
   sentinel to follow the OS.
3. **Instance default** — the backend `ctf_theme` config, served by the public
   `GET /api/v1/instance` endpoint (readable before login).
4. **System** — `prefers-color-scheme`, mapping to a built-in dark or light theme.

An unknown name at any level does not error; it simply fails to match and the next
source is tried, so a stale preference, a `ctf_theme` this build does not ship, and a view
naming a theme that has been removed all fall back safely. The admin custom-override blob
(`theme_tokens`, a JSON object of token overrides) is merged over whichever base theme wins,
the view's included. `resolved.source` says which level answered, and the picker in settings
uses it to explain itself when the answer was not the player's.

`theme/` may import from `views/`; `views/` must not import from `theme/`. A view names its
theme as a string, and this is the only module that turns a name into a theme.

## Applying and no-flash

`apply.ts` writes the resolved tokens to `document.documentElement` inline style, sets
`color-scheme`, and stamps `data-theme`. `boot.ts` runs once before React renders,
resolving from the two inputs available without a network call (saved preference +
system), so first paint is themed. The `:root` block in `styles.css` mirrors the
terminal theme as the pre-JS fallback. The instance default and custom override arrive
with the app and are folded in by `ThemeProvider`.
