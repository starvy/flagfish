import type { ReactNode } from "react";
import { Field, cx, type FieldControlProps } from "../ui";
import type { TriMode, TriValue } from "./tristate";

const MODES: ReadonlyArray<{ mode: TriMode; label: string }> = [
  { mode: "keep", label: "keep" },
  { mode: "clear", label: "clear" },
  { mode: "set", label: "set" },
];

export interface TriStateFieldProps {
  /** Matches the request key, so a 422 on `freeze` lands on the freeze control. */
  name: string;
  label: ReactNode;
  hint?: ReactNode;
  value: TriValue;
  onChange: (next: TriValue) => void;
  /** What the server has today, rendered as the consequence of `keep`. */
  current: ReactNode;
  /** What `clear` costs, in the operator's terms: "the event will have no freeze". */
  clearNote: ReactNode;
  error?: string;
  /** The editor shown under `set`. Wired to the label and to the error message. */
  children: (control: FieldControlProps, value: string, onValue: (raw: string) => void) => ReactNode;
}

/**
 * One `keep | clear | set` field.
 *
 * The three buttons are the write: there is no way to reach `clear` by emptying a box, and no
 * way to reach `set` without typing something, which is what makes an untouched field provably
 * an omitted key.
 */
export function TriStateField({
  name,
  label,
  hint,
  value,
  onChange,
  current,
  clearNote,
  error,
  children,
}: TriStateFieldProps) {
  return (
    <Field name={name} label={label} hint={hint} error={error}>
      {(control) => (
        <div className="admin-tri">
          <div className="admin-tri__modes" role="group" aria-label={`${name}: keep, clear or set`}>
            {MODES.map(({ mode, label: modeLabel }, i) => (
              <label
                key={mode}
                className={cx("admin-tri__mode", value.mode === mode && "admin-tri__mode--on")}
              >
                <input
                  type="radio"
                  // The Field's label points at the first radio: the mode is the field.
                  id={i === 0 ? control.id : undefined}
                  name={`${control.id}-mode`}
                  checked={value.mode === mode}
                  onChange={() => onChange({ ...value, mode })}
                  aria-describedby={control["aria-describedby"]}
                />
                {modeLabel}
              </label>
            ))}
          </div>

          {value.mode === "set" && (
            <div className="admin-tri__control">
              {children(
                { ...control, id: `${control.id}-value` },
                value.value,
                (raw) => onChange({ mode: "set", value: raw }),
              )}
            </div>
          )}

          {value.mode === "keep" && <p className="admin-tri__note">Unchanged — currently {current}.</p>}

          {value.mode === "clear" && (
            <p className="admin-tri__note admin-tri__note--clear">{clearNote}</p>
          )}
        </div>
      )}
    </Field>
  );
}
