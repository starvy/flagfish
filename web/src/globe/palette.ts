import type { CountryState } from "./countryStates";

/**
 * The scene's colours.
 *
 * Deliberately not the token contract. Tokens describe a document — surfaces, borders, text —
 * and a lit sphere in a starfield is not a document: these values have to hold against a black
 * background and a glowing atmosphere in every theme, and a light theme's "surface" would
 * disappear. Everything around the scene — the drawer, the panels, the buttons — is tokens, so
 * a colour theme still lands where a colour theme belongs.
 */
export const SCENE = {
  /** Transparent: the starfield behind the canvas is CSS, so it costs no texture. */
  background: "rgba(0,0,0,0)",
  atmosphere: "#4bc7ff",
  /** A country nobody put a challenge in. Present, but not inviting. */
  quiet: "#14202c",
  quietStroke: "#233648",
  /** Untouched: the accent, at rest. */
  open: "#1d7d9c",
  /** Something has been solved here. Brighter, because it is in play. */
  partial: "#22d3ee",
  /** Every challenge here is done. */
  captured: "#34d399",
  stroke: "#5eead4",
  pulse: "#22d3ee",
  /** First blood gets its own colour, the same one the board uses for it. */
  bloodPulse: "#ff4d6d",
} as const;

/** The cap colour for one country's polygon. */
export function capColorOf(state: CountryState | undefined): string {
  if (state === undefined) return SCENE.quiet;
  switch (state.capture) {
    case "open":
      return SCENE.open;
    case "partial":
      return SCENE.partial;
    case "captured":
      return SCENE.captured;
  }
}

export function strokeColorOf(state: CountryState | undefined): string {
  return state === undefined ? SCENE.quietStroke : SCENE.stroke;
}

/** Countries in play stand slightly proud of the sphere, so a pin has something to sit on. */
export function altitudeOf(state: CountryState | undefined): number {
  return state === undefined ? 0.006 : 0.018;
}
