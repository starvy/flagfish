// The server's denial vocabulary, verbatim. Every string here is emitted by the Go policy
// gate or by a raw middleware gate; nothing else is a reason. Keeping this list exact is what
// lets the UI switch on a denial instead of pattern-matching prose.
export const REASONS = [
  // policy gate
  "setup-incomplete",
  "banned",
  "team-banned",
  "password-change-required",
  "not-found",
  "auth-required",
  "authentication-required",
  "admins-only",
  "admin-required",
  "scores-hidden",
  "unverified",
  "incomplete-profile",
  "incomplete-team-profile",
  "team-required",
  "already-on-team",
  "team-creation-disabled",
  "ctf-not-started",
  "ctf-ended",
  "paused",
  "already-authed",

  // raw middleware gates
  "csrf",
  "rate-limited",
  "unavailable",
  "invalid-credentials",
  "body-too-large",
  "bad-request",
  "internal-error",
  "streaming-unsupported",
] as const;

export type Reason = (typeof REASONS)[number];

const REASON_SET: ReadonlySet<string> = new Set(REASONS);

export function isReason(value: string | null | undefined): value is Reason {
  return value !== null && value !== undefined && REASON_SET.has(value);
}

// The middleware gates advertise the reason as a URI; the Huma operations omit `type`
// entirely (it defaults to "about:blank") and put the reason in `detail` instead.
const TYPE_PREFIX = "urn:flagfish:error:";

export function reasonFromType(type: string | null | undefined): Reason | null {
  if (!type || !type.startsWith(TYPE_PREFIX)) return null;
  const tail = type.slice(TYPE_PREFIX.length);
  return isReason(tail) ? tail : null;
}
