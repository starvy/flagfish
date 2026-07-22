import { test, expect, registerPlayer, loginViaUI } from "../fixtures";
import type { Page } from "@playwright/test";

// Forced password change. An organiser flags a credential as suspect; the player is sent to
// /change-password (not looped through login) and, once changed, reaches the app.

async function forcePasswordChange(adminPage: Page, email: string, name: string) {
  await adminPage.goto("/admin/users");
  await adminPage.getByLabel("Search field").selectOption("email");
  await adminPage.getByPlaceholder("Search…").fill(email);
  await adminPage.getByRole("button", { name: "Search" }).click();
  const row = adminPage.getByRole("row").filter({ hasText: email });
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: "Force new password" }).click();
  const dialog = adminPage.getByRole("dialog");
  await dialog.getByLabel(/Name of the user/).fill(name);
  await dialog.getByRole("button", { name: "Force new password" }).click();
  await expect(adminPage.getByRole("dialog")).toBeHidden();
}

test("a forced player is walled to /change-password and can exit it", async ({ browser, adminPage }) => {
  const s = Date.now();
  const name = `Forced ${s}`;
  const email = `forced-${s}@example.com`;
  const oldPassword = "ForcedOld123";
  const newPassword = "ForcedNew456";

  const player = await registerPlayer(browser, {
    name,
    email,
    password: oldPassword,
    team: { name: `ForcedTeam ${s}`, password: "ForcedTeamPw1" },
  });
  await player.context.close();

  await forcePasswordChange(adminPage, email, name);

  // A clean browser (no stale cookie): the forced credential logs in, and the wall routes it
  // straight to the change form rather than back to login.
  const context = await browser.newContext();
  const page = await context.newPage();
  await loginViaUI(page, email, oldPassword);
  await page.waitForURL((url) => url.pathname === "/change-password");
  await expect(page.getByRole("heading", { name: "Choose a new password" })).toBeVisible();

  // Setting a new password lifts the wall and drops the player into the app.
  await page.getByLabel(/^Current password/).fill(oldPassword);
  await page.getByLabel(/^New password/).fill(newPassword);
  await page.getByLabel(/^Confirm new password/).fill(newPassword);
  await page.getByRole("button", { name: "Change password" }).click();
  await page.waitForURL((url) => url.pathname === "/challenges");
  expect(new URL(page.url()).pathname).toBe("/challenges");

  await context.close();
});

// The same player recovers in the SAME browser. Forcing a password deletes their sessions, leaving a
// now-dead session cookie in the browser. That stale cookie must degrade to anonymous rather than
// hard-fail the credential routes with a 401 — otherwise POST /login is rejected and the player is
// stuck in a login→login loop. This guards that a killed session recovers from its own browser.
test("a forced player recovers in the same browser (dead session cookie)", async ({
  browser,
  adminPage,
}) => {
  const s = Date.now();
  const name = `Forced Same ${s}`;
  const email = `forced-same-${s}@example.com`;
  const oldPassword = "ForcedOld123";
  const newPassword = "ForcedNew456";

  const player = await registerPlayer(browser, {
    name,
    email,
    password: oldPassword,
    team: { name: `ForcedSameTeam ${s}`, password: "ForcedTeamPw1" },
  });

  await forcePasswordChange(adminPage, email, name);

  // Same context — the killed session's cookie is still here.
  await loginViaUI(player.page, email, oldPassword);
  await player.page.waitForURL((url) => url.pathname === "/change-password");
  await player.page.getByLabel(/^Current password/).fill(oldPassword);
  await player.page.getByLabel(/^New password/).fill(newPassword);
  await player.page.getByLabel(/^Confirm new password/).fill(newPassword);
  await player.page.getByRole("button", { name: "Change password" }).click();
  await player.page.waitForURL((url) => url.pathname === "/challenges");

  await player.context.close();
});
