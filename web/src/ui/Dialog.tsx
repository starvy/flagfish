import { useCallback, useEffect, useId, useRef, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { cx } from "./cx";
import { Button } from "./Button";

export type DialogSize = "sm" | "md" | "lg";

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  /** Buttons, right-aligned. Cancel first, the action last. */
  footer?: ReactNode;
  size?: DialogSize;
  /** What takes focus when the dialog opens. Defaults to the first tabbable node. */
  initialFocusRef?: RefObject<HTMLElement | null>;
  closeOnBackdrop?: boolean;
  className?: string;
}

const TABBABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = "md",
  initialFocusRef,
  closeOnBackdrop = true,
  className,
}: DialogProps) {
  const panel = useRef<HTMLDivElement>(null);
  const id = useId();
  const titleId = `${id}-title`;
  const descId = `${id}-desc`;

  const tabbables = useCallback(
    () => Array.from(panel.current?.querySelectorAll<HTMLElement>(TABBABLE) ?? []),
    [],
  );

  useEffect(() => {
    if (!open) return;

    // Whatever opened the dialog gets focus back when it closes — otherwise focus
    // falls to <body> and a keyboard user restarts from the top of the page.
    const opener = document.activeElement as HTMLElement | null;
    (initialFocusRef?.current ?? tabbables()[0] ?? panel.current)?.focus();

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== "Tab") return;

      // The trap: Tab off either end wraps to the other. Without it focus walks out
      // into the page behind the backdrop — inert to the eye, but not to Tab.
      const nodes = tabbables();
      if (nodes.length === 0) {
        e.preventDefault();
        return;
      }
      const first = nodes[0]!;
      const last = nodes[nodes.length - 1]!;
      const active = document.activeElement;

      if (e.shiftKey && (active === first || !panel.current?.contains(active))) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    };

    document.addEventListener("keydown", onKeyDown, true);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      document.body.style.overflow = prevOverflow;
      opener?.focus?.();
    };
  }, [open, onClose, initialFocusRef, tabbables]);

  if (!open) return null;

  return createPortal(
    <div
      className="ff-dialog__backdrop"
      // Only a press that lands on the backdrop closes. A drag that began on text
      // inside the panel and released out here is a selection, not a dismiss.
      onMouseDown={(e) => {
        if (closeOnBackdrop && e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description === undefined ? undefined : descId}
        tabIndex={-1}
        className={cx("ff-dialog", size !== "md" && `ff-dialog--${size}`, className)}
      >
        <div className="ff-dialog__head">
          <div>
            <h2 className="ff-dialog__title" id={titleId}>
              {title}
            </h2>
            {description !== undefined && (
              <p className="ff-dialog__desc" id={descId}>
                {description}
              </p>
            )}
          </div>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close dialog">
            ✕
          </Button>
        </div>

        <div className="ff-dialog__body">{children}</div>

        {footer !== undefined && <div className="ff-dialog__foot">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}
