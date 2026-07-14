import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, setCsrfToken, setUnauthorizedHandler } from "./client";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}

const fetchMock = vi.fn<typeof fetch>();

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  setCsrfToken(null);
  setUnauthorizedHandler(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
  fetchMock.mockReset();
});

function lastInit(): RequestInit {
  const call = fetchMock.mock.calls.at(-1);
  expect(call).toBeDefined();
  return call![1]!;
}

function lastHeaders(): Record<string, string> {
  return (lastInit().headers ?? {}) as Record<string, string>;
}

describe("CSRF handling", () => {
  it("attaches CSRF-Token to unsafe methods once a token is set", async () => {
    setCsrfToken("tok-123");
    fetchMock.mockResolvedValue(jsonResponse({ status: "incorrect", first_blood: false, value: 0 }));

    await api.attempt(1, "flag{x}");

    expect(lastHeaders()["CSRF-Token"]).toBe("tok-123");
    expect(lastInit().credentials).toBe("include");
  });

  it("does not attach CSRF-Token to GET requests", async () => {
    setCsrfToken("tok-123");
    fetchMock.mockResolvedValue(jsonResponse({ challenges: [] }));

    await api.challenges();

    expect(lastHeaders()["CSRF-Token"]).toBeUndefined();
  });

  it("sends no CSRF header when no token is held", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ ok: true }));

    await api.logout();

    expect(lastHeaders()["CSRF-Token"]).toBeUndefined();
  });

  it("stores the token from a login response and uses it on the next write", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ user_id: 1, csrf_token: "fresh", expires_at: "2026-01-01T00:00:00Z" }),
    );
    await api.login({ email: "a@b.c", password: "hunter22" });

    fetchMock.mockResolvedValueOnce(jsonResponse({ status: "correct", first_blood: true, value: 500 }));
    await api.attempt(7, "flag{y}");

    expect(lastHeaders()["CSRF-Token"]).toBe("fresh");
  });

  it("clears the token on logout", async () => {
    setCsrfToken("stale");
    // A Response body is single-use; hand out a fresh one per call.
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse({ ok: true })));

    await api.logout();
    await api.attempt(1, "flag{z}");

    expect(lastHeaders()["CSRF-Token"]).toBeUndefined();
  });
});

describe("error surfacing", () => {
  it("throws ApiError carrying the problem detail", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ title: "Conflict", status: 409, detail: "hint already unlocked" }, 409),
    );

    const err = await api.unlockHint(1, 2).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(409);
    expect((err as ApiError).message).toBe("hint already unlocked");
  });

  it("invokes the unauthorized handler on a 401", async () => {
    const kicked = vi.fn();
    setUnauthorizedHandler(kicked);
    fetchMock.mockResolvedValue(jsonResponse({ detail: "session expired" }, 401));

    await expect(api.me()).rejects.toBeInstanceOf(ApiError);
    expect(kicked).toHaveBeenCalledOnce();
  });

  it("does not treat a failed login as a dead session", async () => {
    const kicked = vi.fn();
    setUnauthorizedHandler(kicked);
    fetchMock.mockResolvedValue(jsonResponse({ detail: "invalid email or password" }, 401));

    await expect(api.login({ email: "a@b.c", password: "nope-nope" })).rejects.toBeInstanceOf(
      ApiError,
    );
    expect(kicked).not.toHaveBeenCalled();
  });
});
