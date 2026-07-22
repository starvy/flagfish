import { test, expect, registerPlayer, loginViaUI } from "../fixtures";
import type { Browser, Page } from "@playwright/test";

// Ban walls. A banned user and a banned team are both cut off from gameplay; the account is never
// deleted, and an unban restores access.
//
// Note: an admin ban deletes the target's sessions, so the affected player's live tab holds a dead
// cookie. That dead cookie is a separate concern (see forced-password-change), so the credential /
// team semantics here are checked from FRESH browser contexts — a login with no stale cookie — which
// is what actually exercises "is this credential allowed in?".

async function findUserRow(adminPage: Page, email: string) {
  await adminPage.goto("/admin/users");
  await adminPage.getByLabel("Search field").selectOption("email");
  await adminPage.getByPlaceholder("Search…").fill(email);
  await adminPage.getByRole("button", { name: "Search" }).click();
  const row = adminPage.getByRole("row").filter({ hasText: email });
  await expect(row).toBeVisible();
  return row;
}

async function findTeamRow(adminPage: Page, teamName: string) {
  await adminPage.goto("/admin/teams");
  await adminPage.getByPlaceholder("Search…").fill(teamName);
  await adminPage.getByRole("button", { name: "Search" }).click();
  const row = adminPage.getByRole("row").filter({ hasText: teamName });
  await expect(row).toBeVisible();
  return row;
}

// Where a navigation to the challenges settles. `/challenges` is the URL the moment the goto lands,
// before the auth guard has decided anything, so waiting on the URL alone would race the wall's
// client-side redirect. Instead wait for a real terminal state: the challenges board rendered, or a
// bounce to /login or /team. A live notification stream means the page never goes network-idle, so
// element/URL conditions are the only reliable signal.
async function reachable(page: Page): Promise<string> {
  await page.goto("/challenges", { waitUntil: "domcontentloaded" });
  await page.waitForFunction(
    () => {
      const p = location.pathname;
      if (p === "/login" || p === "/team") return true;
      // The board only renders once the guard has admitted the session.
      return p === "/challenges" && document.querySelector(".page-head h1") !== null;
    },
    undefined,
    { timeout: 15000 },
  );
  return new URL(page.url()).pathname;
}

// Submit the login form in a fresh context and return the page wherever it lands — without asserting
// success, because a walled credential is supposed to fail to reach gameplay.
async function attemptLogin(browser: Browser, email: string, password: string): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/login");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  const done = page.waitForResponse(
    (r) => r.url().endsWith("/api/v1/login") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Sign in" }).click();
  await done;
  return page;
}

test("a banned user is walled and an unban restores access", async ({ browser, adminPage }) => {
  const s = Date.now();
  const name = `Banned ${s}`;
  const email = `banned-${s}@example.com`;
  const password = "BannedPass123";

  const player = await registerPlayer(browser, {
    name,
    email,
    password,
    team: { name: `BannedTeam ${s}`, password: "BannedTeamPw1" },
  });
  expect(await reachable(player.page)).toBe("/challenges");

  // Ban.
  const row = await findUserRow(adminPage, email);
  await row.getByRole("button", { name: "Ban", exact: true }).click();
  const dialog = adminPage.getByRole("dialog");
  await dialog.getByLabel(/Name of the user/).fill(name);
  await dialog.getByRole("button", { name: "Ban", exact: true }).click();
  await expect(adminPage.getByRole("dialog")).toBeHidden();

  // The live session is void and the credential cannot re-authenticate from a clean browser either.
  expect(await reachable(player.page)).toBe("/login");
  const bannedTry = await attemptLogin(browser, email, password);
  await expect(bannedTry.getByRole("alert")).toBeVisible();
  expect(new URL(bannedTry.url()).pathname).toBe("/login");
  await bannedTry.context().close();

  // Unban restores it — the account was never deleted.
  const row2 = await findUserRow(adminPage, email);
  await row2.getByRole("button", { name: "Unban" }).click();
  const dialog2 = adminPage.getByRole("dialog");
  await dialog2.getByLabel(/Name of the user/).fill(name);
  await dialog2.getByRole("button", { name: "Unban" }).click();
  await expect(adminPage.getByRole("dialog")).toBeHidden();

  const restored = await browser.newContext();
  const restoredPage = await restored.newPage();
  await loginViaUI(restoredPage, email, password);
  expect(await reachable(restoredPage)).toBe("/challenges");

  await restored.close();
  await player.context.close();
});

test("a banned team walls its members and an unban restores them", async ({ browser, adminPage }) => {
  const s = Date.now();
  const teamName = `WalledTeam ${s}`;
  const email = `capt-${s}@example.com`;
  const password = "CaptPass1234";

  const captain = await registerPlayer(browser, {
    name: `Captain ${s}`,
    email,
    password,
    team: { name: teamName, password: "WalledTeamPw1" },
  });
  expect(await reachable(captain.page)).toBe("/challenges");

  // Ban the team.
  const row = await findTeamRow(adminPage, teamName);
  await row.getByRole("button", { name: "Ban", exact: true }).click();
  const dialog = adminPage.getByRole("dialog");
  await dialog.getByLabel(/Name of the team/).fill(teamName);
  await dialog.getByRole("button", { name: "Ban team" }).click();
  await expect(adminPage.getByRole("dialog")).toBeHidden();

  // The member's session is void; a clean re-login gets a valid session but the team wall still
  // bounces it away from gameplay (the credential is fine — the team is not).
  expect(await reachable(captain.page)).toBe("/login");
  const walled = await attemptLogin(browser, email, password);
  expect(await reachable(walled)).toBe("/login");
  await walled.context().close();

  // Unban and the team plays again.
  const row2 = await findTeamRow(adminPage, teamName);
  await row2.getByRole("button", { name: "Unban" }).click();
  const dialog2 = adminPage.getByRole("dialog");
  await dialog2.getByLabel(/Name of the team/).fill(teamName);
  await dialog2.getByRole("button", { name: "Unban team" }).click();
  await expect(adminPage.getByRole("dialog")).toBeHidden();

  const restored = await browser.newContext();
  const restoredPage = await restored.newPage();
  await loginViaUI(restoredPage, email, password);
  expect(await reachable(restoredPage)).toBe("/challenges");

  await restored.close();
  await captain.context.close();
});
