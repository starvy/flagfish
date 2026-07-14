import type { CSSProperties } from "react";
import { TOKEN_KEYS, type TokenKey, type TokenOverrides, type Tokens } from "../theme/tokens";
import { Badge, Button, Input } from "../ui";

/** The tokens a preview should paint with: the base theme, with the operator's draft on top. */
export function mergeTokens(base: Tokens, overrides: TokenOverrides): Tokens {
  return { ...base, ...overrides };
}

/** Custom properties, scoped to one element — the whole trick behind previewing without repainting. */
export function tokenStyle(tokens: Tokens): CSSProperties {
  const style: Record<string, string> = {};
  for (const key of TOKEN_KEYS) style[`--${key}`] = tokens[key];
  return style as CSSProperties;
}

// The tokens that name a paintable colour; the rest are a radius, a shadow, a font stack.
const COLOUR = /^color-/;

export interface TokenEditorProps {
  base: Tokens;
  overrides: TokenOverrides;
  onChange: (next: TokenOverrides) => void;
  disabled?: boolean;
}

/**
 * The override editor. Every contract key is a row; a row with an empty box is not an override,
 * so clearing the box removes the key rather than storing an empty string the stylesheet would
 * then try to paint with.
 */
export function TokenEditor({ base, overrides, onChange, disabled }: TokenEditorProps) {
  const set = (key: TokenKey, raw: string) => {
    const next = { ...overrides };
    if (raw.trim() === "") delete next[key];
    else next[key] = raw;
    onChange(next);
  };

  return (
    <div className="admin-tokens">
      {TOKEN_KEYS.map((key) => {
        const override = overrides[key];
        const effective = override ?? base[key];
        return (
          <div
            key={key}
            className={override !== undefined ? "admin-token admin-token--set" : "admin-token"}
          >
            {COLOUR.test(key) ? (
              <span
                className="admin-token__swatch"
                style={{ background: effective }}
                aria-hidden="true"
              />
            ) : (
              <span />
            )}
            <label className="admin-token__key" htmlFor={`token-${key}`}>
              {key}
            </label>
            <Input
              id={`token-${key}`}
              className="admin-token__input"
              mono
              value={override ?? ""}
              placeholder={base[key]}
              disabled={disabled}
              onChange={(e) => set(key, e.currentTarget.value)}
              aria-label={`override ${key}`}
            />
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled || override === undefined}
              onClick={() => set(key, "")}
              aria-label={`reset ${key} to the theme default`}
            >
              reset
            </Button>
          </div>
        );
      })}
    </div>
  );
}

/**
 * The live preview: the same primitives the console and the game are built from, drawn with the
 * draft tokens. It is the only honest way to review an override — a swatch grid shows the colour
 * but not the contrast it lands on.
 */
export function ThemePreview({ tokens, themeLabel }: { tokens: Tokens; themeLabel: string }) {
  return (
    <div className="admin-preview" style={tokenStyle(tokens)}>
      <div className="admin-preview__panel">
        <p className="admin-preview__title">{themeLabel}</p>
        <p className="admin-preview__muted">
          Live preview — this is what a player sees once you save.
        </p>
      </div>
      <div className="admin-preview__row">
        <Button size="sm">primary</Button>
        <Button size="sm" variant="secondary">
          secondary
        </Button>
        <Button size="sm" variant="danger">
          danger
        </Button>
      </div>
      <div className="admin-preview__row">
        <Badge tone="accent">accent</Badge>
        <Badge tone="success">solved</Badge>
        <Badge tone="warn">frozen</Badge>
        <Badge tone="danger">banned</Badge>
        <Badge tone="info">signal</Badge>
        <Badge tone="blood">first blood</Badge>
      </div>
      <Input placeholder="flagfish{…}" mono readOnly />
    </div>
  );
}
