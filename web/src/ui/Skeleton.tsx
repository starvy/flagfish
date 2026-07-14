import { cx } from "./cx";

export interface SkeletonProps {
  width?: string | number;
  height?: string | number;
  /** Repeat as N stacked bars — a paragraph or a list placeholder. */
  lines?: number;
  radius?: "sm" | "pill";
  className?: string;
}

export function Skeleton({ width, height, lines = 1, radius = "sm", className }: SkeletonProps) {
  const style = {
    width,
    height,
    borderRadius: radius === "pill" ? "var(--radius-pill)" : undefined,
  };

  return (
    <>
      {Array.from({ length: lines }, (_, i) => (
        <span
          key={i}
          className={cx("ff-skeleton", className)}
          style={style}
          // The container that swaps it out owns the announcement; a shimmer bar has
          // nothing to say.
          aria-hidden="true"
        />
      ))}
    </>
  );
}
