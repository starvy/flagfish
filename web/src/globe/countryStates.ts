import { countryOf, type BoardChallenge } from "../challenges/types";
import { isDrawn } from "./geography";

/** How far a team has got in one country. */
export type Capture = "open" | "partial" | "captured";

export interface CountryState {
  code: string;
  /** Every challenge in this country, by value then name — the board's own order. */
  challenges: readonly BoardChallenge[];
  solved: number;
  total: number;
  capture: Capture;
}

export interface Placement {
  /** Countries with at least one challenge, sorted by code so rendering order is stable. */
  countries: readonly CountryState[];
  /**
   * Everything the map cannot place: no `country` annotation, or one naming a country these
   * outlines do not draw. A view never hides what the API returned — a locked challenge has no
   * annotations at all and lands here, which is exactly where a player can still find it.
   */
  unplaced: readonly BoardChallenge[];
}

function byValueThenName(a: BoardChallenge, b: BoardChallenge): number {
  return a.value - b.value || a.name.localeCompare(b.name);
}

function captureOf(solved: number, total: number): Capture {
  if (solved === 0) return "open";
  return solved === total ? "captured" : "partial";
}

/**
 * Splits the board into what the globe can draw and what it cannot.
 *
 * Pure, and total: every challenge handed in comes back out exactly once, in `countries` or in
 * `unplaced`. That is the property the globe leans on to promise it never loses a challenge.
 */
export function placeChallenges(challenges: readonly BoardChallenge[]): Placement {
  const byCountry = new Map<string, BoardChallenge[]>();
  const unplaced: BoardChallenge[] = [];

  for (const challenge of challenges) {
    const code = countryOf(challenge);
    if (code === null || !isDrawn(code)) {
      unplaced.push(challenge);
      continue;
    }
    const list = byCountry.get(code);
    if (list === undefined) byCountry.set(code, [challenge]);
    else list.push(challenge);
  }

  const countries: CountryState[] = [];
  for (const [code, list] of [...byCountry].sort(([a], [b]) => a.localeCompare(b))) {
    list.sort(byValueThenName);
    const solved = list.filter((c) => c.solved).length;
    countries.push({
      code,
      challenges: list,
      solved,
      total: list.length,
      capture: captureOf(solved, list.length),
    });
  }

  unplaced.sort(byValueThenName);
  return { countries, unplaced };
}

/** Indexed for the render loop, which asks per polygon and per solve event. */
export function indexByCode(countries: readonly CountryState[]): ReadonlyMap<string, CountryState> {
  return new Map(countries.map((c) => [c.code, c]));
}

/** Which country a challenge is in, for turning a solve event into a pulse. */
export function countryByChallenge(
  countries: readonly CountryState[],
): ReadonlyMap<number, string> {
  const out = new Map<number, string>();
  for (const country of countries) {
    for (const challenge of country.challenges) out.set(challenge.id, country.code);
  }
  return out;
}
