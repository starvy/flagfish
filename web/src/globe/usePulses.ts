import { useEffect, useRef, useState } from "react";
import { useSolveStream } from "../lib/solves";
import { anchorOf } from "./geography";

/** A ring on the globe. Lives for a few seconds and is never persisted. */
export interface Pulse {
  /** The solve id: unique, and what keeps a reconnect from drawing the same ring twice. */
  id: number;
  lat: number;
  lng: number;
  firstBlood: boolean;
}

const LIFETIME_MS = 4_000;

/**
 * Turns solves arriving on the stream into rings over the country they were scored in.
 *
 * A solve in a country this map does not draw produces nothing — there is no honest place to put
 * it, and a ring in the wrong ocean is worse than no ring. Nothing here feeds the board: the
 * queries stay the truth, and this is decoration with a lifetime.
 */
export function usePulses(
  countryByChallenge: ReadonlyMap<number, string>,
  options: { enabled?: boolean } = {},
): readonly Pulse[] {
  const enabled = options.enabled ?? true;
  const { events } = useSolveStream({ enabled });
  const [pulses, setPulses] = useState<readonly Pulse[]>([]);
  const handled = useRef(new Set<number>());

  // The index changes as the board refetches; the effect must read the current one without
  // re-running and replaying every event against it.
  const index = useRef(countryByChallenge);
  index.current = countryByChallenge;

  // Each batch expires on its own timer. Tying expiry to the effect's cleanup would cancel it
  // the moment the next solve arrived, and the rings would never come off.
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());
  useEffect(
    () => () => {
      for (const timer of timers.current) clearTimeout(timer);
      timers.current.clear();
    },
    [],
  );

  useEffect(() => {
    const fresh: Pulse[] = [];
    for (const event of events) {
      if (handled.current.has(event.solve_id)) continue;
      handled.current.add(event.solve_id);
      const code = index.current.get(event.challenge_id);
      if (code === undefined) continue;
      const at = anchorOf(code);
      if (at === null) continue;
      fresh.push({ id: event.solve_id, lat: at.lat, lng: at.lng, firstBlood: event.first_blood });
    }
    if (fresh.length === 0) return;

    setPulses((current) => [...current, ...fresh]);
    const ids = new Set(fresh.map((p) => p.id));
    const timer = setTimeout(() => {
      timers.current.delete(timer);
      setPulses((current) => current.filter((p) => !ids.has(p.id)));
    }, LIFETIME_MS);
    timers.current.add(timer);
  }, [events]);

  return pulses;
}
