import { describe, expect, it } from "vitest";
import { annotationsOf, countryOf } from "./types";
import type { BoardChallenge } from "./types";

function challenge(annotations?: Record<string, string>): BoardChallenge {
  return { ...(annotations === undefined ? {} : { annotations }) } as BoardChallenge;
}

describe("annotationsOf", () => {
  it("reads the map the server sends", () => {
    expect(annotationsOf(challenge({ country: "CZ", note: "x" }))).toEqual({
      country: "CZ",
      note: "x",
    });
  });

  // A build talking to a server that predates annotations must still render a board.
  it("is an empty map when the field is absent", () => {
    expect(annotationsOf(challenge())).toEqual({});
  });
});

describe("countryOf", () => {
  it("takes a well-formed code", () => {
    expect(countryOf(challenge({ country: "CZ" }))).toBe("CZ");
  });

  it("normalises case and surrounding space", () => {
    expect(countryOf(challenge({ country: " cz " }))).toBe("CZ");
  });

  it("is null when there is no country at all", () => {
    expect(countryOf(challenge())).toBeNull();
    expect(countryOf(challenge({}))).toBeNull();
    expect(countryOf(challenge({ note: "unrelated" }))).toBeNull();
  });

  // Anything that is not two letters cannot be found on a map; the challenge is unplaced, not
  // placed somewhere arbitrary.
  it("rejects a value that is not two letters", () => {
    for (const bad of ["", "C", "CZE", "C1", "??", "czech republic"]) {
      expect(countryOf(challenge({ country: bad })), bad).toBeNull();
    }
  });
});
