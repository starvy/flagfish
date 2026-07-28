import type { ChallengeDetail, ChallengeListItem } from "../api/client";

export type BoardChallenge = ChallengeListItem;
export type ChallengeFile = NonNullable<ChallengeDetail["files"]>[number];
export type ChallengeHint = NonNullable<ChallengeDetail["hints"]>[number];

export type Challenge = ChallengeDetail & {
  /**
   * Not on the wire. The server counts wrong answers under a lock to enforce `max_attempts`, but
   * it never reports the tally, so the budget renders as a total until it does. Counting what this
   * tab has submitted would be a guess — the cap is all-time and per-account, and a player told
   * "3 of 5 used" when the server thinks 5 is a player who is lied to at the worst moment.
   */
  attempts_used?: number;
};

/**
 * Free-form operator metadata a view may place a challenge by. Always present on the wire, `{}`
 * when there is none and when the challenge is locked or masked — so a view that keys off an
 * annotation cannot be used to learn something about a challenge the player may not see.
 */
export type Annotations = Readonly<Record<string, string>>;

export function annotationsOf(challenge: BoardChallenge | Challenge): Annotations {
  // The schema says always present. A server older than annotations does not, and a board that
  // throws is a worse answer than a board with nothing placed on it.
  return challenge.annotations ?? {};
}

/** The well-known key: an uppercase ISO 3166-1 alpha-2 code. */
export const COUNTRY_ANNOTATION = "country";

/**
 * The country a challenge is placed in, or null.
 *
 * The shape is checked rather than trusted. A value that is not two letters is not a country a
 * map can find, and quietly dropping a pin at the wrong place — or at 0°,0° — is worse than
 * treating the challenge as unplaced, where it is still listed and still playable.
 */
export function countryOf(challenge: BoardChallenge | Challenge): string | null {
  const raw = annotationsOf(challenge)[COUNTRY_ANNOTATION];
  if (typeof raw !== "string") return null;
  const code = raw.trim().toUpperCase();
  return /^[A-Z]{2}$/.test(code) ? code : null;
}

/** `0` means unlimited. It is not "no attempts allowed". */
export function attemptsLabel(c: Challenge): string {
  if (c.max_attempts === 0) return "unlimited attempts";
  if (c.attempts_used === undefined) return `${c.max_attempts} attempts allowed`;
  return `${c.attempts_used} of ${c.max_attempts} attempts used`;
}

/** A redacted count is `null`, and `null` is not zero: it means the caller may not see who solved. */
export function solveCountLabel(count: number | null): string {
  return count === null ? "—" : `${count} ${count === 1 ? "solve" : "solves"}`;
}

export function fileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}
