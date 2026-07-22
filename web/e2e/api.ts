import type { Page } from "@playwright/test";

/**
 * Challenge authoring, seeded through the real admin HTTP API.
 *
 * Every gameplay/scoreboard/hint/prereq/tag flow needs challenges to exist before the player-facing
 * part — the surface actually under test — can run. We seed them out of band, driving the exact
 * admin endpoints, session cookie and CSRF token the editor itself uses from inside the signed-in
 * admin page. This is scenario setup, not the surface under test.
 */

interface ApiArgs {
  method: string;
  path: string;
  body: unknown;
}

// Runs in the page's origin so the session cookie rides along. The CSRF token is read from the SPA's
// own store (where its transport keeps it) rather than fetching /me per call: a whole suite of admin
// writes issuing a /me each would exhaust the account's per-route request budget and start coming
// back 429 — a 429 has no token, and the write that follows then fails the CSRF check. On a 403
// (a token that briefly lagged a fresh login) re-read and retry a couple of times before giving up.
async function call<T>(page: Page, method: string, path: string, body?: unknown): Promise<T> {
  return page.evaluate<T, ApiArgs>(
    async ({ method, path, body }) => {
      const attempt = () => {
        const headers: Record<string, string> = {
          "CSRF-Token": sessionStorage.getItem("flagfish.csrf") ?? "",
        };
        if (body !== undefined) headers["Content-Type"] = "application/json";
        return fetch(`/api/v1${path}`, {
          method,
          credentials: "include",
          headers,
          body: body === undefined ? undefined : JSON.stringify(body),
        });
      };
      let res = await attempt();
      for (let i = 0; i < 3 && res.status === 403; i++) {
        await new Promise((r) => setTimeout(r, 150));
        res = await attempt();
      }
      const text = await res.text();
      if (!res.ok) throw new Error(`${method} ${path} -> ${res.status} ${text}`);
      return (text === "" ? null : JSON.parse(text)) as T;
    },
    { method, path, body },
  );
}

export interface SeedChallenge {
  name: string;
  category: string;
  value: number;
  description?: string;
  maxAttempts?: number;
  state?: "visible" | "hidden";
}

interface AdminChallengeResult {
  id: number;
  name: string;
}

export async function seedChallenge(page: Page, opts: SeedChallenge): Promise<number> {
  const out = await call<AdminChallengeResult>(page, "POST", "/admin/challenges", {
    name: opts.name,
    category: opts.category,
    value: opts.value,
    state: opts.state ?? "visible",
    function: "static",
    max_attempts: opts.maxAttempts ?? 0,
    ...(opts.description === undefined ? {} : { description: opts.description }),
  });
  return out.id;
}

/** Patches a challenge — used to produce a before/after row in the audit trail. */
export async function updateChallenge(
  page: Page,
  id: number,
  body: Record<string, unknown>,
): Promise<void> {
  await call(page, "PATCH", `/admin/challenges/${id}`, body);
}

export interface SeedFlag {
  type?: "static" | "regex";
  content: string;
  caseInsensitive?: boolean;
}

export async function seedFlag(page: Page, challengeId: number, flag: SeedFlag): Promise<void> {
  await call(page, "POST", `/admin/challenges/${challengeId}/flags`, {
    type: flag.type ?? "static",
    content: flag.content,
    case_insensitive: flag.caseInsensitive ?? false,
  });
}

export interface SeedHint {
  content: string;
  cost: number;
  position: number;
  title?: string;
  prerequisites?: number[];
}

interface AdminHintResult {
  id: number;
}

export async function seedHint(page: Page, challengeId: number, hint: SeedHint): Promise<number> {
  const out = await call<AdminHintResult>(page, "POST", `/admin/challenges/${challengeId}/hints`, {
    content: hint.content,
    cost: hint.cost,
    position: hint.position,
    ...(hint.title === undefined ? {} : { title: hint.title }),
    ...(hint.prerequisites === undefined ? {} : { prerequisites: hint.prerequisites }),
  });
  return out.id;
}

export async function seedRequirements(
  page: Page,
  challengeId: number,
  prerequisites: number[],
  visibility: "hidden" | "masked" | "preview",
): Promise<void> {
  await call(page, "PUT", `/admin/challenges/${challengeId}/requirements`, {
    prerequisites,
    visibility,
  });
}

export async function seedTag(page: Page, challengeId: number, value: string): Promise<void> {
  await call(page, "POST", `/admin/challenges/${challengeId}/tags`, { value });
}

/** Flips the fleet-wide pause switch through the admin config API. */
export async function setPaused(page: Page, paused: boolean): Promise<void> {
  await call(page, "PATCH", "/admin/config", { paused });
}

/** Patches instance config (the clock, visibility, …) through the admin config API. */
export async function patchConfig(page: Page, body: Record<string, unknown>): Promise<void> {
  await call(page, "PATCH", "/admin/config", body);
}
