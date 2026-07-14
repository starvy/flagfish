const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * A compact "3m ago" / "in 2h" label. Deliberately coarse: to the second is noise on
 * a scoreboard, and the exact instant is one hover away in the `title`.
 */
export function formatRelative(at: Date, now: Date = new Date()): string {
  const delta = at.getTime() - now.getTime();
  const abs = Math.abs(delta);

  if (abs < 45_000) return "just now";

  const [value, unit] =
    abs < HOUR
      ? [Math.round(abs / MINUTE), "m"]
      : abs < DAY
        ? [Math.round(abs / HOUR), "h"]
        : abs < 30 * DAY
          ? [Math.round(abs / DAY), "d"]
          : [Math.round(abs / (30 * DAY)), "mo"];

  return delta < 0 ? `${value}${unit} ago` : `in ${value}${unit}`;
}

/**
 * How long until the label could change. A timestamp minutes old needs a tick a
 * minute; one days old needs one an hour. Keeps a table of 100 rows off a 1s timer.
 */
export function relativeTickMs(at: Date, now: Date = new Date()): number {
  const abs = Math.abs(at.getTime() - now.getTime());
  if (abs < HOUR) return 15_000;
  if (abs < DAY) return MINUTE;
  return HOUR;
}

/** Full, unambiguous, locale-formatted — what goes in `title`. */
export function formatAbsolute(at: Date): string {
  return at.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "medium" });
}
