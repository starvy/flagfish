import { test, expect, registerPlayer, loginViaUI } from "../fixtures";
import { latestEmailToken } from "../db";

// Forgot-password → reset. The token never exists in plaintext in the DB, so the test pulls it from
// the queued reset email (river_job args) — the same code a real inbox would receive.

test("a player resets their password and the old one stops working", async ({ browser }) => {
  const s = Date.now();
  const email = `reset-${s}@example.com`;
  const oldPassword = "OldPass1234";
  const newPassword = "NewPass5678";

  const player = await registerPlayer(browser, { name: `Reset ${s}`, email, password: oldPassword });
  await player.context.close(); // reset is done signed-out, from a fresh browser

  const context = await browser.newContext();
  const page = await context.newPage();

  // Request the link.
  await page.goto("/forgot-password");
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Send the reset link" }).click();
  await expect(page.getByRole("heading", { name: "Check your inbox" })).toBeVisible();

  // Pull the token the server mailed and complete the reset in the browser.
  const token = await latestEmailToken(email);
  await page.goto(`/reset-password?token=${token}`);
  // Anchored regex: the required asterisk is part of the label's accessible name ("New password *"),
  // and plain "New password" would also match "Confirm new password".
  await page.getByLabel(/^New password/).fill(newPassword);
  await page.getByLabel(/^Confirm new password/).fill(newPassword);
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(page.getByRole("heading", { name: "Password changed" })).toBeVisible();

  // The new password signs in.
  await loginViaUI(page, email, newPassword);
  expect(new URL(page.url()).pathname).not.toBe("/login");

  // The old password does not.
  const other = await browser.newContext();
  const otherPage = await other.newPage();
  await otherPage.goto("/login");
  await otherPage.getByLabel("Email").fill(email);
  await otherPage.getByLabel("Password").fill(oldPassword);
  await otherPage.getByRole("button", { name: "Sign in" }).click();
  await expect(otherPage.getByRole("alert")).toBeVisible();
  expect(new URL(otherPage.url()).pathname).toBe("/login");

  await other.close();
  await context.close();
});
