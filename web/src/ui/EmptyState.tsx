import type { ReactNode } from "react";
import { cx } from "./cx";

export interface EmptyStateProps {
  icon?: ReactNode;
  title: string;
  /** Say what would fill this list, not that it is empty — the user can see that. */
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
}

export function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div className={cx("ff-empty", className)}>
      {icon !== undefined && (
        <div className="ff-empty__icon" aria-hidden="true">
          {icon}
        </div>
      )}
      <p className="ff-empty__title">{title}</p>
      {description !== undefined && <p className="ff-empty__desc">{description}</p>}
      {action !== undefined && <div className="ff-empty__action">{action}</div>}
    </div>
  );
}
