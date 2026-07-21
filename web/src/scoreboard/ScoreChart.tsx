import { useId } from "react";
import type { ScorePoint } from "../api/client";
import { EmptyState, formatAbsolute } from "../ui";
import { areaPath, linePath, niceMax, project } from "../lib/chart";

const WIDTH = 640;
const HEIGHT = 180;
const PAD = 6;

export interface ScoreChartProps {
  points: readonly ScorePoint[];
  /** Names the account in the accessible summary, e.g. "kittensa's". */
  subject?: string;
}

/**
 * A cumulative-score line for a public profile. Scoring is a step function — a total holds flat
 * between solves and jumps on each — so the line steps, and the fill under it reads as magnitude.
 *
 * The server already applies the freeze and the hidden/banned rule, handing the public an empty
 * series rather than a partial one, so an empty `points` is a real state to render plainly, not a
 * fault. The SVG is decorative; the data lives in an adjacent table for assistive tech.
 */
export function ScoreChart({ points, subject }: ScoreChartProps) {
  const captionId = useId();

  if (points.length === 0) {
    return (
      <EmptyState
        title="No scoring activity yet"
        description="This chart fills in once the first points land."
      />
    );
  }

  // Rise from zero at the first solve's instant, so the step's height is the score itself.
  const data = [
    { x: Date.parse(points[0].date), y: 0 },
    ...points.map((p) => ({ x: Date.parse(p.date), y: p.score })),
  ];
  const peak = Math.max(...points.map((p) => p.score));
  const yMax = niceMax(peak);
  const projected = project(data, { width: WIDTH, height: HEIGHT, pad: PAD, yMax });
  const baseline = HEIGHT - PAD;
  const last = projected[projected.length - 1];
  const final = points[points.length - 1];
  const who = subject === undefined || subject === "" ? "This account" : subject;

  return (
    <figure className="ff-scorechart" aria-labelledby={captionId}>
      <svg
        className="ff-scorechart__svg"
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        preserveAspectRatio="none"
        role="img"
        aria-labelledby={captionId}
      >
        <path className="ff-scorechart__area" d={areaPath(projected, baseline, true)} />
        <path className="ff-scorechart__line" d={linePath(projected, true)} />
        <circle className="ff-scorechart__head" cx={last.x} cy={last.y} r={3.5} />
      </svg>

      <figcaption id={captionId} className="ff-scorechart__cap">
        {who} reached <strong>{final.score}</strong> points over {points.length}{" "}
        {points.length === 1 ? "scoring event" : "scoring events"}.
      </figcaption>

      <table className="ff-sr-only">
        <caption>Score after each scoring event</caption>
        <thead>
          <tr>
            <th scope="col">When</th>
            <th scope="col">Change</th>
            <th scope="col">Score</th>
          </tr>
        </thead>
        <tbody>
          {points.map((p, i) => (
            <tr key={`${p.date}-${i}`}>
              <td>{formatAbsolute(new Date(p.date))}</td>
              <td>{p.delta >= 0 ? `+${p.delta}` : p.delta}</td>
              <td>{p.score}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}
