import { describe, expect, it } from "vitest";
import { fieldErrors } from "./Form";

describe("fieldErrors", () => {
  it("maps a 422 problem document onto field names", () => {
    const problem = {
      title: "Unprocessable Entity",
      status: 422,
      errors: [
        { location: "body.email", message: "expected string to match format 'email'" },
        { location: "body.hints[2].cost", message: "expected number >= 0" },
      ],
    };

    expect(fieldErrors(problem)).toEqual({
      email: "expected string to match format 'email'",
      cost: "expected number >= 0",
    });
  });

  it("files a locationless or whole-body error under _", () => {
    expect(fieldErrors({ errors: [{ message: "body is not valid JSON" }] })).toEqual({
      _: "body is not valid JSON",
    });
    expect(fieldErrors({ errors: [{ location: "body", message: "required" }] })).toEqual({
      _: "required",
    });
  });

  it("keeps the first message when a field has several", () => {
    const problem = {
      errors: [
        { location: "body.name", message: "too short" },
        { location: "body.name", message: "bad characters" },
      ],
    };
    expect(fieldErrors(problem)).toEqual({ name: "too short" });
  });

  it("survives anything that is not a problem document", () => {
    expect(fieldErrors(null)).toEqual({});
    expect(fieldErrors(undefined)).toEqual({});
    expect(fieldErrors(new Error("boom"))).toEqual({});
    expect(fieldErrors({ errors: "nope" })).toEqual({});
    expect(fieldErrors({ errors: [{ location: "body.x" }] })).toEqual({});
  });
});
