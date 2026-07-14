// The token contract: the one canonical set of semantic CSS variables the whole
// UI is allowed to reference. A theme is nothing more than one value for each key
// here, and styles.css consumes only these — no component carries a raw colour.
//
// Keys are written without the leading `--`; applyTokens prepends it when it
// writes them to the document root.
//
// Only what a theme may repaint belongs here. The spacing and type scales are
// structure, not palette: they are fixed at :root in styles.css so a theme cannot
// silently reflow the app, and so a new theme stays a short list of colours.
export const TOKEN_KEYS = [
  "color-bg", // page background
  "color-surface", // panels, cards, dialogs
  "color-inset", // inputs, code, sunken areas
  "color-border",
  "color-border-strong",
  "color-text",
  "color-text-muted",
  "color-accent",
  "color-accent-dim", // resting accent (button fill, focus ring)
  "color-accent-contrast", // text drawn on top of a solid accent
  "color-danger",
  "color-danger-contrast",
  "color-warn",
  "color-info", // a signal, not a verdict: notices, anticheat findings
  "color-blood", // first-blood highlight
  "color-success",
  "color-diff-add", // audit trail: the `after` side of a JSON diff
  "color-diff-del", // audit trail: the `before` side
  "color-overlay", // modal backdrop
  "radius",
  "shadow",
  "font-body",
  "font-mono",
] as const;

export type TokenKey = (typeof TOKEN_KEYS)[number];

// A complete set of values for the contract. Every theme provides exactly this.
export type Tokens = Record<TokenKey, string>;

// A partial set: the admin custom-override blob and any test fixture merge as this
// over a base theme.
export type TokenOverrides = Partial<Record<TokenKey, string>>;

// Keep only keys that belong to the contract, dropping anything an operator or an
// imported archive slipped in. An override is data from outside; it does not get to
// invent variables the stylesheet never reads.
export function sanitizeOverrides(input: Record<string, unknown> | null | undefined): TokenOverrides {
  if (!input) return {};
  const known = new Set<string>(TOKEN_KEYS);
  const out: TokenOverrides = {};
  for (const [key, value] of Object.entries(input)) {
    if (known.has(key) && typeof value === "string") out[key as TokenKey] = value;
  }
  return out;
}
