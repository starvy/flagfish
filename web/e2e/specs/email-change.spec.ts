import { test, expect, registerPlayer } from "../fixtures";
import { latestEmailToken, liveEmailOf } from "../db";

// Self-service email change: the live address only moves after a token delivered to the NEW address
// is confirmed. The token is read from the queued mail (sent to the new address), then confirmed
// through the browser.

test("a player changes their email after confirming the new address", async ({ browser }) => {
  const s = Date.now();
  const oldEmail = `mail-old-${s}@example.com`;
  const newEmail = `mail-new-${s}@example.com`;

  const { context, page } = await registerPlayer(browser, {
    name: `Mailer ${s}`,
    email: oldEmail,
    password: "MailPass1234",
  });

  await page.goto("/settings?tab=profile");
  const userId = Number(await page.locator("dd .ff-mono").first().innerText());

  // Request the change. The live address must not move yet.
  await page.getByLabel("Email").fill(newEmail);
  await page.getByRole("button", { name: "Change email" }).click();
  await expect(page.getByText("Email change pending")).toBeVisible();
  expect(await liveEmailOf(userId)).toBe(oldEmail);

  // The confirmation code was mailed to the NEW address; confirm it in the browser.
  const token = await latestEmailToken(newEmail);
  await page.goto(`/confirm-email?token=${token}`);
  await page.waitForURL((url) => url.pathname.startsWith("/settings"));

  // The live address has moved — in the UI and in the database.
  await expect(page.getByText(`Current: ${newEmail}`)).toBeVisible();
  expect(await liveEmailOf(userId)).toBe(newEmail);

  await context.close();
});
