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
