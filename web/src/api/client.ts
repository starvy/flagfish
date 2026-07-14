import type { components } from "./schema.gen";

type Schemas = components["schemas"];

export type Instance = Schemas["InstanceOutputBody"];
export type Session = Schemas["SessionOutputBody"];
export type Me = Schemas["MeOutputBody"];
export type ChallengeListItem = Schemas["ChallengeListItem"];
export type ChallengeDetail = Schemas["ChallengeDetailOutputBody"];
export type ChallengeSolve = Schemas["ChallengeSolve"];
export type AttemptResult = Schemas["AttemptOutputBody"];
export type UnlockResult = Schemas["UnlockOutputBody"];
export type Standing = Schemas["Standing"];
export type TokenListItem = Schemas["TokenListItem"];
export type CreatedToken = Schemas["CreateTokenOutputBody"];

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, detail: string) {
    super(detail);
    this.name = "ApiError";
    this.status = status;
  }
}

// The session cookie is httpOnly, so the CSRF token is the only auth state the
// SPA holds. sessionStorage lets it survive a reload; absent (tests, SSR) we
// fall back to memory only.
const CSRF_KEY = "flagfish.csrf";

let csrfToken: string | null = null;

function storage(): Storage | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null;
  }
}

export function getCsrfToken(): string | null {
  if (csrfToken === null) csrfToken = storage()?.getItem(CSRF_KEY) ?? null;
  return csrfToken;
}

export function setCsrfToken(token: string | null): void {
  csrfToken = token;
  const s = storage();
  if (!s) return;
  if (token === null) s.removeItem(CSRF_KEY);
  else s.setItem(CSRF_KEY, token);
}

// Installed once by the app shell; a 401 anywhere means the session is gone and
// the only useful destination is the login page.
let onUnauthorized: (() => void) | null = null;

export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn;
}

function isUnsafe(method: string): boolean {
  return !["GET", "HEAD", "OPTIONS"].includes(method);
}

interface RequestOpts {
  // Login and change-password answer 401 for bad credentials; that is the
  // form's error to show, not a dead session.
  localUnauthorized?: boolean;
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  opts?: RequestOpts,
): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (isUnsafe(method)) {
    const token = getCsrfToken();
    if (token !== null) headers["CSRF-Token"] = token;
  }

  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    credentials: "include",
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (!res.ok) {
    let detail = `${res.status} ${res.statusText}`;
    try {
      const problem = (await res.json()) as { detail?: string; title?: string };
      detail = problem.detail ?? problem.title ?? detail;
    } catch {
      // Not a problem document; keep the status line.
    }
    if (res.status === 401 && !opts?.localUnauthorized) onUnauthorized?.();
    throw new ApiError(res.status, detail);
  }

  return (await res.json()) as T;
}

function rememberSession(sess: Session): Session {
  setCsrfToken(sess.csrf_token);
  return sess;
}

export const api = {
  // Public branding: the anonymous theme + CTF name the shell needs before login.
  instance: () => request<Instance>("GET", "/instance"),

  register: (body: { name: string; email: string; password: string }) =>
    request<Session>("POST", "/register", body, { localUnauthorized: true }).then(rememberSession),

  login: (body: { email: string; password: string }) =>
    request<Session>("POST", "/login", body, { localUnauthorized: true }).then(rememberSession),

  logout: async () => {
    const out = await request<Schemas["ClearedOutputBody"]>("POST", "/logout");
    setCsrfToken(null);
    return out;
  },

  me: () => request<Me>("GET", "/me"),

  changePassword: (body: { current_password: string; new_password: string }) =>
    request<Session>("POST", "/me/password", body, { localUnauthorized: true }).then(
      rememberSession,
    ),

  challenges: () => request<Schemas["ChallengesOutputBody"]>("GET", "/challenges"),

  challenge: (id: number) => request<ChallengeDetail>("GET", `/challenges/${id}`),

  challengeSolves: (id: number) =>
    request<Schemas["SolvesOutputBody"]>("GET", `/challenges/${id}/solves`),

  attempt: (id: number, flag: string) =>
    request<AttemptResult>("POST", `/challenges/${id}/attempt`, { flag }),

  unlockHint: (challengeId: number, hintId: number) =>
    request<UnlockResult>("POST", `/challenges/${challengeId}/hints/${hintId}/unlock`),

  scoreboard: (limit?: number) =>
    request<Schemas["ScoreboardOutputBody"]>(
      "GET",
      limit === undefined ? "/scoreboard" : `/scoreboard?limit=${limit}`,
    ),

  tokens: () => request<Schemas["ListTokensOutputBody"]>("GET", "/tokens"),

  createToken: (body: { description?: string; ttl_hours?: number }) =>
    request<CreatedToken>("POST", "/tokens", body),

  deleteToken: (id: number) => request<Schemas["OkOutputBody"]>("DELETE", `/tokens/${id}`),
};
