import type { ChallengeDetail, ChallengeListItem, Instance } from "../api/client";

// `api/schema.gen.ts` is generated from `openapi.yaml`, and the committed copy is one regeneration
// behind the Go: `locked`, `flag_mode`, `instance` and a file's `name` are all on the wire today but
// missing from the generated types. What follows is what the server actually sends. Re-running
// `npm run generate:api` turns these from corrections into redundant aliases; nothing below is a
// guess about the API, and none of it invents a field the handlers do not write.

export interface ChallengeFile {
  id: number;
  name: string;
  size_bytes: number;
}

export interface ChallengeHint {
  id: number;
  /** Null for an untitled hint. */
  title: string | null;
  cost: number;
  unlocked: boolean;
  /** An earlier hint in the chain is still locked, so the unlock would be refused. Not "unbought". */
  locked: boolean;
}

/** The caller's own bundle for a unique-flag challenge. It carries the vars, never the flag. */
export interface ChallengeInstance {
  instance_id: number;
  artifact_id?: number;
  vars: Record<string, unknown>;
}

export type BoardChallenge = ChallengeListItem & {
  locked: boolean;
  /** Not yet returned by `list-challenges`; rendered the moment the handler starts sending it. */
  tags?: string[] | null;
};

export type Challenge = Omit<ChallengeDetail, "files" | "hints"> & {
  locked: boolean;
  flag_mode: "static" | "unique";
  files: ChallengeFile[] | null;
  hints: ChallengeHint[] | null;
  instance?: ChallengeInstance;
  /**
   * Not on the wire. The server counts wrong answers under a lock to enforce `max_attempts` but
   * never reports the tally, so the budget can only be rendered as a total until it does. Guessing
   * the count from what this tab has submitted would be a lie: the cap is all-time and per-account.
   */
  attempts_used?: number;
};

export type InstanceInfo = Instance & {
  paused: boolean;
  start?: string;
  end?: string;
  freeze?: string;
};

export const asBoardChallenge = (c: ChallengeListItem): BoardChallenge => c as BoardChallenge;
export const asChallenge = (c: ChallengeDetail): Challenge => c as unknown as Challenge;
export const asInstanceInfo = (i: Instance): InstanceInfo => i as InstanceInfo;

/** `0` means unlimited — it is not "no attempts allowed". */
export function attemptsLabel(c: Challenge): string {
  if (c.max_attempts === 0) return "unlimited attempts";
  if (c.attempts_used === undefined) return `${c.max_attempts} attempts allowed`;
  return `${c.attempts_used} of ${c.max_attempts} attempts used`;
}

/** A redacted count is `null`, and `null` is not zero: the caller may not see who solved what. */
export function solveCountLabel(count: number | null): string {
  return count === null ? "—" : `${count} ${count === 1 ? "solve" : "solves"}`;
}

export function fileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}
</content>
