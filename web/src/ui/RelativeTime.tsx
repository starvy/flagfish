import { useEffect, useState } from "react";
import { cx } from "./cx";
import { formatAbsolute, formatRelative, relativeTickMs } from "./timeFormat";

export interface RelativeTimeProps {
  /** An RFC 3339 string from the API, or a Date. */
  value: string | Date;
  /** Re-render as the label goes stale. Cheap: the interval widens with the age. */
  live?: boolean;
  className?: string;
}

export function RelativeTime({ value, live = true, className }: RelativeTimeProps) {
  const at = typeof value === "string" ? new Date(value) : value;
  const valid = !Number.isNaN(at.getTime());
  const instant = valid ? at.getTime() : 0;
  const [, tick] = useState(0);

  useEffect(() => {
    if (!live || !valid) return;
    const id = setInterval(() => tick((n) => n + 1), relativeTickMs(new Date(instant)));
    return () => clearInterval(id);
    // The instant is the identity: a fresh Date for the same moment must not re-arm.
  }, [live, valid, instant]);

  if (!valid) return <span className={cx("ff-time", className)}>—</span>;

  return (
    <time
      dateTime={at.toISOString()}
      title={formatAbsolute(at)}
      className={cx("ff-time", className)}
    >
      {formatRelative(at)}
    </time>
  );
}
