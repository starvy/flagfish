import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "./cx";

export interface CardProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  title?: ReactNode;
  /** Rendered on the right of the header row: buttons, a menu, a badge. */
  actions?: ReactNode;
  footer?: ReactNode;
  /** Drop the body padding — for a card whose body is a DataTable. */
  flush?: boolean;
}

export const Card = forwardRef<HTMLDivElement, CardProps>(function Card(
  { title, actions, footer, flush = false, className, children, ...rest },
  ref,
) {
  return (
    <div {...rest} ref={ref} className={cx("ff-card", className)}>
      {(title !== undefined || actions !== undefined) && (
        <div className="ff-card__head">
          {typeof title === "string" ? <h2 className="ff-card__title">{title}</h2> : title}
          {actions !== undefined && <div className="ff-row">{actions}</div>}
        </div>
      )}
      <div className={cx("ff-card__body", flush && "ff-card__body--flush")}>{children}</div>
      {footer !== undefined && <div className="ff-card__foot">{footer}</div>}
    </div>
  );
});
