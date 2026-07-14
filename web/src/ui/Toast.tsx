import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { cx } from "./cx";
import { Button } from "./Button";

export type ToastTone = "success" | "error" | "warn" | "info";

export interface ToastOptions {
  tone?: ToastTone;
  title?: string;
  message?: ReactNode;
  /** Milliseconds on screen; 0 pins it until dismissed. */
  duration?: number;
  action?: ReactNode;
}

export interface Toast extends ToastOptions {
  id: number;
  tone: ToastTone;
}

export interface ToastApi {
  toast: (opts: ToastOptions) => number;
  success: (title: string, message?: ReactNode) => number;
  error: (title: string, message?: ReactNode) => number;
  warn: (title: string, message?: ReactNode) => number;
  info: (title: string, message?: ReactNode) => number;
  dismiss: (id: number) => void;
  dismissAll: () => void;
}

const Ctx = createContext<ToastApi | null>(null);

export function useToast(): ToastApi {
  const api = useContext(Ctx);
  if (!api) throw new Error("useToast must be used inside a <ToastProvider>");
  return api;
}

export interface ToastProviderProps {
  children: ReactNode;
  /** The oldest is dropped once the stack is full. */
  max?: number;
  defaultDuration?: number;
}

const GLYPH: Record<ToastTone, string> = {
  success: "✓",
  error: "✕",
  warn: "!",
  info: "i",
};

export function ToastProvider({ children, max = 4, defaultDuration = 5000 }: ToastProviderProps) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const seq = useRef(0);

  const dismiss = useCallback((id: number) => {
    setToasts((list) => list.filter((t) => t.id !== id));
  }, []);

  const api = useMemo<ToastApi>(() => {
    const toast = (opts: ToastOptions): number => {
      const id = ++seq.current;
      const next: Toast = { duration: defaultDuration, ...opts, tone: opts.tone ?? "info", id };
      setToasts((list) => [...list, next].slice(-max));
      return id;
    };
    const shorthand =
      (tone: ToastTone) =>
      (title: string, message?: ReactNode): number =>
        toast({ tone, title, message });

    return {
      toast,
      success: shorthand("success"),
      // A failure the user has to act on outlives the default dwell time.
      error: (title, message) => toast({ tone: "error", title, message, duration: 8000 }),
      warn: shorthand("warn"),
      info: shorthand("info"),
      dismiss,
      dismissAll: () => setToasts([]),
    };
  }, [defaultDuration, max, dismiss]);

  return (
    <Ctx.Provider value={api}>
      {children}
      <ToastRegion toasts={toasts} onDismiss={dismiss} />
    </Ctx.Provider>
  );
}

function ToastRegion({ toasts, onDismiss }: { toasts: Toast[]; onDismiss: (id: number) => void }) {
  if (typeof document === "undefined") return null;

  return createPortal(
    // polite, not assertive: a save confirmation must not cut off whatever is being
    // read to the user. The region stays mounted so additions get announced.
    <div className="ff-toast-region" role="status" aria-live="polite" aria-atomic="false">
      {toasts.map((t) => (
        <ToastCard key={t.id} toast={t} onDismiss={onDismiss} />
      ))}
    </div>,
    document.body,
  );
}

function ToastCard({ toast, onDismiss }: { toast: Toast; onDismiss: (id: number) => void }) {
  const [paused, setPaused] = useState(false);
  const { id, duration } = toast;

  useEffect(() => {
    if (!duration || paused) return;
    const timer = setTimeout(() => onDismiss(id), duration);
    return () => clearTimeout(timer);
    // Leaving a hover restarts the clock rather than resuming it: a toast the user
    // just looked at should not vanish the moment they look away.
  }, [id, duration, paused, onDismiss]);

  return (
    <div
      className={cx("ff-toast", `ff-toast--${toast.tone}`)}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      <span className="ff-toast__icon" aria-hidden="true">
        {GLYPH[toast.tone]}
      </span>
      <div className="ff-toast__body">
        {toast.title !== undefined && <p className="ff-toast__title">{toast.title}</p>}
        {toast.message !== undefined && <div className="ff-toast__msg">{toast.message}</div>}
        {toast.action}
      </div>
      <Button variant="ghost" size="sm" aria-label="Dismiss" onClick={() => onDismiss(toast.id)}>
        ✕
      </Button>
    </div>
  );
}
