import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { adminApi } from "./admin";
import { api, ApiError, setCsrfToken, setUnauthorizedHandler } from "./client";

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
      ...headers,
    },
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

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0]);
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

    fetchMock.mockResolvedValueOnce(
      jsonResponse({ status: "correct", first_blood: true, value: 500 }),
    );
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

describe("CSRF recovery", () => {
  const csrfDenial = () =>
    jsonResponse(
      {
        type: "urn:flagfish:error:csrf",
        detail: "missing or invalid CSRF token",
        status: 403,
      },
      403,
    );

  it("re-reads the token and replays the write exactly once", async () => {
    setCsrfToken("stale");
    fetchMock
      .mockResolvedValueOnce(csrfDenial())
      .mockResolvedValueOnce(jsonResponse({ user_id: 1, csrf_token: "rotated" }))
      .mockResolvedValueOnce(jsonResponse({ status: "incorrect", first_blood: false, value: 0 }));

    const out = await api.attempt(3, "flag{a}");

    expect(out.status).toBe("incorrect");
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(lastHeaders()["CSRF-Token"]).toBe("rotated");
  });

  it("never loops: a second CSRF denial is thrown", async () => {
    setCsrfToken("stale");
    fetchMock
      .mockResolvedValueOnce(csrfDenial())
      .mockResolvedValueOnce(jsonResponse({ user_id: 1, csrf_token: "rotated" }))
      .mockResolvedValueOnce(csrfDenial());

    const err = await api.attempt(3, "flag{a}").catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).reason).toBe("csrf");
    // The original write, the /me refresh, the replay — and then it stops.
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("gives up when /me yields no token rather than retrying blind", async () => {
    setCsrfToken("stale");
    fetchMock
      .mockResolvedValueOnce(csrfDenial())
      .mockResolvedValueOnce(jsonResponse({ user_id: 1, name: "p" }));

    await expect(api.attempt(3, "flag{a}")).rejects.toBeInstanceOf(ApiError);
    expect(fetchMock).toHaveBeenCalledTimes(2);
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

  it("reads the reason from a Huma operation's detail", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ title: "Forbidden", detail: "paused" }, 403));

    const err = (await api.attempt(1, "f").catch((e: unknown) => e)) as ApiError;
    expect(err.reason).toBe("paused");
  });

  it("reads the reason from a middleware gate's type, whose detail is prose", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        { type: "urn:flagfish:error:rate-limited", detail: "too many requests" },
        429,
        { "Retry-After": "12" },
      ),
    );

    const err = (await api.challenges().catch((e: unknown) => e)) as ApiError;
    expect(err.reason).toBe("rate-limited");
    expect(err.retryAfter).toBe(12);
    expect(err.detail).toBe("too many requests");
  });

  it("surfaces the redirect destination from the Location header, not a 3xx", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ detail: "team-required" }, 403, { Location: "/team" }),
    );

    const err = (await api.challenges().catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(403);
    expect(err.reason).toBe("team-required");
    expect(err.location).toBe("/team");
  });

  it("surfaces per-field validation errors on a 422", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        {
          title: "Unprocessable Entity",
          detail: "validation failed",
          errors: [
            { location: "body.password", message: "expected length >= 8", value: "x" },
            { location: "body.email", message: "expected format email" },
          ],
        },
        422,
      ),
    );

    const err = (await api
      .register({ name: "p", email: "bad", password: "x" })
      .catch((e: unknown) => e)) as ApiError;

    expect(err.status).toBe(422);
    expect(err.fieldErrors).toHaveLength(2);
    expect(err.fieldErrors[0]).toEqual({
      location: "body.password",
      message: "expected length >= 8",
    });
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

describe("query parameters", () => {
  it("sends only the scoreboard params that were given", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ standings: [] }));

    await api.scoreboard({ bracket: 2, as_of: "2026-01-01T00:00:00Z" });

    expect(lastUrl()).toBe("/api/v1/scoreboard?bracket=2&as_of=2026-01-01T00%3A00%3A00Z");
  });

  it("omits the query string entirely when there is nothing to send", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ standings: [] }));

    await api.scoreboard();

    expect(lastUrl()).toBe("/api/v1/scoreboard");
  });
});

describe("multipart upload", () => {
  it("sends the file as `file`, with CSRF but without a hand-set Content-Type", async () => {
    setCsrfToken("tok-123");
    fetchMock.mockResolvedValue(jsonResponse({ id: 9, name: "a.zip" }, 201));

    await adminApi.uploadFile(4, new File(["bytes"], "a.zip"));

    expect(lastUrl()).toBe("/api/v1/admin/challenges/4/files");
    expect(lastHeaders()["CSRF-Token"]).toBe("tok-123");
    // The browser must pick the multipart boundary itself.
    expect(lastHeaders()["Content-Type"]).toBeUndefined();

    const form = lastInit().body as FormData;
    expect(form).toBeInstanceOf(FormData);
    expect(form.get("file")).toBeInstanceOf(File);
  });
});

describe("downloads", () => {
  it("returns bytes and the server's filename", async () => {
    fetchMock.mockResolvedValue(
      new Response("PK", {
        status: 200,
        headers: {
          "Content-Type": "application/zip",
          "Content-Disposition": 'attachment; filename="challenge.zip"',
        },
      }),
    );

    const file = await api.downloadFile(11);

    expect(lastUrl()).toBe("/api/v1/files/11");
    expect(file.filename).toBe("challenge.zip");
    expect(await file.blob.text()).toBe("PK");
  });

  it("still raises a problem document on a failed download", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ detail: "file not found" }, 404));

    const err = (await api.downloadFile(11).catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(404);
    expect(err.detail).toBe("file not found");
  });
});

describe("admin transport", () => {
  it("uses the same cookie and CSRF token, under the admin prefix", async () => {
    setCsrfToken("tok-123");
    fetchMock.mockResolvedValue(jsonResponse({ id: 1, name: "x", banned: true }));

    await adminApi.setUserBanned(5, true);

    expect(lastUrl()).toBe("/api/v1/admin/users/5/ban");
    expect(lastInit().method).toBe("PUT");
    expect(lastInit().credentials).toBe("include");
    expect(lastHeaders()["CSRF-Token"]).toBe("tok-123");
  });

  it("treats a 204 as an empty success, not a JSON parse failure", async () => {
    setCsrfToken("tok-123");
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await expect(adminApi.deleteChallenge(3)).resolves.toBeUndefined();
  });
});
