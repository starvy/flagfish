import type { Theme } from "../theme";

const mono = '"JetBrains Mono", "Fira Code", ui-monospace, "SF Mono", Menlo, Consolas, monospace';

// A distinctly different dark theme: warm amber phosphor on near-black, the old CRT
// look. Same contract, a completely different feel — the interchangeability proof.
export const amber: Theme = {
  name: "amber",
  label: "Amber (CRT)",
  colorScheme: "dark",
  tokens: {
    "color-bg": "#140f05",
    "color-surface": "#1e1608",
    "color-inset": "#0d0a03",
    "color-border": "#3a2c10",
    "color-border-strong": "#5a4418",
    "color-text": "#ffcf6b",
    "color-text-muted": "#b08a3e",
    "color-accent": "#ffb020",
    "color-accent-dim": "#8a5e0f",
    "color-accent-contrast": "#140f05",
    "color-danger": "#ff6a3d",
    "color-danger-contrast": "#140f05",
    "color-warn": "#ffd24a",
    "color-blood": "#ff3b30",
    "color-success": "#ffb020",
    "color-overlay": "rgba(10, 6, 0, 0.78)",
    radius: "6px",
    shadow: "0 8px 24px rgba(0, 0, 0, 0.55)",
    "font-body": mono,
    "font-mono": mono,
  },
};
