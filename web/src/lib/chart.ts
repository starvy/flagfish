// Dependency-free chart maths. Screens draw plain SVG from these; nothing here touches React,
// the DOM or an axis library — which is what keeps the static binary small and this logic
// unit-testable on its own.

export interface Point {
  x: number;
  y: number;
}

/** Round a positive value up to a tidy axis maximum — 1, 2, 5 × 10ⁿ. Non-positive input → 1. */
export function niceMax(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  const normalized = value / magnitude;
  const step = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10;
  return step * magnitude;
}

export interface ProjectOptions {
  width: number;
  height: number;
  pad: number;
  /** Force the top of the y-axis; defaults to a nice ceiling over the data's max. */
  yMax?: number;
}

/**
 * Map data points into SVG screen coordinates inside a padded box, flipping y so a larger value
 * sits higher. A single point — or a series with no spread in x — lands on the horizontal centre
 * rather than collapsing onto the left edge. An empty series projects to nothing.
 */
export function project(data: readonly Point[], opts: ProjectOptions): Point[] {
  if (data.length === 0) return [];
  const { width, height, pad } = opts;
  const xs = data.map((d) => d.x);
  const xMin = Math.min(...xs);
  const xMax = Math.max(...xs);
  const yMax = opts.yMax ?? niceMax(Math.max(...data.map((d) => d.y)));
  const innerW = width - pad * 2;
  const innerH = height - pad * 2;

  const xAt = (x: number) =>
    xMax === xMin ? pad + innerW / 2 : pad + ((x - xMin) / (xMax - xMin)) * innerW;
  const yAt = (y: number) => pad + innerH - (yMax <= 0 ? 0 : (y / yMax) * innerH);

  return data.map((d) => ({ x: xAt(d.x), y: yAt(d.y) }));
}

function n(value: number): string {
  return (Math.round(value * 100) / 100).toString();
}

/**
 * An SVG path `d` through screen points. With `step`, the path holds flat then jumps — the shape
 * of a cumulative score, which stays level between solves and rises on each one.
 */
export function linePath(points: readonly Point[], step = false): string {
  if (points.length === 0) return "";
  let d = `M ${n(points[0].x)} ${n(points[0].y)}`;
  for (let i = 1; i < points.length; i++) {
    d += step ? ` H ${n(points[i].x)} V ${n(points[i].y)}` : ` L ${n(points[i].x)} ${n(points[i].y)}`;
  }
  return d;
}

/** The same path, closed down to a baseline and back — a filled area under the line. */
export function areaPath(points: readonly Point[], baselineY: number, step = false): string {
  if (points.length === 0) return "";
  const first = points[0];
  const last = points[points.length - 1];
  return `${linePath(points, step)} L ${n(last.x)} ${n(baselineY)} L ${n(first.x)} ${n(baselineY)} Z`;
}

/** Each value as a fraction of the largest — for a bar whose length reads against the top bar. */
export function fractionsOfMax(values: readonly number[]): number[] {
  const max = Math.max(0, ...values);
  return values.map((v) => (max === 0 ? 0 : Math.max(0, v) / max));
}

/** Each value as a share of the total — for a proportion/breakdown bar. Empty or all-zero → zeros. */
export function proportions(values: readonly number[]): number[] {
  const total = values.reduce((sum, v) => sum + Math.max(0, v), 0);
  return values.map((v) => (total === 0 ? 0 : Math.max(0, v) / total));
}
