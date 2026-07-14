import { useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "./cx";

export interface TabItem {
  id: string;
  label: ReactNode;
  content: ReactNode;
  disabled?: boolean;
}

export interface TabsProps {
  items: readonly TabItem[];
  /** Controlled: the selected tab id. Omit for uncontrolled. */
  value?: string;
  defaultValue?: string;
  onChange?: (id: string) => void;
  /** Names the tab list for screen readers. */
  label: string;
  className?: string;
}

export function Tabs({ items, value, defaultValue, onChange, label, className }: TabsProps) {
  const base = useId();
  const [internal, setInternal] = useState(defaultValue ?? items[0]?.id ?? "");
  const refs = useRef(new Map<string, HTMLButtonElement>());

  const selected = value ?? internal;
  const select = (id: string) => {
    if (value === undefined) setInternal(id);
    onChange?.(id);
  };

  const move = (e: KeyboardEvent<HTMLDivElement>) => {
    const step: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1 };
    const usable = items.filter((t) => !t.disabled);
    if (usable.length === 0) return;

    let next: string | undefined;
    if (e.key in step) {
      const at = usable.findIndex((t) => t.id === selected);
      next = usable[(at + step[e.key]! + usable.length) % usable.length]!.id;
    } else if (e.key === "Home") {
      next = usable[0]!.id;
    } else if (e.key === "End") {
      next = usable[usable.length - 1]!.id;
    }
    if (next === undefined) return;

    e.preventDefault();
    select(next);
    // Follow-focus: arrowing through a roving-tabindex tab list moves the selection
    // with the focus, which is what a screen reader user expects here.
    refs.current.get(next)?.focus();
  };

  const active = items.find((t) => t.id === selected);

  return (
    <div className={cx("ff-tabs", className)}>
      <div className="ff-tabs__list" role="tablist" aria-label={label} onKeyDown={move}>
        {items.map((t) => {
          const on = t.id === selected;
          return (
            <button
              key={t.id}
              type="button"
              role="tab"
              id={`${base}-tab-${t.id}`}
              className="ff-tabs__tab"
              aria-selected={on}
              aria-controls={`${base}-panel-${t.id}`}
              // Roving tabindex: one Tab stop for the whole list, arrows inside it.
              tabIndex={on ? 0 : -1}
              disabled={t.disabled}
              onClick={() => select(t.id)}
              ref={(el) => {
                if (el) refs.current.set(t.id, el);
                else refs.current.delete(t.id);
              }}
            >
              {t.label}
            </button>
          );
        })}
      </div>

      {active && (
        <div
          role="tabpanel"
          id={`${base}-panel-${active.id}`}
          aria-labelledby={`${base}-tab-${active.id}`}
          className="ff-tabs__panel"
          tabIndex={0}
        >
          {active.content}
        </div>
      )}
    </div>
  );
}
