import type { HTMLAttributes } from "react";
import { cx } from "./cx";

export type BadgeTone = "neutral" | "accent" | "success" | "warn" | "danger" | "info" | "blood";

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: BadgeTone;
}

export function Badge({ tone = "neutral", className, children, ...rest }: BadgeProps) {
  return (
    <span {...rest} className={cx("ff-badge", `ff-badge--${tone}`, className)}>
      {children}
    </span>
  );
}
