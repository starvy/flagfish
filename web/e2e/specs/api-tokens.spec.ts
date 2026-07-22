import { test, expect, registerPlayer } from "../fixtures";

// API tokens: minted once, usable as a real bearer credential, killed by an explicit revoke AND —
// the recently-fixed security property this pins — by a password change.

async function mintToken(page: import("@playwright/test").Page, description: string): Promise<string> {
  await page.goto("/settings?tab=tokens");
  // An account with no tokens shows both a header and an empty-state "New token" button; either mints.
  await page.getByRole("button", { name: "New token" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Description").fill(description);
  await dialog.getByRole("button", { name: "Create token" }).click();

  // The plaintext is shown exactly once, in the reveal dialog.
  const reveal = page.getByRole("dialog");
  await expect(reveal.getByText("This is shown once")).toBeVisible();
  const token = (await reveal.locator(".ff-code code").innerText()).trim();
  await reveal.getByRole("button", { name: "I have stored it" }).click();
  return token;
}

test("a token authenticates the API, a revoke kills it", async ({ browser, request }) => {
  const s = Date.now();
  const { context, page } = await registerPlayer(browser, {
    name: `Token ${s}`,
    email: `token-${s}@example.com`,
    password: "TokenPass123",
  });

  const description = `solver-${s}`;
  const token = await mintToken(page, description);

  // A fresh request context carries no session cookie, so a 200 here is the token's doing.
  const ok = await request.get("/api/v1/me", { headers: { Authorization: `Bearer ${token}` } });
  expect(ok.status()).toBe(200);

  // Revoke it through the UI.
  await page.getByRole("row", { name: new RegExp(description) }).getByRole("button", { name: "Revoke" }).click();
  const confirm = page.getByRole("dialog");
  await confirm.getByLabel(/Name of the token/).fill(description);
  await confirm.getByRole("button", { name: "Revoke token" }).click();
  await expect(page.getByRole("dialog")).toBeHidden();

  const revoked = await request.get("/api/v1/me", { headers: { Authorization: `Bearer ${token}` } });
  expect(revoked.status()).toBe(401);

  await context.close();
});

test("changing the password revokes existing tokens", async ({ browser, request }) => {
  const s = Date.now();
  const password = "PwTokenPass1";
  const { context, page } = await registerPlayer(browser, {
    name: `PwToken ${s}`,
    email: `pwtoken-${s}@example.com`,
    password,
  });

  const token = await mintToken(page, `ci-${s}`);
  const before = await request.get("/api/v1/me", { headers: { Authorization: `Bearer ${token}` } });
  expect(before.status()).toBe(200);

  // A password change is treated as a possible compromise: it must drop every token on the account.
  await page.goto("/settings?tab=security");
  await page.getByLabel("Current password").fill(password);
  await page.getByLabel("New password").fill("PwTokenPass2");
  await page.getByRole("button", { name: "Change password" }).click();
  await expect(page.locator(".ff-toast__title", { hasText: "Password changed" })).toBeVisible();

  const after = await request.get("/api/v1/me", { headers: { Authorization: `Bearer ${token}` } });
  expect(after.status()).toBe(401);

  await context.close();
});
