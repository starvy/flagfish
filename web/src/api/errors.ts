import { isReason, reasonFromType, type Reason } from "../policy/reasons";

/** One field-level validation failure, as Huma reports it on a 422. */
export interface FieldError {
  /** Dotted path into the request, e.g. `body.password`. */
  location: string | null;
  message: string;
}

interface Problem {
  type?: string;
  title?: string;
  detail?: string;
  status?: number;
  errors?: Array<{ location?: string; message?: string }> | null;
}

export class ApiError extends Error {
  readonly status: number;
  readonly detail: string;
  readonly title: string | null;
  /** The server's denial reason, when the failure was a policy or middleware gate. */
  readonly reason: Reason | null;
  /** Where the server says to send the caller. A redirect is a 403/404 + Location, never a 3xx. */
  readonly location: string | null;
  /** Seconds to wait, on a 429. */
  readonly retryAfter: number | null;
  /** Per-field failures, on a 422. Empty otherwise. */
  readonly fieldErrors: readonly FieldError[];

  constructor(init: {
    status: number;
    detail: string;
    title?: string | null;
    reason?: Reason | null;
    location?: string | null;
    retryAfter?: number | null;
    fieldErrors?: readonly FieldError[];
  }) {
    super(init.detail);
    this.name = "ApiError";
    this.status = init.status;
    this.detail = init.detail;
    this.title = init.title ?? null;
    this.reason = init.reason ?? null;
    this.location = init.location ?? null;
    this.retryAfter = init.retryAfter ?? null;
    this.fieldErrors = init.fieldErrors ?? [];
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

// Retry-After is either delta-seconds or an HTTP-date.
function parseRetryAfter(header: string | null): number | null {
  if (header === null) return null;
  const seconds = Number(header);
  if (Number.isFinite(seconds)) return Math.max(0, Math.trunc(seconds));
  const when = Date.parse(header);
  if (Number.isNaN(when)) return null;
  return Math.max(0, Math.round((when - Date.now()) / 1000));
}

/**
 * Builds an ApiError from a failed response, consuming its body.
 *
 * The reason is read from `type` first and only then from `detail`: the middleware gates name
 * the reason in `type` but put prose in `detail` ("too many requests"), while the Huma
 * operations omit `type` and carry the reason in `detail`. Trying both, and accepting only a
 * string that is in the known vocabulary, is what lets one rule cover both shapes.
 */
export async function toApiError(res: Response): Promise<ApiError> {
  let problem: Problem = {};
  try {
    problem = (await res.json()) as Problem;
  } catch {
    // Not a problem document (a proxy's HTML 502, an empty body); the status line stands.
  }

  const detail = problem.detail ?? problem.title ?? `${res.status} ${res.statusText}`;
  const reason = reasonFromType(problem.type) ?? (isReason(problem.detail) ? problem.detail : null);

  return new ApiError({
    status: res.status,
    detail,
    title: problem.title ?? null,
    reason,
    location: res.headers.get("Location"),
    retryAfter: res.status === 429 ? parseRetryAfter(res.headers.get("Retry-After")) : null,
    fieldErrors: (problem.errors ?? []).map((e) => ({
      location: e.location ?? null,
      message: e.message ?? "",
    })),
  });
}
