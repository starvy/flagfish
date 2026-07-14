import type { ReactNode } from "react";
import { cx } from "../ui";

export interface AdminPageProps {
  title: string;
  description?: ReactNode;
  /** Buttons, filters — anything that acts on the whole screen. */
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}

export function AdminPage({ title, description, actions, children, className }: AdminPageProps) {
  return (
    <section className={cx("admin-page", className)}>
      <header className="admin-page__head">
        <div className="admin-page__titles">
          <h1 className="admin-page__title">{title}</h1>
          {description !== undefined && <p className="admin-page__desc">{description}</p>}
        </div>
        {actions !== undefined && <div className="admin-page__actions">{actions}</div>}
      </header>
      <div className="admin-page__body">{children}</div>
    </section>
  );
}

export type StatTone = "neutral" | "accent" | "warn" | "danger";

export interface StatProps {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  tone?: StatTone;
}

export function Stat({ label, value, hint, tone = "neutral" }: StatProps) {
  return (
    <div className="admin-stat">
      <span className="admin-stat__label">{label}</span>
      <span className={cx("admin-stat__value", tone !== "neutral" && `admin-stat__value--${tone}`)}>
        {value}
      </span>
      {hint !== undefined && <span className="admin-stat__hint">{hint}</span>}
    </div>
  );
}

export function StatGrid({ children }: { children: ReactNode }) {
  return <div className="admin-stats">{children}</div>;
}

/** Label/value pairs: the read-only half of every console screen. */
export function DefList({ children }: { children: ReactNode }) {
  return <dl className="admin-defs">{children}</dl>;
}

export function Def({ term, children }: { term: ReactNode; children: ReactNode }) {
  return (
    <>
      <dt>{term}</dt>
      <dd>{children}</dd>
    </>
  );
}
