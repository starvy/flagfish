import { useEffect, type ReactNode } from "react";
import { useRouter } from "@tanstack/react-router";
import { denialOf, type Denial } from "./denial";

export interface PolicyGateProps {
  /** A query or mutation error. Anything that is not a policy denial is rethrown. */
  error: unknown;
  children?: ReactNode;
  /** Replace the default notice. */
  fallback?: (denial: Denial) => ReactNode;
  /** Render the denial in place instead of following the server's Location. */
  redirect?: boolean;
}

/**
 * The boundary every gated screen sits behind.
 *
 * A denial with a Location is a redirect the server chose — it arrives as a 403/404 carrying a
 * `Location` header, never as a 3xx, because fetch would have followed a 3xx and the SPA would
 * have learned nothing. Everything else renders where it stands.
 */
export function PolicyGate({ error, children, fallback, redirect = true }: PolicyGateProps) {
  const router = useRouter();
  const denial = error === null || error === undefined ? null : denialOf(error);
  const dest = redirect ? (denial?.location ?? null) : null;

  useEffect(() => {
    if (dest !== null) void router.navigate({ href: dest });
  }, [dest, router]);

  // Not a denial: this is a bug or a network fault, and it belongs to the error boundary.
  if (error !== null && error !== undefined && denial === null) throw error;
  if (denial === null) return <>{children}</>;
  if (dest !== null) return null;

  return <>{fallback ? fallback(denial) : <PolicyNotice denial={denial} />}</>;
}

export function PolicyNotice({ denial }: { denial: Denial }) {
  const { treatment, retryAfter } = denial;
  return (
    <div className={`notice notice-${treatment.tone}`} role="status">
      <h2 className="notice-title">{treatment.title}</h2>
      <p className="notice-message">{treatment.message}</p>
      {retryAfter !== null && <p className="notice-retry">Try again in {retryAfter}s.</p>}
      {treatment.action && (
        <a className="notice-action" href={treatment.action.to}>
          {treatment.action.label}
        </a>
      )}
    </div>
  );
}
