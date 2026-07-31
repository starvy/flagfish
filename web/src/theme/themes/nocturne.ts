import type { Theme } from "../theme";

const mono = '"JetBrains Mono", "Fira Code", ui-monospace, "SF Mono", Menlo, Consolas, monospace';

// The ops-center: near-black space, teal instrumentation, surfaces that barely lift off the
// background. The page background is the starfield's own black on purpose — under the globe view
// the scene and the chrome around it have to read as one surface, with no seam where the canvas
// ends.
export const nocturne: Theme = {
  name: "nocturne",
  label: "Nocturne (dark)",
  colorScheme: "dark",
  tokens: {
    "color-bg": "#01050c",
    "color-surface": "#08131d",
    "color-inset": "#00030a",
    "color-border": "#123240",
    "color-border-strong": "#1d5163",
    "color-text": "#cfe9f2",
    "color-text-muted": "#7fa3b3",
    "color-accent": "#22d3ee",
    "color-accent-dim": "#0e7490",
    "color-accent-contrast": "#00131a",
    "color-danger": "#ff5a68",
    "color-danger-contrast": "#14040a",
    "color-warn": "#ffb454",
    "color-info": "#4bc7ff",
    "color-blood": "#ff4d6d",
    "color-success": "#34d399",
    "color-diff-add": "#34d399",
    "color-diff-del": "#ff5a68",
    "color-overlay": "rgba(0, 3, 8, 0.8)",
    radius: "4px",
    shadow: "0 10px 30px rgba(0, 0, 0, 0.6)",
    "font-body": mono,
    "font-mono": mono,
  },
};
