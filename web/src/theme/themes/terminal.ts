import type { Theme } from "../theme";

const mono = '"JetBrains Mono", "Fira Code", ui-monospace, "SF Mono", Menlo, Consolas, monospace';

// The default: the dark, green-on-black terminal look, expressed in tokens. The
// values here mirror the :root fallback in styles.css so a paint that lands before
// the theme is applied is already this theme, never an unstyled flash.
export const terminal: Theme = {
  name: "terminal",
  label: "Terminal (dark)",
  colorScheme: "dark",
  tokens: {
    "color-bg": "#0b0f14",
    "color-surface": "#111823",
    "color-inset": "#070a0e",
    "color-border": "#1e2a3a",
    "color-border-strong": "#2e4058",
    "color-text": "#c9d7e4",
    "color-text-muted": "#7d8fa3",
    "color-accent": "#3ddc84",
    "color-accent-dim": "#1f7a4d",
    "color-accent-contrast": "#04140b",
    "color-danger": "#ff5c57",
    "color-danger-contrast": "#14040a",
    "color-warn": "#f3c669",
    "color-info": "#5cb8ff",
    "color-blood": "#ff2e63",
    "color-success": "#3ddc84",
    "color-diff-add": "#3ddc84",
    "color-diff-del": "#ff5c57",
    "color-overlay": "rgba(4, 7, 10, 0.75)",
    radius: "6px",
    shadow: "0 8px 24px rgba(0, 0, 0, 0.45)",
    "font-body": mono,
    "font-mono": mono,
  },
};
