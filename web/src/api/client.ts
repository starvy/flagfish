import type { components } from "./schema.gen";
import { toApiError } from "./errors";

type Schemas = components["schemas"];

/** A request body as the caller writes it: the generated schema minus Huma's `$schema` marker. */
type Body<T> = Omit<T, "$schema">;

export { ApiError, isApiError, type FieldError } from "./errors";

export type Instance = Schemas["InstanceOutputBody"];
export type Session = Schemas["SessionOutputBody"];
export type Me = Schemas["MeOutputBody"];
export type MeField = Schemas["MeFieldBody"];
export type ChallengeListItem = Schemas["ChallengeListItem"];
export type ChallengeDetail = Schemas["ChallengeDetailOutputBody"];
export type ChallengeSolve = Schemas["ChallengeSolve"];
export type AttemptResult = Schemas["AttemptOutputBody"];
export type UnlockResult = Schemas["UnlockOutputBody"];
export type Standing = Schemas["Standing"];
export type Scoreboard = Schemas["ScoreboardOutputBody"];
export type Brackets = Schemas["BracketsOutputBody"];
export type Team = Schemas["TeamBody"];
export type Notification = Schemas["NotificationBody"];
export type NotificationPage = Schemas["ListNotificationsOutputBody"];
export type TokenListItem = Schemas["TokenListItem"];
export type CreatedToken = Schemas["CreateTokenOutputBody"];
export type PageLink = Schemas["PageLink"];
export type PageContent = Schemas["PageOutputBody"];
export type UserProfile = Schemas["UserProfileBody"];
export type ProfileSolve = Schemas["ProfileSolveBody"];
export type ScoreHistory = Schemas["ScoreHistoryOutputBody"];
export type ScorePoint = Schemas["ScorePointBody"];

/** The server's verdict on a flag. A wrong flag is a 200 with `status: "incorrect"`, never an error. */
export type AttemptStatus = "correct" | "incorrect" | "already_solved";

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
  // Set once on the retry of a CSRF failure, so a server that keeps rejecting the
  // token cannot put us in a loop.
  retried?: boolean;
}

/**
 * Re-reads the session's CSRF token after the server rejected the one we hold.
 *
 * The token is a property of the session, so the only way to learn the current one is to ask.
 * `/me` is the cheapest authenticated read; it yields the token only if the server puts one in
 * the body, and if it does not we give up rather than retry blind.
 */
async function refreshCsrfToken(): Promise<boolean> {
  const res = await fetch("/api/v1/me", { method: "GET", credentials: "include" });
  if (!res.ok) return false;
  const body: unknown = await res.json().catch(() => null);
  if (typeof body !== "object" || body === null) return false;
  const token = (body as { csrf_token?: unknown }).csrf_token;
  if (typeof token !== "string" || token === "") return false;
  setCsrfToken(token);
  return true;
}

function csrfHeader(method: string): Record<string, string> {
  if (!isUnsafe(method)) return {};
  const token = getCsrfToken();
  return token === null ? {} : { "CSRF-Token": token };
}

function send(
  method: string,
  path: string,
  headers: Record<string, string>,
  body?: BodyInit,
): Promise<Response> {
  return fetch(`/api/v1${path}`, { method, headers, credentials: "include", body });
}

/**
 * The one transport. Every operation — public and admin — goes through here, which is what
 * keeps the cookie, the CSRF token and the split 401 rule from drifting apart.
 */
