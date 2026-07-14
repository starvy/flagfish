import type { Notification } from "../api/client";

export type { Notification };

/**
 * One row per id, newest first.
 *
 * Three sources overlap by design: the stream replays its last 50 on every connect, the replay
 * window overlaps the live feed by up to one event, and the backfill covers the same rows again.
 * The id is the only thing that says "already have it".
 */
export function mergeNotifications(
  ...sources: readonly (readonly Notification[] | null | undefined)[]
): Notification[] {
  const byId = new Map<number, Notification>();
  for (const source of sources ?? []) {
    for (const n of source ?? []) {
      if (!byId.has(n.id)) byId.set(n.id, n);
    }
  }
  // Ids are monotonic, so id order is publication order — no date parsing needed.
  return [...byId.values()].sort((a, b) => b.id - a.id);
}

export function newestId(items: readonly Notification[]): number {
  let max = 0;
  for (const n of items) if (n.id > max) max = n.id;
  return max;
}

// The wire carries no kind or severity — a first blood is a notification like any other — so
// the title is the only signal there is. The publisher writes it; we read it.
const FIRST_BLOOD = /first\s*blood/i;

export function isFirstBlood(n: Notification): boolean {
  return FIRST_BLOOD.test(n.title);
}

/** A toast is one line wide. Take the first prose line of the Markdown body and cap it. */
export function excerpt(content: string, max = 120): string {
  const line = content
    .split("\n")
    .map((l) => l.trim())
    .find((l) => l !== "" && !l.startsWith("```")) ?? "";
  return line.length > max ? `${line.slice(0, max - 1)}…` : line;
}
