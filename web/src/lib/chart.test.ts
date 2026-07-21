import { describe, expect, it } from "vitest";
import { areaPath, axisTicks, fractionsOfMax, linePath, niceMax, project, proportions } from "./chart";

describe("niceMax", () => {
  it.each([
    [0, 1],
    [-5, 1],
    [1, 1],
    [3, 5],
    [7, 10],
    [42, 50],
    [128, 200],
    [999, 1000],
  ])("rounds %i up to %i", (input, want) => {
    expect(niceMax(input)).toBe(want);
  });

  it("never returns less than the input", () => {
    for (const v of [11, 23, 60, 305, 1234]) expect(niceMax(v)).toBeGreaterThanOrEqual(v);
  });
});

describe("axisTicks", () => {
  it("labels zero, the midpoint and the max", () => {
    expect(axisTicks(500)).toEqual([0, 250, 500]);
    expect(axisTicks(1000)).toEqual([0, 500, 1000]);
  });

  it("collapses a tiny axis to whole numbers instead of a fractional midpoint", () => {
    expect(axisTicks(1)).toEqual([0, 1]);
    expect(axisTicks(2)).toEqual([0, 1, 2]);
  });

  it("always spans from zero to the max", () => {
    for (const yMax of [1, 50, 200, 999]) {
      const ticks = axisTicks(yMax);
      expect(ticks[0]).toBe(0);
      expect(ticks[ticks.length - 1]).toBe(Math.round(yMax));
    }
  });
});

describe("project", () => {
  const box = { width: 100, height: 100, pad: 0 };

  it("projects an empty series to nothing", () => {
    expect(project([], box)).toEqual([]);
  });

  it("flips y so a larger value sits higher (smaller screen-y)", () => {
    const [low, high] = project(
      [
        { x: 0, y: 0 },
        { x: 1, y: 100 },
      ],
      { ...box, yMax: 100 },
    );
    expect(low.y).toBeGreaterThan(high.y);
    expect(high.y).toBe(0); // the max value pins to the top
    expect(low.y).toBe(100); // zero pins to the bottom
  });

  it("centres a single point instead of pinning it to the left edge", () => {
    const [only] = project([{ x: 5, y: 5 }], box);
    expect(only.x).toBe(50);
  });

  it("centres a series with no spread in x", () => {
    const pts = project(
      [
        { x: 7, y: 1 },
        { x: 7, y: 2 },
      ],
      box,
    );
    expect(pts.every((p) => p.x === 50)).toBe(true);
  });

  it("respects padding", () => {
    const pts = project(
      [
        { x: 0, y: 0 },
        { x: 1, y: 10 },
      ],
      { width: 100, height: 100, pad: 10, yMax: 10 },
    );
    expect(pts[0].x).toBe(10);
    expect(pts[1].x).toBe(90);
  });
});

describe("linePath", () => {
  const pts = [
    { x: 0, y: 0 },
    { x: 10, y: 20 },
  ];

  it("draws a straight polyline by default", () => {
    expect(linePath(pts)).toBe("M 0 0 L 10 20");
  });

  it("draws a step (hold then jump) when asked", () => {
    expect(linePath(pts, true)).toBe("M 0 0 H 10 V 20");
  });

  it("is empty for no points", () => {
    expect(linePath([])).toBe("");
  });
});

describe("areaPath", () => {
  it("closes the line down to a baseline and back", () => {
    const d = areaPath(
      [
        { x: 0, y: 5 },
        { x: 10, y: 5 },
      ],
      100,
    );
    expect(d).toBe("M 0 5 L 10 5 L 10 100 L 0 100 Z");
  });

  it("is empty for no points", () => {
    expect(areaPath([], 100)).toBe("");
  });
});

describe("fractionsOfMax", () => {
  it("scales each value against the largest", () => {
    expect(fractionsOfMax([5, 10, 0])).toEqual([0.5, 1, 0]);
  });

  it("returns zeros when the max is zero", () => {
    expect(fractionsOfMax([0, 0])).toEqual([0, 0]);
  });
});

describe("proportions", () => {
  it("returns each value's share of the total", () => {
    expect(proportions([1, 3])).toEqual([0.25, 0.75]);
  });

  it("returns zeros for an empty or all-zero series", () => {
    expect(proportions([])).toEqual([]);
    expect(proportions([0, 0])).toEqual([0, 0]);
  });
});
