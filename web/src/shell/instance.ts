import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { instanceQuery } from "../queries";

export type AccountMode = "users" | "teams";
export type RegistrationVisibility = "public" | "private";

/** Where the event's clock stands right now. */
export type Phase = "before" | "running" | "ended";

export interface InstanceState {
  ctfName: string;
  mode: AccountMode;
  /** Teams mode gates whole routes, not just widgets: `/team` does not exist in users mode. */
  teamsMode: boolean;
  start?: Date;
  end?: Date;
  freeze?: Date;
  paused: boolean;
  teamCreation: boolean;
  verifyEmails: boolean;
  registrationVisibility: RegistrationVisibility;
  /**
   * Which view of the challenge board this instance leads with. Passed through unvalidated:
   * `views/resolve.ts` owns what is and is not a view this build can render, and a value it
   * does not know has to reach it to be reported.
   */
  portalView: string | null;
  loaded: boolean;
}

function at(value: string | undefined): Date | undefined {
  if (value === undefined) return undefined;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? undefined : d;
}

function mode(value: string | undefined): AccountMode {
  // Anything we do not recognise is users mode. Conjuring a team UI out of a value we cannot
  // read would strand a player on routes the server answers 404 for.
  return value === "teams" ? "teams" : "users";
}

function visibility(value: string | undefined): RegistrationVisibility {
  return value === "private" ? "private" : "public";
}

export function phaseAt(now: number, start?: Date, end?: Date): Phase {
  if (start !== undefined && now < start.getTime()) return "before";
  if (end !== undefined && now >= end.getTime()) return "ended";
  return "running";
}

/**
 * The instance snapshot, decoded. Does not tick: everything here changes only when the query
 * refetches, so the shell can hold it without re-rendering the page under it every second.
 */
export function useInstanceState(): InstanceState {
  const { data } = useQuery(instanceQuery);

  const accountMode = mode(data?.mode);
  return {
    ctfName: data?.ctf_name ?? "flagfish",
    mode: accountMode,
    teamsMode: accountMode === "teams",
    start: at(data?.start),
    end: at(data?.end),
    freeze: at(data?.freeze),
    paused: data?.paused ?? false,
    teamCreation: data?.team_creation ?? true,
    verifyEmails: data?.verify_emails ?? false,
    registrationVisibility: visibility(data?.registration_visibility),
    portalView: data?.portal_view ?? null,
    loaded: data !== undefined,
  };
}

/**
 * True while the organisers have paused the event: the flag input is disabled and says why.
 * Only submissions are paused — browsing and hint unlocks keep working — and an admin is
 * paused with everybody else.
 */
export function useCtfPaused(): boolean {
  return useInstanceState().paused;
}

/** A clock that re-renders its caller. Keep it in the leaf that shows time, never in a layout. */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

export interface CtfClock extends InstanceState {
  now: number;
  phase: Phase;
  /** The board is frozen: solves after this instant are not shown to a non-exempt viewer. */
  frozen: boolean;
}

/** The instance snapshot plus a live clock. Ticking, so only components that show time use it. */
export function useCtfClock(): CtfClock {
  const instance = useInstanceState();
  const now = useNow(1000);
  return {
    ...instance,
    now,
    phase: phaseAt(now, instance.start, instance.end),
    frozen: instance.freeze !== undefined && now >= instance.freeze.getTime(),
  };
}

/** `2d 04:17:09`, counting down. Drops the day part once it is gone. */
export function formatRemaining(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  const hms = `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`;
  return days > 0 ? `${days}d ${hms}` : hms;
}
