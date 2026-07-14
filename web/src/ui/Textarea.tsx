import { forwardRef, type TextareaHTMLAttributes } from "react";
import { cx } from "./cx";

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
  mono?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { invalid, mono, className, rows = 6, ...rest },
  ref,
) {
  return (
    <textarea
      {...rest}
      ref={ref}
      rows={rows}
      className={cx("ff-textarea", mono && "ff-textarea--mono", className)}
      aria-invalid={invalid || undefined}
    />
  );
});
