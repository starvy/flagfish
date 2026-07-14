import { forwardRef, type InputHTMLAttributes, type ReactNode } from "react";
import { cx } from "./cx";

export interface CheckboxProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "type"> {
  label?: ReactNode;
}

export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(function Checkbox(
  { label, className, ...rest },
  ref,
) {
  const input = (
    <input
      {...rest}
      ref={ref}
      type="checkbox"
      className={cx("ff-check__input", label === undefined && className)}
    />
  );

  if (label === undefined) return input;

  return (
    <label className={cx("ff-check", className)}>
      {input}
      <span>{label}</span>
    </label>
  );
});
