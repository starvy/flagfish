import { useEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "./Button";
import { Dialog } from "./Dialog";
import { Field } from "./Form";
import { Input } from "./Input";

export interface ConfirmDestructiveProps {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  /** The exact string the operator must type. Confirm stays disabled until it matches. */
  resourceName: string;
  /** What kind of thing it is: "user", "challenge", "team". Used in the copy. */
  resourceKind?: string;
  title?: string;
  /** What this does, and what it cannot undo. */
  description?: ReactNode;
  confirmLabel?: string;
  /** The delete is in flight. */
  busy?: boolean;
  children?: ReactNode;
}

export function ConfirmDestructive({
  open,
  onClose,
  onConfirm,
  resourceName,
  resourceKind = "resource",
  title,
  description,
  confirmLabel = "Delete",
  busy = false,
  children,
}: ConfirmDestructiveProps) {
  const [typed, setTyped] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  // A reopened dialog must not still hold the last confirmation; that would turn the
  // second delete into a single click.
  useEffect(() => {
    if (open) setTyped("");
  }, [open]);

  const armed = typed === resourceName && !busy;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="sm"
      title={title ?? `Delete ${resourceKind}`}
      description={description}
      initialFocusRef={inputRef}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="danger" onClick={onConfirm} disabled={!armed} loading={busy}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      {children}
      <p className="ff-confirm__prompt">
        This cannot be undone. Type <span className="ff-confirm__name">{resourceName}</span> to
        confirm.
      </p>
      <Field name="confirm" label={`Name of the ${resourceKind}`}>
        <Input
          ref={inputRef}
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && armed) onConfirm();
          }}
          mono
          autoComplete="off"
          spellCheck={false}
          placeholder={resourceName}
        />
      </Field>
    </Dialog>
  );
}
