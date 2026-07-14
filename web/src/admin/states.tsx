import type { ReactNode } from "react";
import { PolicyNotice, denialOf } from "../policy";
import { Alert, Button, Skeleton } from "../ui";
import { errorDetail } from "./errors";

export interface ErrorStateProps {
  error: unknown;
  onRetry?: () => void;
  title?: string;
}

/**
 * The error and denied states, which are the same branch seen from two sides: a policy denial
 * is the server telling us this screen is not ours, and it renders as the reason it named — a
 * retry button would be a lie. Anything else is a fault, and the operator gets the server's
 * detail plus a way to try again.
 */
export function ErrorState({ error, onRetry, title = "Could not load" }: ErrorStateProps) {
  const denial = denialOf(error);
  if (denial !== null) return <PolicyNotice denial={denial} />;

  return (
    <Alert tone="danger" title={title}>
      <p>{errorDetail(error)}</p>
      {onRetry && (
        <Button size="sm" variant="secondary" onClick={onRetry}>
          Retry
        </Button>
      )}
    </Alert>
  );
}

/** The loading state: the shape of the screen, not a spinner on an empty page. */
export function LoadingState({ rows = 4 }: { rows?: number }) {
  return (
    <div className="ff-stack" aria-busy="true" aria-live="polite">
      <Skeleton height="1.75rem" width="14rem" />
      <Skeleton lines={rows} height="3rem" />
    </div>
  );
}

/**
 * The four states in one place: an async screen renders exactly one of them and never falls
 * through a hole between two.
 */
export interface AsyncStateProps {
  loading: boolean;
  error: unknown;
  onRetry?: () => void;
  /** Rendered when the fetch succeeded but there is nothing to show. */
  isEmpty?: boolean;
  empty?: ReactNode;
  children: ReactNode;
  skeletonRows?: number;
}

export function AsyncState({
  loading,
  error,
  onRetry,
  isEmpty = false,
  empty,
  children,
  skeletonRows,
}: AsyncStateProps) {
  if (error !== null && error !== undefined) return <ErrorState error={error} onRetry={onRetry} />;
  if (loading) return <LoadingState rows={skeletonRows} />;
  if (isEmpty && empty !== undefined) return <>{empty}</>;
  return <>{children}</>;
}
