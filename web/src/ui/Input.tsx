import { forwardRef, type InputHTMLAttributes } from "react";
import { cx } from "./cx";

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  invalid?: boolean;
  /** Monospace: flags, tokens, hashes — anything read character by character. */
  mono?: boolean;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { invalid, mono, className, type = "text", ...rest },
  ref,
) {
  return (
    <input
      {...rest}
      ref={ref}
      type={type}
      className={cx("ff-input", mono && "ff-input--mono", className)}
      aria-invalid={invalid || undefined}
    />
  );
});
