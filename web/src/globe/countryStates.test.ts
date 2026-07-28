import { describe, expect, it } from "vitest";
import { countryByChallenge, indexByCode, placeChallenges } from "./countryStates";
import { capColorOf, SCENE } from "./palette";
import type { BoardChallenge } from "../challenges/types";

let nextId = 1;

function challenge(
  opts: { country?: string; solved?: boolean; value?: number; name?: string } = {},
): BoardChallenge {
  return {
    id: nextId++,
    name: opts.name ?? `challenge ${nextId}`,
    category: "misc",
    function: "static",
    value: opts.value ?? 100,
    solved: opts.solved ?? false,
    locked: false,
    solve_count: 0,
    annotations: opts.country === undefined ? {} : { country: opts.country },
  } as BoardChallenge;
}

describe("placeChallenges", () => {
  it("groups challenges by the country they are annotated with", () => {
    const { countries } = placeChallenges([
      challenge({ country: "CZ" }),
      challenge({ country: "JP" }),
      challenge({ country: "CZ" }),
    ]);
    expect(countries.map((c) => c.code)).toEqual(["CZ", "JP"]);
    expect(countries[0]!.total).toBe(2);
    expect(countries[1]!.total).toBe(1);
  });

  it("counts progress per country", () => {
    const { countries } = placeChallenges([
      challenge({ country: "CZ", solved: true }),
      challenge({ country: "CZ" }),
      challenge({ country: "JP", solved: true }),
      challenge({ country: "PL" }),
    ]);
    const byCode = indexByCode(countries);
    expect(byCode.get("CZ")).toMatchObject({ solved: 1, total: 2, capture: "partial" });
    expect(byCode.get("JP")).toMatchObject({ solved: 1, total: 1, capture: "captured" });
    expect(byCode.get("PL")).toMatchObject({ solved: 0, total: 1, capture: "open" });
  });

  it("orders a country's challenges by value then name, like the board", () => {
    const { countries } = placeChallenges([
      challenge({ country: "CZ", value: 300, name: "c" }),
      challenge({ country: "CZ", value: 100, name: "b" }),
      challenge({ country: "CZ", value: 100, name: "a" }),
    ]);
    expect(countries[0]!.challenges.map((c) => c.name)).toEqual(["a", "b", "c"]);
  });

  it("normalises the annotation before matching", () => {
    const { countries, unplaced } = placeChallenges([challenge({ country: "cz" })]);
    expect(countries.map((c) => c.code)).toEqual(["CZ"]);
    expect(unplaced).toHaveLength(0);
  });

  // The property the whole view rests on: a country nobody can find is still a challenge
  // somebody can play.
  it("lists a challenge with no country rather than dropping it", () => {
    const { countries, unplaced } = placeChallenges([challenge(), challenge({ country: "CZ" })]);
    expect(countries).toHaveLength(1);
    expect(unplaced).toHaveLength(1);
  });

  it("lists a challenge in a country these outlines do not draw", () => {
    // Monaco is a real ISO code; the 1:110m cut is too coarse to carry it.
    const { countries, unplaced } = placeChallenges([challenge({ country: "MC" })]);
    expect(countries).toHaveLength(0);
    expect(unplaced).toHaveLength(1);
  });

  it("lists a challenge whose annotation is not a country at all", () => {
    const { unplaced } = placeChallenges([challenge({ country: "nonsense" })]);
    expect(unplaced).toHaveLength(1);
  });

  it("returns every challenge exactly once", () => {
    const input = [
      challenge({ country: "CZ" }),
      challenge({ country: "CZ", solved: true }),
      challenge({ country: "JP" }),
      challenge({ country: "MC" }),
      challenge(),
    ];
    const { countries, unplaced } = placeChallenges(input);
    const seen = [...countries.flatMap((c) => [...c.challenges]), ...unplaced].map((c) => c.id);
    expect(seen.sort()).toEqual(input.map((c) => c.id).sort());
  });

  it("is empty, not broken, on an empty board", () => {
    expect(placeChallenges([])).toEqual({ countries: [], unplaced: [] });
  });
});

describe("countryByChallenge", () => {
  it("maps a challenge id to the country it is drawn in", () => {
    const a = challenge({ country: "CZ" });
    const b = challenge();
    const { countries } = placeChallenges([a, b]);
    const index = countryByChallenge(countries);
    expect(index.get(a.id)).toBe("CZ");
    // An unplaced challenge has nowhere to pulse; the caller must see that as absent.
    expect(index.get(b.id)).toBeUndefined();
  });
});

describe("capColorOf", () => {
  it("paints a country by how far its team has got", () => {
    const { countries } = placeChallenges([
      challenge({ country: "CZ" }),
      challenge({ country: "JP", solved: true }),
      challenge({ country: "PL", solved: true }),
      challenge({ country: "PL" }),
    ]);
    const byCode = indexByCode(countries);
    expect(capColorOf(byCode.get("CZ"))).toBe(SCENE.open);
    expect(capColorOf(byCode.get("PL"))).toBe(SCENE.partial);
    expect(capColorOf(byCode.get("JP"))).toBe(SCENE.captured);
  });

  it("leaves a country with no challenges neutral", () => {
    expect(capColorOf(undefined)).toBe(SCENE.quiet);
  });
});
