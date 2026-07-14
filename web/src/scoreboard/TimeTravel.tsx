import { useEffect, useRef, useState } from "react";
import { Button, formatAbsolute } from "../ui";

const MINUTE = 60_000;
// Long enough that dragging the handle across an event does not fire a request per pixel,
// short enough that letting go feels like it answered immediately.
const COMMIT_DELAY = 250;

export interface TimeTravelProps {
  /** The left edge of the scrub range: when the event opened. */
  start: Date;
  /** The right edge: now, or the end of the event once it is over. Handle at the edge = live. */
  latest: Date;
  /** The committed `as_of`, or undefined for the live board. */
  value: string | undefined;
  onChange: (asOf: string | undefined) => void;
}

/**
 * Scrubs the standings back through the event.
 *
 * The handle moves at once and the URL follows a beat later, so a drag is smooth and the address
 * bar still ends up as the shareable truth. Parking the handle at the right edge means "live" and
 * clears `as_of` rather than pinning the board to the instant the drag happened to end.
 */
export function TimeTravel({ start, latest, value, onChange }: TimeTravelProps) {
  const lo = start.getTime();
  const hi = Math.max(latest.getTime(), lo);
  const committed = value === undefined ? hi : new Date(value).getTime();

  const [position, setPosition] = useState(committed);
  const timer = useRef<number | undefined>(undefined);

  // The URL owns this control: a back button, or a link someone shared, has to move the handle.
  useEffect(() => setPosition(committed), [committed]);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const scrub = (next: number) => {
    setPosition(next);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      onChange(next >= hi ? undefined : new Date(next).toISOString());
    }, COMMIT_DELAY);
  };

  const live = position >= hi;
  const label = formatAbsolute(new Date(position));

  return (
    <div className="ff-board-control ff-board-travel">
      <label className="ff-board-control__label" htmlFor="ff-as-of">
        as of
      </label>
      <input
        id="ff-as-of"
        className="ff-board-travel__range"
        type="range"
        min={lo}
        max={hi}
        step={MINUTE}
        value={position}
        onChange={(e) => scrub(Number(e.target.value))}
        aria-valuetext={live ? "live" : label}
      />
      <output className="ff-board-travel__value" htmlFor="ff-as-of">
        {live ? "live" : label}
      </output>
      {!live && (
        <Button size="sm" variant="ghost" onClick={() => onChange(undefined)}>
          back to live
        </Button>
      )}
    </div>
  );
}
