import { useEffect, useState, type ReactNode } from "react";
import { isApiError } from "../api/client";
import { PolicyGate, denialOf, type Denial } from "../policy";
import { Alert, Button, formatAbsolute } from "../ui";
import { formatCountdown } from "./clock";

/** A wall clock that re-renders on a cadence. Only for screens that are counting something down. */
export function useNow(everyMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(id);
  }, [everyMs]);
  return now;
}

export function QueryError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const detail = isApiError(error)
    ? error.detail
    : error instanceof Error
      ? error.message
      : "The request did not complete.";

  return (
    <Alert tone="danger" title="Could not load this">
      <p>{detail}</p>
      <Button size="sm" onClick={onRetry}>
        Try again
      </Button>
    </Alert>
  );
}

export interface ScreenGateProps {
  /** The query's error, or null/undefined while it is fine. */
  error: unknown;
  onRetry: () => void;
  /** Override the notice for a denial the screen has something better to say about. */
  fallback?: (denial: Denial) => ReactNode;
  children: ReactNode;
}

/**
 * Splits a failed read into the two things it can be.
 *
 * A denial is an answer — the server telling us this viewer may not see this, in words the policy
 * table already knows how to render. Everything else is a fault, and a fault gets the server's
 * detail and a retry button. Collapsing the two would turn "the scoreboard is hidden" into a red
 * error box, which is the opposite of what it means.
 */
export function ScreenGate({ error, onRetry, fallback, children }: ScreenGateProps) {
  if (error === null || error === undefined) return <>{children}</>;
  if (denialOf(error) === null) return <QueryError error={error} onRetry={onRetry} />;
  return <PolicyGate error={error} fallback={fallback} />;
}

/**
 * The `ctf-not-started` denial, rendered as the thing a player actually wants: how long is left.
 *
 * It reloads itself the moment the clock runs out, so nobody has to guess when to hit refresh.
 */
export function Countdown({
  denial,
  start,
  onStarted,
}: {
  denial: Denial;
  start: Date | null;
  onStarted: () => void;
}) {
  const now = useNow(1000);
  const remaining = start === null ? null : start.getTime() - now;

  useEffect(() => {
    if (remaining !== null && remaining <= 0) onStarted();
  }, [remaining, onStarted]);

  return (
    <Alert tone="info" title={denial.treatment.title}>
      {start === null || remaining === null ? (
        <p>{denial.treatment.message}</p>
      ) : (
        <>
          <p className="ff-board-countdown">{formatCountdown(remaining)}</p>
          <p className="ff-muted">The board opens at {formatAbsolute(start)}.</p>
        </>
      )}
    </Alert>
  );
}
