import { cx } from "./cx";

export type SpinnerSize = "sm" | "md" | "lg";

export interface SpinnerProps {
  size?: SpinnerSize;
  /** Announced to assistive tech; pass null inside a control that already labels itself. */
  label?: string | null;
  className?: string;
}

export function Spinner({ size = "md", label = "Loading", className }: SpinnerProps) {
  return (
    <span
      className={cx("ff-spinner", size !== "md" && `ff-spinner--${size}`, className)}
      role={label === null ? undefined : "status"}
      aria-hidden={label === null || undefined}
    >
      {label !== null && <span className="ff-sr-only">{label}</span>}
    </span>
  );
}
