import type { ReactNode } from "react";
import { cx } from "./cx";
import { Button } from "./Button";

export type AlertTone = "info" | "success" | "warn" | "danger";

export interface AlertProps {
  tone?: AlertTone;
  title?: ReactNode;
  children?: ReactNode;
  /** Renders a dismiss button; the caller owns the resulting visibility state. */
  onDismiss?: () => void;
  /** Full-bleed, square-edged: the page-top variant. */
  banner?: boolean;
  className?: string;
}

const GLYPH: Record<AlertTone, string> = {
  info: "i",
  success: "✓",
  warn: "!",
  danger: "✕",
};

export function Alert({
  tone = "info",
  title,
  children,
  onDismiss,
  banner = false,
  className,
}: AlertProps) {
  return (
    <div
      // Danger is a failure the user must hear about now; the rest can wait for the
      // reading order. role=alert on everything makes a page of notices unusable.
      role={tone === "danger" ? "alert" : "status"}
      className={cx("ff-alert", `ff-alert--${tone}`, banner && "ff-alert--banner", className)}
    >
      <span className="ff-alert__icon" aria-hidden="true">
        {GLYPH[tone]}
      </span>
      <div className="ff-alert__body">
        {title !== undefined && <p className="ff-alert__title">{title}</p>}
        {children !== undefined && <div className="ff-alert__msg">{children}</div>}
      </div>
      {onDismiss && (
        <Button
          variant="ghost"
          size="sm"
          className="ff-alert__dismiss"
          onClick={onDismiss}
          aria-label="Dismiss"
        >
          ✕
        </Button>
      )}
    </div>
  );
}

/** The same alert, full width and square against the edges it touches. */
export function Banner(props: Omit<AlertProps, "banner">) {
  return <Alert {...props} banner />;
}
