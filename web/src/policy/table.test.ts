import { describe, expect, it } from "vitest";
import decideGo from "../../../internal/domain/policy/decide.go?raw";
import middlewareGo from "../../../internal/httpapi/middleware.go?raw";
import { ApiError } from "../api/errors";
import { denialOf } from "./denial";
import { REASONS, isReason, reasonFromType, type Reason } from "./reasons";
import { TREATMENTS } from "./table";

describe("the reason vocabulary tracks the Go source", () => {
  // The union is the server's vocabulary or it is nothing: a reason the server can emit but
  // the UI has never heard of renders as a blank denial. Read it out of the Go rather than
  // trusting a doc that can drift.
  it("covers every reason in the policy gate's name table", () => {
    const table = /var reasonNames = map\[Reason\]string\{([\s\S]*?)\n\}/.exec(decideGo);
    expect(table).not.toBeNull();

    const emitted = [...table![1].matchAll(/"([a-z-]+)"/g)]
      .map((m) => m[1])
      .filter((s) => s !== "");

    expect(emitted.length).toBeGreaterThan(0);
    for (const reason of emitted) {
      expect(REASONS, `policy reason "${reason}" is missing from the Reason union`).toContain(
        reason,
      );
    }
  });

  it("covers every reason the raw middleware gates emit", () => {
    const emitted = [
      ...middlewareGo.matchAll(/problem\(\s*w,\s*http\.Status\w+,\s*"([a-z-]+)"/g),
    ].map((m) => m[1]);

    expect(emitted).toContain("csrf");
    expect(emitted).toContain("rate-limited");
    for (const reason of emitted) {
      expect(REASONS, `middleware reason "${reason}" is missing from the Reason union`).toContain(
        reason,
      );
    }
  });
});

describe("every reason has a UI treatment", () => {
  it.each(REASONS)("%s", (reason) => {
    const t = TREATMENTS[reason];
    expect(t).toBeDefined();
    expect(t.title).not.toBe("");
    expect(t.message).not.toBe("");
    expect(["info", "warning", "danger"]).toContain(t.tone);
  });

  it("maps no reason it was not given", () => {
    expect(Object.keys(TREATMENTS).sort()).toEqual([...REASONS].sort());
  });
});

// The golden table: the treatment each denial gets. A change here is a product decision and
// must show up as a diff in this list, not as a surprise on a screen.
const GOLDEN: ReadonlyArray<[Reason, { tone: string; transient: boolean; action: string | null }]> = [
  ["setup-incomplete", { tone: "info", transient: false, action: "/setup" }],
  ["banned", { tone: "danger", transient: false, action: null }],
  ["team-banned", { tone: "danger", transient: false, action: null }],
  ["password-change-required", { tone: "warning", transient: false, action: "/reset-password" }],
  ["not-found", { tone: "info", transient: false, action: null }],
  ["auth-required", { tone: "info", transient: false, action: "/login" }],
  ["authentication-required", { tone: "info", transient: false, action: "/login" }],
  ["admins-only", { tone: "warning", transient: false, action: null }],
  ["admin-required", { tone: "warning", transient: false, action: null }],
  ["scores-hidden", { tone: "info", transient: false, action: null }],
  ["unverified", { tone: "warning", transient: false, action: "/confirm" }],
  ["incomplete-profile", { tone: "warning", transient: false, action: "/settings" }],
  ["incomplete-team-profile", { tone: "warning", transient: false, action: "/team" }],
  ["team-required", { tone: "info", transient: false, action: "/team" }],
  ["already-on-team", { tone: "info", transient: false, action: "/team" }],
  ["team-creation-disabled", { tone: "info", transient: false, action: null }],
  ["ctf-not-started", { tone: "info", transient: true, action: null }],
  ["ctf-ended", { tone: "info", transient: false, action: null }],
  ["paused", { tone: "warning", transient: true, action: null }],
  ["already-authed", { tone: "info", transient: false, action: "/challenges" }],
  ["csrf", { tone: "warning", transient: false, action: null }],
  ["rate-limited", { tone: "warning", transient: true, action: null }],
  ["unavailable", { tone: "warning", transient: true, action: null }],
  ["invalid-credentials", { tone: "warning", transient: false, action: "/login" }],
  ["body-too-large", { tone: "warning", transient: false, action: null }],
  ["internal-error", { tone: "danger", transient: false, action: null }],
  ["streaming-unsupported", { tone: "warning", transient: false, action: null }],
];

describe("reason to UI treatment", () => {
  it.each(GOLDEN)("%s", (reason, want) => {
    const t = TREATMENTS[reason];
    expect(t.tone).toBe(want.tone);
    expect(t.transient).toBe(want.transient);
    expect(t.action?.to ?? null).toBe(want.action);
  });

  it("covers the whole vocabulary", () => {
    expect(GOLDEN.map(([r]) => r).sort()).toEqual([...REASONS].sort());
  });
});

describe("reason parsing", () => {
  it("reads a reason out of the middleware's type URI", () => {
    expect(reasonFromType("urn:flagfish:error:rate-limited")).toBe("rate-limited");
  });

  it("ignores Huma's about:blank default", () => {
    expect(reasonFromType("about:blank")).toBeNull();
  });

  it("refuses a URI naming something outside the vocabulary", () => {
    expect(reasonFromType("urn:flagfish:error:made-up")).toBeNull();
  });

  it("recognises a bare reason string", () => {
    expect(isReason("ctf-ended")).toBe(true);
    expect(isReason("too many requests")).toBe(false);
  });
});

describe("denialOf", () => {
  it("is null for anything that is not an ApiError", () => {
    expect(denialOf(new Error("boom"))).toBeNull();
    expect(denialOf(null)).toBeNull();
  });

  it("carries the Location the policy gate chose", () => {
    const d = denialOf(
      new ApiError({ status: 403, detail: "team-required", reason: "team-required", location: "/team" }),
    );
    expect(d?.location).toBe("/team");
    expect(d?.treatment.title).toBe("Join a team to play");
  });

  it("renders a 403 the server did not name, using its detail", () => {
    // The attempt path's resource guards 403 with prose, not a reason.
    const d = denialOf(new ApiError({ status: 403, detail: "solve the prerequisites first" }));
    expect(d).not.toBeNull();
    expect(d!.reason).toBeNull();
    expect(d!.treatment.message).toBe("solve the prerequisites first");
  });

  it("does not swallow the hint-unlock UI states", () => {
    // 402 (cannot afford) and 409 (already unlocked) belong to the screen, not to the gate.
    expect(denialOf(new ApiError({ status: 402, detail: "not enough points" }))).toBeNull();
    expect(denialOf(new ApiError({ status: 409, detail: "hint already unlocked" }))).toBeNull();
  });

  it("surfaces retry_after on a 429", () => {
    const d = denialOf(
      new ApiError({ status: 429, detail: "too many requests", reason: "rate-limited", retryAfter: 30 }),
    );
    expect(d?.retryAfter).toBe(30);
    expect(d?.treatment.transient).toBe(true);
  });
});
