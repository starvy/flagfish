import { forwardRef, type SelectHTMLAttributes } from "react";
import { cx } from "./cx";

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  invalid?: boolean;
  /** Convenience for the common case; `children` still works for grouped options. */
  options?: readonly SelectOption[];
  placeholder?: string;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { invalid, options, placeholder, className, children, ...rest },
  ref,
) {
  return (
    // The wrapper exists only to hang the chevron on; the native select keeps its own
    // popup, keyboard behaviour and mobile picker.
    <span className="ff-select-wrap">
      <select
        {...rest}
        ref={ref}
        className={cx("ff-select", className)}
        aria-invalid={invalid || undefined}
      >
        {placeholder !== undefined && (
          <option value="" disabled>
            {placeholder}
          </option>
        )}
        {options?.map((o) => (
          <option key={o.value} value={o.value} disabled={o.disabled}>
            {o.label}
          </option>
        ))}
        {children}
      </select>
    </span>
  );
});
