/**
 * Can this browser draw the globe at all?
 *
 * Asked once, before the scene is built. A WebGL context is not something to find out about by
 * rendering a black rectangle: the answer decides whether the player gets the globe or gets told
 * why they are looking at the standard board instead.
 */
export type WebGLProbe = () => unknown;

function probeContext(): unknown {
  if (typeof document === "undefined") return null;
  const canvas = document.createElement("canvas");
  // `webgl` covers every browser that can run three.js; `experimental-webgl` is the old
  // spelling some locked-down builds still answer to.
  return canvas.getContext("webgl") ?? canvas.getContext("experimental-webgl");
}

/**
 * A probe that throws counts as "no": a browser with WebGL disabled by policy, or a headless
 * environment with no GPU at all, raises rather than returning null, and either way there is no
 * context to render into.
 */
export function supportsWebGL(probe: WebGLProbe = probeContext): boolean {
  try {
    return probe() != null;
  } catch {
    return false;
  }
}
