import { describe, expect, it } from "vitest";
import { annotationsOf, countryOf } from "./types";
import type { BoardChallenge } from "./types";

// The field is required on the wire and the UI ships inside the binary that serves it, so
// "absent" is not a state a fixture may invent: no server can produce it.
function challenge(annotations?: Record<string, string>): BoardChallenge {
  return { annotations: annotations ?? {} } as BoardChallenge;
}

describe("annotationsOf", () => {
  it("reads the map the server sends", () => {
    expect(annotationsOf(challenge({ country: "CZ", note: "x" }))).toEqual({
      country: "CZ",
      note: "x",
    });
  });

  it("is an empty map when a challenge carries none", () => {
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
