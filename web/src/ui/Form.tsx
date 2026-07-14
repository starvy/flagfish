import {
  Children,
  cloneElement,
  createContext,
  isValidElement,
  useContext,
  useId,
  type FormEvent,
  type FormHTMLAttributes,
  type ReactElement,
  type ReactNode,
} from "react";
import { cx } from "./cx";
import { Alert } from "./Alert";

/** Field name -> message. Produced by `fieldErrors()` from a 422 problem document. */
export type FieldErrors = Record<string, string>;

interface FormCtx {
  errors: FieldErrors;
  formId: string;
}

const Ctx = createContext<FormCtx | null>(null);

export interface FormProps extends Omit<FormHTMLAttributes<HTMLFormElement>, "onSubmit"> {
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  /** Per-field messages; a Field picks up its own by `name`. */
  errors?: FieldErrors;
  /** The error that belongs to no field: "wrong password", "instance is locked". */
  error?: ReactNode;
  /** Buttons. Rendered below the fields, outside the field flow. */
  footer?: ReactNode;
}

export function Form({ onSubmit, errors, error, footer, className, children, ...rest }: FormProps) {
  const formId = useId();

  return (
    <Ctx.Provider value={{ errors: errors ?? {}, formId }}>
      <form {...rest} onSubmit={onSubmit} className={cx("ff-form", className)} noValidate>
        {error !== undefined && error !== null && (
          <Alert tone="danger" title="Could not save">
            {error}
          </Alert>
        )}
        {children}
        {footer !== undefined && <div className="ff-form__footer">{footer}</div>}
      </form>
    </Ctx.Provider>
  );
}

/** What a Field hands its control so the label, hint and error are wired to it. */
export interface FieldControlProps {
  id: string;
  name: string;
  "aria-describedby": string | undefined;
  "aria-invalid": true | undefined;
  required?: boolean;
}

export interface FieldProps {
  /** Matches the key in the Form's `errors` map and the control's `name`. */
  name: string;
  label: ReactNode;
  hint?: ReactNode;
  /** Overrides whatever the Form's error map says for this field. */
  error?: string;
  required?: boolean;
  /**
   * A control, or a function receiving the wiring. A single element child is cloned
   * with the wiring props, never overwriting one the caller set; anything more
   * complex should take the function form and place them by hand.
   */
  children: ReactNode | ((control: FieldControlProps) => ReactNode);
  className?: string;
}

export function Field({ name, label, hint, error, required, children, className }: FieldProps) {
  const ctx = useContext(Ctx);
  const fallback = useId();
  const base = `${ctx?.formId ?? fallback}-${name}`;
  const message = error ?? ctx?.errors[name];

  const hintId = hint !== undefined ? `${base}-hint` : undefined;
  const errorId = message !== undefined ? `${base}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(" ") || undefined;

  const control: FieldControlProps = {
    id: base,
    name,
    "aria-describedby": describedBy,
    "aria-invalid": message !== undefined ? true : undefined,
    required,
  };

  return (
    <div className={cx("ff-field", className)}>
      <label className="ff-field__label" htmlFor={base}>
        {label}
        {required && (
          <span className="ff-field__req" aria-hidden="true">
            *
          </span>
        )}
      </label>
      {typeof children === "function" ? children(control) : wire(children, control)}
      {hint !== undefined && (
        <span className="ff-field__hint" id={hintId}>
          {hint}
        </span>
      )}
      {message !== undefined && (
        <span className="ff-field__error" id={errorId}>
          {message}
        </span>
      )}
    </div>
  );
}

// Push the wiring onto a single element child, never clobbering a prop the caller set
// deliberately. Anything else passes through untouched — that caller wants the
// function form.
function wire(children: ReactNode, control: FieldControlProps): ReactNode {
  const only = Children.count(children) === 1 ? Children.only(children) : null;
  if (!only || !isValidElement(only)) return children;

  const own = only.props as Record<string, unknown>;
  const next: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(control)) {
    if (own[key] === undefined && value !== undefined) next[key] = value;
  }
  return cloneElement(only as ReactElement<Record<string, unknown>>, next);
}

/** The shape Huma puts in an RFC 7807 problem document on a 422. */
interface ProblemDetail {
  location?: string;
  message?: string;
}

/**
 * Map a 422 problem document's `errors` onto field names.
 *
 * `location` arrives dotted and prefixed by where it was found — `body.email`,
 * `body.hints[2].cost` — so the leaf, minus any index, is the field name a form
 * knows. Anything without a usable location lands under `_`, which no Field claims:
 * show those as the form-level error rather than dropping them on the floor.
 */
export function fieldErrors(problem: unknown): FieldErrors {
  const list = (problem as { errors?: unknown } | null | undefined)?.errors;
  if (!Array.isArray(list)) return {};

  const out: FieldErrors = {};
  for (const raw of list as ProblemDetail[]) {
    if (typeof raw?.message !== "string") continue;
    const loc = typeof raw.location === "string" ? raw.location : "";
    const leaf = loc.split(".").pop()?.replace(/\[\d+\]$/, "") ?? "";
    const key = leaf === "" || leaf === "body" ? "_" : leaf;
    // First message wins: a field with two complaints shows the one the server led with.
    if (!(key in out)) out[key] = raw.message;
  }
  return out;
}