export async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  opts: RequestOpts = {},
): Promise<T> {
  const headers = csrfHeader(method);
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await send(method, path, headers, body === undefined ? undefined : JSON.stringify(body));

  if (!res.ok) {
    const err = await toApiError(res);
    // A stale token is recoverable, and recoverable exactly once: fetch the current one and
    // replay the write. Everything else about the 403 is the caller's to render.
    if (err.reason === "csrf" && !opts.retried && (await refreshCsrfToken())) {
      return request<T>(method, path, body, { ...opts, retried: true });
    }
    if (err.status === 401 && !opts.localUnauthorized) onUnauthorized?.();
    throw err;
  }

  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

/**
 * Multipart upload. The browser must set Content-Type itself so the multipart boundary matches
 * the body — setting it by hand produces a body the server cannot parse — but the write is
 * still cookie-authenticated and still carries the CSRF header.
 */
export async function upload<T>(
  method: string,
  path: string,
  form: FormData,
  opts: RequestOpts = {},
): Promise<T> {
  const res = await send(method, path, csrfHeader(method), form);

  if (!res.ok) {
    const err = await toApiError(res);
    if (err.reason === "csrf" && !opts.retried && (await refreshCsrfToken())) {
      return upload<T>(method, path, form, { ...opts, retried: true });
    }
    if (err.status === 401 && !opts.localUnauthorized) onUnauthorized?.();
    throw err;
  }

  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export interface DownloadedFile {
  blob: Blob;
  filename: string | null;
}

/** A file response is bytes, not JSON — but a failed one is still a problem document. */
export async function downloadBlob(path: string): Promise<DownloadedFile> {
  const res = await send("GET", path, {});
  if (!res.ok) {
    const err = await toApiError(res);
    if (err.status === 401) onUnauthorized?.();
    throw err;
  }
  return { blob: await res.blob(), filename: filenameOf(res) };
}

function filenameOf(res: Response): string | null {
  const cd = res.headers.get("Content-Disposition");
  if (cd === null) return null;
  const star = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  if (star) return decodeURIComponent(star[1]);
  const plain = /filename="?([^";]+)"?/i.exec(cd);
  return plain ? plain[1] : null;
}

export function query(params: Record<string, string | number | boolean | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined) q.set(k, String(v));
  }
  const s = q.toString();
  return s === "" ? "" : `?${s}`;
}

function rememberSession(sess: Session): Session {
  setCsrfToken(sess.csrf_token);
  return sess;
}

export interface ScoreboardParams {
  limit?: number;
  /** Standings as they stood at this instant (RFC3339) — how a frozen board is read. */
  as_of?: string;
  /** Admin only: see through the freeze. */
  preview?: boolean;
  bracket?: number;
}

export interface NotificationsParams {
  page?: number;
  per_page?: number;
}

