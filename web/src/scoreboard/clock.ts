import type { Instance } from "../api/client";

export type Mode = "users" | "teams";

/** The event clock, as far as the public instance document tells it. Any field may be unset. */
export interface EventClock {
  mode: Mode | null;
  start: Date | null;
  end: Date | null;
  freeze: Date | null;
}

// The server sends mode and the three clock instants on /instance, but the committed OpenAPI
// document still describes the older, thinner body, so the generated type does not know about
// them. Read them structurally until the document is regenerated; the cast disappears then.
interface WireInstance {
  mode?: unknown;
  start?: unknown;
  end?: unknown;
  freeze?: unknown;
}

function instant(value: unknown): Date | null {
  if (typeof value !== "string") return null;
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? null : at;
}

export function clockOf(instance: Instance | undefined): EventClock {
  const wire = (instance ?? {}) as WireInstance;
  return {
    mode: wire.mode === "users" || wire.mode === "teams" ? wire.mode : null,
    start: instant(wire.start),
    end: instant(wire.end),
    freeze: instant(wire.freeze),
  };
}

/** Past the freeze the board stops moving for everyone the organisers have not exempted. */
export function isFrozen(clock: EventClock, now: number): boolean {
  return clock.freeze !== null && now >= clock.freeze.getTime();
}

export function hasEnded(clock: EventClock, now: number): boolean {
  return clock.end !== null && now >= clock.end.getTime();
}

/** `3d 04:12:07`, counting down. Days are dropped once there are none left. */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const days = Math.floor(total / 86_400);
  const hours = Math.floor((total % 86_400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  const hms = `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`;
  return days > 0 ? `${days}d ${hms}` : hms;
}
