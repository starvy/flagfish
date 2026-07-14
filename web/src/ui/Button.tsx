import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { cx } from "./cx";
import { Spinner } from "./Spinner";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Disables the button and swaps the label for a spinner without changing its width. */
  loading?: boolean;
  fullWidth?: boolean;
  iconStart?: ReactNode;
  iconEnd?: ReactNode;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = "secondary",
    size = "md",
    loading = false,
    fullWidth = false,
    iconStart,
    iconEnd,
    className,
    children,
    disabled,
    type = "button",
    ...rest
  },
  ref,
) {
  return (
    <button
      {...rest}
      ref={ref}
      // A button inside a form defaults to submit; make every intent explicit instead.
      type={type}
      className={cx(
        "ff-btn",
        `ff-btn--${variant}`,
        size !== "md" && `ff-btn--${size}`,
        fullWidth && "ff-btn--block",
        loading && "ff-btn--loading",
        className,
      )}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
    >
      <span className="ff-btn__label">
        {iconStart}
        {children}
        {iconEnd}
      </span>
      {loading && (
        <span className="ff-btn__spinner">
          <Spinner size="sm" label="Working" />
        </span>
      )}
    </button>
  );
});
