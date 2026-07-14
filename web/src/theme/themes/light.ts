import type { Theme } from "../theme";

const mono = '"JetBrains Mono", "Fira Code", ui-monospace, "SF Mono", Menlo, Consolas, monospace';

// A light theme: paper background, ink text, the same green accent pulled darker so
// it holds contrast on white. Proves the contract carries across the light/dark line
// without any component knowing which side it is on.
export const light: Theme = {
  name: "light",
  label: "Paper (light)",
  colorScheme: "light",
  tokens: {
    "color-bg": "#f7f8fa",
    "color-surface": "#ffffff",
    "color-inset": "#eef1f5",
    "color-border": "#d3dae2",
    "color-border-strong": "#b3bfcc",
    "color-text": "#1a2430",
    "color-text-muted": "#5c6b7a",
    "color-accent": "#128a4e",
    "color-accent-dim": "#0f6f3f",
    "color-accent-contrast": "#ffffff",
    "color-danger": "#c8352f",
    "color-danger-contrast": "#ffffff",
    "color-warn": "#a5760a",
    "color-info": "#0b62c4",
    "color-blood": "#d81a52",
    "color-success": "#128a4e",
    "color-diff-add": "#128a4e",
    "color-diff-del": "#c8352f",
    "color-overlay": "rgba(26, 36, 48, 0.4)",
    radius: "6px",
    shadow: "0 8px 24px rgba(26, 36, 48, 0.15)",
    "font-body": mono,
    "font-mono": mono,
  },
};
