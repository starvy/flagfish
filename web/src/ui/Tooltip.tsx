import { useId, useState, type ReactNode } from "react";
import { cx } from "./cx";

export interface TooltipProps {
  /** Supplementary text only. Anything essential belongs in the visible label. */
  content: ReactNode;
  placement?: "top" | "bottom";
  children: ReactNode;
  className?: string;
}

export function Tooltip({ content, placement = "top", children, className }: TooltipProps) {
  const id = useId();
  const [open, setOpen] = useState(false);

  return (
    <span
      className={cx("ff-tooltip", className)}
      // Focus opens it too: a keyboard user gets the same hint a mouse user does.
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
      onFocus={() => setOpen(true)}
      onBlur={() => setOpen(false)}
      onKeyDown={(e) => e.key === "Escape" && setOpen(false)}
      aria-describedby={open ? id : undefined}
    >
      {children}
      {open && (
        <span
          role="tooltip"
          id={id}
          className={cx("ff-tooltip__bubble", `ff-tooltip__bubble--${placement}`)}
        >
          {content}
        </span>
      )}
    </span>
  );
}
