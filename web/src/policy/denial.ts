import { ApiError } from "../api/errors";
import type { Reason } from "./reasons";
import { TREATMENTS, type Treatment } from "./table";

export interface Denial {
  /** Null when the server denied without naming a reason — an L2 resource guard, say. */
  reason: Reason | null;
  status: number;
  /** The server's own words. More specific than the table's copy when it says anything. */
  detail: string;
  /** Set by the policy gate when the denial has somewhere to send you. Never a 3xx. */
  location: string | null;
  retryAfter: number | null;
  treatment: Treatment;
}

// The statuses the gates actually deny with. A 402/409 from a hint unlock is a UI state the
// screen owns, not a gate, so it is deliberately absent: it must not be swallowed here.
const GATE_STATUSES = new Set([401, 403, 404, 429, 503]);

// A denial the server did not name still has to render as something. The status is the only
// thing left to key on, and the server's detail carries the specifics.
function fallback(status: number, detail: string): Treatment {
  switch (status) {
    case 401:
      return TREATMENTS["invalid-credentials"];
    case 404:
      return { ...TREATMENTS["not-found"], message: detail };
    case 429:
      return TREATMENTS["rate-limited"];
    case 503:
      return { ...TREATMENTS.unavailable, message: detail };
    default:
      return { title: "Not allowed", message: detail, tone: "warning", transient: false, action: null };
  }
}

/**
 * Reads an error as a policy denial, or returns null if it is not one.
 *
 * Null means "this is not the policy layer's problem" — the caller rethrows and lets the error
 * boundary have it. Swallowing an unknown error here would turn a bug into a polite message,
 * which is the one thing this layer must not do.
 */
export function denialOf(error: unknown): Denial | null {
  if (!(error instanceof ApiError)) return null;
  if (error.reason === null && !GATE_STATUSES.has(error.status)) return null;

  return {
    reason: error.reason,
    status: error.status,
    detail: error.detail,
    location: error.location,
    retryAfter: error.retryAfter,
    treatment: error.reason === null ? fallback(error.status, error.detail) : TREATMENTS[error.reason],
  };
}
