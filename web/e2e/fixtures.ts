import { test as base, expect, type Browser, type BrowserContext, type Page } from "@playwright/test";

// The seeded admin. up.sh creates this identical account on both the teams and the users instance,
// so the defaults below let `up.sh && npm run e2e` run without exporting anything. Overridable so
// the same specs can point at a differently-seeded instance.
export const ADMIN_EMAIL = process.env.FLAGFISH_E2E_ADMIN_EMAIL ?? "admin@example.com";
export const ADMIN_PASSWORD = process.env.FLAGFISH_E2E_ADMIN_PASSWORD ?? "AdminPass123!";

/** A short, collision-proof suffix so reruns and sibling specs never share a name or an email. */
export function uniq(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
}

// ---------------------------------------------------------------------------- teams

/**
 * The join password for a team addressed only by name. Derived so every player naming the same team
 * agrees on the secret, and long enough to clear the server's 8-character minimum.
 */
export function teamJoinPassword(team: string): string {
  return `join-${team}-secret`.toLowerCase().replace(/\s+/g, "-");
}

/** A constant team join password for specs that do not care about the value. */
export const TEAM_PASSWORD = "team-secret-123";

// ---------------------------------------------------------------------------- auth

/** Sign a page's context in through the real login form; returns once it has left /login. */
export async function login(page: Page, email: string, password: string): Promise<void> {
  await page.goto("/login");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  // The login lands somewhere inside the authed app (players on the board, an admin in the console).
  await page.waitForURL((url) => !url.pathname.startsWith("/login"));
}

/** Historical alias — some specs import the "ViaUI" spelling. */
export const loginViaUI = login;

// ---------------------------------------------------------------------------- team helpers

/** Create a team from the enrollment page; the creator becomes its captain. */
export async function createTeamViaUI(page: Page, name: string, password: string): Promise<void> {
  await page.goto("/team");
  const card = page
    .locator(".ff-card")
    .filter({ has: page.getByRole("heading", { name: "Create a team" }) });
  await card.getByLabel("Team name").fill(name);
  await card.getByLabel("Join password").fill(password);
  const [res] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith("/api/v1/teams") && r.request().method() === "POST"),
    card.getByRole("button", { name: "Create team" }).click(),
  ]);
  if (!res.ok()) throw new Error(`create team failed: ${res.status()} ${await res.text()}`);
  await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();
}

/** Join an existing team from the enrollment page by name + shared password. */
export async function joinTeamViaUI(page: Page, name: string, password: string): Promise<void> {
  await page.goto("/team");
  const card = page
    .locator(".ff-card")
    .filter({ has: page.getByRole("heading", { name: "Join a team" }) });
  await card.getByLabel("Team name").fill(name);
  await card.getByLabel("Join password").fill(password);
  const [res] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/teams/join") && r.request().method() === "POST"),
    card.getByRole("button", { name: "Join team" }).click(),
  ]);
  if (!res.ok()) throw new Error(`join team failed: ${res.status()} ${await res.text()}`);
  await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();
}

/**
 * Land on a team named only by string: the first caller creates it (as captain), the rest join. The
 * password is derived from the name so every caller agrees without passing it around.
 */
async function joinOrCreateTeam(page: Page, team: string): Promise<void> {
  const password = teamJoinPassword(team);
  const memberHeading = page.getByRole("heading", { name: team, exact: true, level: 1 });

  await page.goto("/team");
  // networkidle is unusable here: the SPA holds an SSE stream open, so the network never goes idle.
  await Promise.race([
    memberHeading.waitFor({ state: "visible" }),
    page.getByRole("heading", { name: "Join a team" }).waitFor({ state: "visible" }),
  ]);
  if (await memberHeading.isVisible().catch(() => false)) return;

  // Try to join first; if the team does not exist yet, fall back to creating it.
  const joinCard = page
    .locator(".ff-card")
    .filter({ has: page.getByRole("heading", { name: "Join a team" }) });
  await joinCard.getByLabel("Team name").fill(team);
  await joinCard.getByLabel("Join password").fill(password);
  const [joinRes] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/teams/join") && r.request().method() === "POST"),
    joinCard.getByRole("button", { name: "Join team" }).click(),
  ]);
  if (joinRes.ok()) {
    await expect(memberHeading).toBeVisible();
    return;
  }
  await createTeamViaUI(page, team, password);
}

// ---------------------------------------------------------------------------- players

/** A team to create or join, named directly or spelled out with an explicit join intent. */
export type TeamOption = string | { name: string; password?: string; join?: boolean };

export interface PlayerOptions {
  name: string;
  email: string;
  password: string;
  team?: TeamOption;
}

export interface Player {
  context: BrowserContext;
  page: Page;
  name: string;
  email: string;
  password: string;
  team?: TeamOption;
}

async function enrollTeam(page: Page, team: TeamOption): Promise<void> {
  if (typeof team === "string") {
    await joinOrCreateTeam(page, team);
    return;
  }
  const password = team.password ?? teamJoinPassword(team.name);
  if (team.join) await joinTeamViaUI(page, team.name, password);
  else await createTeamViaUI(page, team.name, password);
}

/**
 * Register a fresh player in its own browser context, so several players coexist in one test without
 * sharing a session. When `team` is given the player also lands on a team — created (as captain) or
 * joined — because in teams mode a teamless player is walled out of the challenges.
 */
