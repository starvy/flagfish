import { describe, expect, it } from "vitest";
import { supportsWebGL } from "./webgl";

describe("supportsWebGL", () => {
  it("is true when the browser hands back a context", () => {
    expect(supportsWebGL(() => ({ drawingBufferWidth: 300 }))).toBe(true);
  });

  it("is false when there is no context", () => {
    expect(supportsWebGL(() => null)).toBe(false);
    expect(supportsWebGL(() => undefined)).toBe(false);
  });

  // WebGL disabled by policy raises rather than returning null, and a view that lets that
  // through renders a blank canvas instead of an explanation.
  it("is false when asking for a context throws", () => {
    expect(
      supportsWebGL(() => {
        throw new Error("WebGL is disabled");
      }),
    ).toBe(false);
  });

  it("answers without a document, so it is safe to call anywhere", () => {
    expect(supportsWebGL()).toBe(false);
  });
});