export const api = {
  // Public branding: the anonymous theme + CTF name the shell needs before login.
  instance: () => request<Instance>("GET", "/instance"),

  register: (body: Body<Schemas["RegisterInputBody"]>) =>
    request<Session>("POST", "/register", body, { localUnauthorized: true }).then(rememberSession),

  login: (body: Body<Schemas["LoginInputBody"]>) =>
    request<Session>("POST", "/login", body, { localUnauthorized: true }).then(rememberSession),

  logout: async () => {
    const out = await request<Schemas["ClearedOutputBody"]>("POST", "/logout");
    setCsrfToken(null);
    return out;
  },

  me: () => request<Me>("GET", "/me"),

  // The custom fields the registration form renders, before an account exists.
  registrationFields: () =>
    request<Schemas["RegistrationFieldsOutputBody"]>("GET", "/register/fields"),

  // Answer or edit the caller's own custom fields. A required field created after sign-up is
  // answerable here even when it is not otherwise editable — the profile-gate remedy.
  answerFields: (body: Body<Schemas["AnswerFieldsInputBody"]>) =>
    request<Schemas["AnswerFieldsOutputBody"]>("PUT", "/me/fields", body),

  // Profile PATCH semantics: an omitted key keeps, an explicit null clears, a value sets.
  updateMe: (body: Body<Schemas["UpdateMeInputBody"]>) => request<Me>("PATCH", "/me", body),

  changeName: (body: Body<Schemas["ChangeNameInputBody"]>) => request<Me>("PATCH", "/me/name", body),

  changeEmail: (body: Body<Schemas["ChangeEmailInputBody"]>) => request<Me>("PATCH", "/me/email", body),

  confirmEmailChange: (body: Body<Schemas["ConfirmEmailInputBody"]>) =>
    request<Schemas["OkOutputBody"]>("POST", "/verify/email-change", body, { localUnauthorized: true }),

  userProfile: (id: number) => request<UserProfile>("GET", `/users/${id}`),

  changePassword: (body: Body<Schemas["ChangePasswordInputBody"]>) =>
    request<Session>("POST", "/me/password", body, { localUnauthorized: true }).then(rememberSession),

  resetRequest: (body: Body<Schemas["RequestResetInputBody"]>) =>
    request<Schemas["OkOutputBody"]>("POST", "/reset-password", body, { localUnauthorized: true }),

  // A reset also deletes the account's API tokens, and the response says how many — the user is
  // the only one who can mint replacements, so they have to be told that they need to.
  resetApply: (body: Body<Schemas["ResetPasswordInputBody"]>) =>
    request<Schemas["RevokedOutputBody"]>("PATCH", "/reset-password", body, {
      localUnauthorized: true,
    }),

  verifyResend: () => request<Schemas["OkOutputBody"]>("POST", "/verify/resend"),

  verifyConfirm: (body: Body<Schemas["ConfirmEmailInputBody"]>) =>
    request<Schemas["OkOutputBody"]>("POST", "/verify/confirm", body, { localUnauthorized: true }),

  challenges: () => request<Schemas["ChallengesOutputBody"]>("GET", "/challenges"),

  challenge: (id: number) => request<ChallengeDetail>("GET", `/challenges/${id}`),

  challengeSolves: (id: number, cursor?: string) =>
    request<Schemas["SolvesOutputBody"]>("GET", `/challenges/${id}/solves${query({ cursor })}`),

  attempt: (id: number, flag: string) =>
    request<AttemptResult>("POST", `/challenges/${id}/attempt`, { flag }),

  unlockHint: (challengeId: number, hintId: number) =>
    request<UnlockResult>("POST", `/challenges/${challengeId}/hints/${hintId}/unlock`),

  downloadFile: (id: number) => downloadBlob(`/files/${id}`),

  scoreboard: (params: ScoreboardParams = {}) =>
    request<Scoreboard>("GET", `/scoreboard${query({ ...params })}`),

  brackets: () => request<Brackets>("GET", "/brackets"),

  // One account's cumulative score over time. The id is a team in teams mode, a user in users mode;
  // a hidden, banned or frozen-out account answers with empty points, never a leak.
  scoreHistory: (id: number) => request<ScoreHistory>("GET", `/scoreboard/${id}`),

  notifications: (params: NotificationsParams = {}) =>
    request<NotificationPage>("GET", `/notifications${query({ ...params })}`),

  createTeam: (body: Body<Schemas["CreateTeamInputBody"]>) => request<Team>("POST", "/teams", body),

  joinTeam: (body: Body<Schemas["JoinTeamInputBody"]>) => request<Team>("POST", "/teams/join", body),

  // A 404 here is "you have no team" — an enrollment state, not a failure.
  myTeam: () => request<Team>("GET", "/me/team"),

  // Captain only; the server enforces captaincy in the write itself.
  updateMyTeam: (body: Body<Schemas["UpdateMyTeamInputBody"]>) =>
    request<Team>("PATCH", "/me/team", body),

  leaveTeam: () => request<Schemas["LeftTeamOutputBody"]>("POST", "/me/team/leave"),

  // Captain-only roster controls; the server enforces captaincy in each write itself.
  kickMember: (userId: number) => request<Team>("DELETE", `/me/team/members/${userId}`),

  transferCaptaincy: (body: Body<Schemas["TransferCaptainInputBody"]>) =>
    request<Team>("PUT", "/me/team/captain", body),

  disbandTeam: () => request<Schemas["LeftTeamOutputBody"]>("DELETE", "/me/team"),

  team: (id: number) => request<Team>("GET", `/teams/${id}`),

  tokens: () => request<Schemas["ListTokensOutputBody"]>("GET", "/tokens"),

  createToken: (body: Body<Schemas["CreateTokenInputBody"]>) =>
    request<CreatedToken>("POST", "/tokens", body),

  deleteToken: (id: number) => request<Schemas["OkOutputBody"]>("DELETE", `/tokens/${id}`),

  // Content pages. `pages` is the published list behind the nav; `page` is one page by its slug,
  // and its 403/404 are the ClassPages gate — an auth-gated page to an anonymous caller, a draft
  // or missing slug to anyone.
  pages: () => request<Schemas["ListPagesOutputBody"]>("GET", "/pages"),

  page: (route: string) =>
    request<PageContent>("GET", `/pages/${encodeURIComponent(route)}`),
};
