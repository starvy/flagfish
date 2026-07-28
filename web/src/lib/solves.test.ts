import { describe, expect, it } from "vitest";
import { parseSolveEvent } from "./solves";

describe("parseSolveEvent", () => {
  it("reads a well-formed frame", () => {
    const e = parseSolveEvent(
      '{"solve_id":7,"challenge_id":3,"first_blood":true,"solved_at":"2026-07-28T10:00:00Z"}',
    );
    expect(e).toEqual({
      solve_id: 7,
      challenge_id: 3,
      first_blood: true,
      solved_at: "2026-07-28T10:00:00Z",
    });
  });

  it("defaults first_blood to false rather than guessing", () => {
    expect(parseSolveEvent('{"solve_id":1,"challenge_id":2}')?.first_blood).toBe(false);
    // Only a real `true` is a first blood: a truthy string is not the server saying so.
    expect(
      parseSolveEvent('{"solve_id":1,"challenge_id":2,"first_blood":"yes"}')?.first_blood,
    ).toBe(false);
  });

  it("drops a frame without the two ids the animation is keyed by", () => {
    expect(parseSolveEvent('{"challenge_id":2}')).toBeNull();
    expect(parseSolveEvent('{"solve_id":1}')).toBeNull();
    expect(parseSolveEvent('{"solve_id":"1","challenge_id":2}')).toBeNull();
    expect(parseSolveEvent('{"solve_id":1,"challenge_id":null}')).toBeNull();
  });

  it("drops a frame that is not an object", () => {
    for (const raw of ["", "null", "[]", "12", '"solve"', "{"]) {
      expect(parseSolveEvent(raw), raw).toBeNull();
    }
  });

  it("tolerates a missing timestamp", () => {
    expect(parseSolveEvent('{"solve_id":1,"challenge_id":2}')?.solved_at).toBe("");
  });
});