export async function registerPlayer(browser: Browser, opts: PlayerOptions): Promise<Player> {
  const context = await browser.newContext();
  const page = await context.newPage();

  await page.goto("/register");
  await page.getByLabel("Name").fill(opts.name);
  await page.getByLabel("Email").fill(opts.email);
  await page.getByLabel("Password").fill(opts.password);
  await page.getByRole("button", { name: "Create account" }).click();
  // Registration signs the browser in and lands on the board in both account models.
  await page.waitForURL("**/challenges");

  if (opts.team !== undefined) await enrollTeam(page, opts.team);

  return { context, page, name: opts.name, email: opts.email, password: opts.password, team: opts.team };
}

/** Registers a player with a generated identity, returning both the page and the identity. */
export async function registerFreshPlayer(
  browser: Browser,
  prefix = "player",
): Promise<{ page: Page; player: { name: string; email: string; password: string } }> {
  const player = {
    name: uniq(prefix),
    email: `${uniq(prefix)}@example.com`,
    password: "PlayerPass123!",
  };
  const { page } = await registerPlayer(browser, player);
  return { page, player };
}

// ---------------------------------------------------------------------------- gameplay

/** Submit a flag on the player challenge page (already open) and return once the request is sent. */
export async function submitFlag(page: Page, flag: string): Promise<void> {
  await page.getByLabel("flag", { exact: true }).fill(flag);
  await page.getByRole("button", { name: "submit" }).click();
}

// ---------------------------------------------------------------------------- admin authoring

/** The SPA stores its per-session CSRF token in sessionStorage and echoes it as a request header. */
export async function csrfHeaders(page: Page): Promise<Record<string, string>> {
  const token = await page.evaluate(() => sessionStorage.getItem("flagfish.csrf"));
  return token === null ? {} : { "CSRF-Token": token };
}

export interface ChallengeSpec {
  name: string;
  category?: string;
  value?: number;
  flag: string;
}

/**
 * Create a challenge with a single static flag through the admin REST API, returning its numeric id.
 * Authoring is scenario setup for the player-facing flows under test, so it goes through the exact
 * endpoints (and CSRF token) the editor itself calls rather than clicking through the editor UI.
 */
export async function createChallengeWithFlag(admin: Page, spec: ChallengeSpec): Promise<number> {
  const headers = await csrfHeaders(admin);
  const create = await admin.request.post("/api/v1/admin/challenges", {
    headers,
    data: { name: spec.name, category: spec.category ?? "misc", value: spec.value ?? 100 },
  });
  if (!create.ok()) throw new Error(`create challenge failed: ${create.status()} ${await create.text()}`);
  const challenge = (await create.json()) as { id: number };

  const flag = await admin.request.post(`/api/v1/admin/challenges/${challenge.id}/flags`, {
    headers,
    data: { type: "static", content: spec.flag, case_insensitive: false },
  });
  if (!flag.ok()) throw new Error(`add flag failed: ${flag.status()} ${await flag.text()}`);
  return challenge.id;
}

/**
 * Create a challenge through the admin editor UI (Details form, then the Flags tab). Kept for specs
 * that want to exercise the authoring surface itself rather than seed through the API.
 */
export async function createChallenge(
  adminPage: Page,
  spec: { name: string; category: string; value: number; flag: string },
): Promise<void> {
  await adminPage.goto("/admin/challenges");
  await adminPage.getByRole("link", { name: "New challenge" }).first().click();
  await adminPage.getByRole("button", { name: "Create challenge" }).waitFor({ state: "visible" });

  await adminPage.getByRole("textbox", { name: "Name", exact: true }).fill(spec.name);
  await adminPage.getByRole("textbox", { name: "Category", exact: true }).fill(spec.category);
  await adminPage.getByRole("spinbutton", { name: "Value", exact: true }).fill(String(spec.value));
  await adminPage.getByRole("button", { name: "Create challenge" }).click();
  await adminPage.getByRole("button", { name: "Save", exact: true }).waitFor({ state: "visible" });

  await adminPage.getByRole("tab", { name: "Flags" }).click();
  await adminPage.getByRole("textbox", { name: "Flag", exact: true }).fill(spec.flag);
  await adminPage.getByRole("button", { name: "Add flag" }).click();
  await expect(adminPage.getByText(spec.flag, { exact: true })).toBeVisible();
}

// ---------------------------------------------------------------------------- fixtures

/** A logged-in player minted through the real registration form, bound to the fixture's page. */
export interface PlayerSession {
  page: Page;
  name: string;
  email: string;
}

interface Fixtures {
  /** A page already signed in as the seeded admin, in its own context. */
  adminPage: Page;
  /** Mint a brand-new teamless player on the fixture's page, the same way the UI does. */
  registerPlayer: () => Promise<PlayerSession>;
}

export const test = base.extend<Fixtures>({
  adminPage: async ({ browser }, use) => {
    const context = await browser.newContext();
    const page = await context.newPage();
    await login(page, ADMIN_EMAIL, ADMIN_PASSWORD);
    await use(page);
    await context.close();
  },

  registerPlayer: async ({ page }, use) => {
    await use(async () => {
      const stamp = uniq("player");
      const name = `Player ${stamp}`;
      const email = `${stamp}@example.com`;
      await page.goto("/register");
      await page.getByLabel("Name").fill(name);
      await page.getByLabel("Email").fill(email);
      await page.getByLabel("Password").fill("correct horse battery");
      await page.getByRole("button", { name: "Create account" }).click();
      await page.waitForURL("**/challenges");
      return { page, name, email };
    });
  },
});

export { expect };
